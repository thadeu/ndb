# frozen_string_literal: true

require "json"
require "uri"
require "time"

module Ndb
  Page = Struct.new(:rows, :next_cursor, :plan, :examined, :total)

  # The HTTP API of the daemon (plan §12.1). It builds JSON and reads
  # NDJSON; it has no query semantics.
  class Client
    DEFAULT_SOCKET = "/var/run/ndb/ndb.sock"

    attr_reader :pool, :endpoint

    RETRY_BASE = 0.05
    RETRY_CAP = 1.0

    # The address is, in order: url:, socket:, env NDB_URL, the default
    # socket. The token is token:, or env NDB_TOKEN. It goes only in the
    # Authorization header, and the unix socket does not need it.
    #
    # ready_timeout is how many seconds a call retries while the daemon is
    # starting or recovering (not_ready, socket missing or refusing), with
    # exponential backoff and jitter. 0 turns it off.
    def initialize(url: nil, socket: nil, token: nil, timeout: 30, pool_size: 16, ready_timeout: 10)
      @endpoint = Endpoint.parse(url || socket || presence(ENV["NDB_URL"]) || DEFAULT_SOCKET)
      @token = presence(token) || presence(ENV["NDB_TOKEN"])
      @pool = Pool.new(@endpoint, timeout: timeout, size: pool_size)
      @ready_timeout = ready_timeout.to_f
    end

    def socket = @endpoint.path

    def find(coll, body)
      page(post(coll, "find", Conditions.regexps(body)))
    end

    def aggregate(coll, body)
      page(post(coll, "aggregate", Conditions.regexps(body)))
    end

    # expire_in (seconds, or "30m" / "1h" / "1d") sets the expires_field of
    # every document to now + expire_in, in the same transaction.
    def insert(coll, docs, expire_in: nil)
      body = { insert: docs }
      body[:expire_in] = expire_in if expire_in

      json(post(coll, "insert", body)).fetch("results")
    end

    def update(coll, id, set: {}, unset: [], if_version: nil, expire_in: nil)
      change = { id: id, set: set, unset: unset }
      change[:if_version] = if_version if if_version
      change[:expire_in] = expire_in if expire_in

      json(post(coll, "update", update: change))
    end

    def delete(coll, id, if_version: nil)
      change = { id: id }
      change[:if_version] = if_version if if_version

      json(post(coll, "delete", delete: change))
    end

    # Makes a document expire, like EXPIRE in Redis: in: seconds or "30m", or
    # at: a Time or an RFC 3339 String. Returns {"id", "v", "expires_at"}.
    # Raises NotFound for a missing or expired document, TtlNotEnabled when
    # the collection has no expires_field, TtlDerived when the expiry comes
    # from ttl + time.
    def expire(coll, id, if_version: nil, **time)
      body = { id: id }
      body[:in] = time[:in] if time[:in]
      body[:at] = time[:at].respond_to?(:utc) ? time[:at].utc.iso8601(9) : time[:at] if time[:at]
      body[:if_version] = if_version if if_version

      json(post(coll, "expire", body))
    end

    # Removes the expiry of a document, like PERSIST in Redis.
    def persist(coll, id)
      json(post(coll, "persist", id: id))
    end

    # The seconds left of a document (an Integer, rounded up), -1 when it
    # never expires, nil when it is missing or expired.
    def ttl(coll, id)
      json(request("GET", "#{path(coll)}/ttl/#{URI.encode_www_form_component(id).gsub('+', '%20')}", nil)).fetch("ttl")
    rescue NotFound
      nil
    end

    # Streams the changes of a collection for a named consumer: first the ones
    # after its last ack, then the new ones. Yields one Hash per event (pos,
    # op, id, v, doc). where is the same Hash/Range conditions as find, and
    # only the matching events come; limit ends the stream after that many
    # events. Without a block it returns an Enumerator. Delivery is at least
    # once: ack the pos of an event when its work is done.
    #
    # The stream raises WatchLagged when the consumer is too slow, and
    # HistoryTruncated when its position is older than the kept WAL; call
    # watch again for the first, start over for the second.
    def watch(coll, consumer, where: nil, from: nil, limit: nil, &block)
      return enum_for(:watch, coll, consumer, where: where, from: from, limit: limit) unless block

      body = { consumer: consumer, from: from, limit: limit }.compact
      body[:where] = Conditions.equality(where) if where

      with_ready_retry { stream_events("#{path(coll)}/watch", body, &block) }
    end

    # Stores the position of an event for its consumer: the next watch with
    # that consumer name starts after it.
    def ack(coll, consumer, pos)
      json(post(coll, "ack", consumer: consumer, pos: pos))
    end

    def ensure_collection(coll, definition)
      json(request("PUT", path(coll), definition))
    end

    def collections
      json(request("GET", "/v1/c", nil)).fetch("collections")
    end

    # Uploads an IO (streamed) and returns the reference hash.
    def put_blob(io, content_type = nil)
      json(request("PUT", "/v1/blobs", io, headers: { "Content-Type" => content_type || "application/octet-stream" }))
    end

    # The bytes of a blob, or of its range from..to (inclusive).
    def get_blob(ref, from: nil, to: nil)
      headers = from ? { "Range" => "bytes=#{from}-#{to}" } : {}

      request("GET", "/v1/blobs/#{ref}", nil, headers: headers).body
    end

    private

    def presence(value)
      value.to_s.strip.empty? ? nil : value
    end

    def post(coll, verb, body)
      request("POST", "#{path(coll)}/#{verb}", body)
    end

    def path(coll)
      "/v1/c/#{URI.encode_www_form_component(coll).gsub('+', '%20')}"
    end

    def request(method, target, body, headers: {})
      payload = encode(body)
      headers = authorize(headers)

      safe = idempotent?(method, target) && !payload.respond_to?(:read)

      with_ready_retry(idempotent: safe) do
        response = @pool.with { |conn| conn.request(method, target, payload, headers: headers, idempotent: safe) }

        raise_api_error(response) if response.status >= 300

        response
      end
    end

    def idempotent?(method, target)
      method == "GET" || target.match?(%r{/(find|count|aggregate|get|stats)\z})
    end

    # Runs the block again while the daemon is not ready. It repeats only
    # when the request certainly was not processed (not_ready, a failed
    # connect), or for a read when the connection dropped. Never a timeout.
    def with_ready_retry(idempotent: false)
      deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + @ready_timeout
      delay = RETRY_BASE

      begin
        yield
      rescue NotReady, ConnectError, ConnectionError => e
        raise if @ready_timeout <= 0 || e.is_a?(TimeoutError)
        raise unless e.is_a?(NotReady) || e.is_a?(ConnectError) || idempotent

        wait = (delay / 2) + (rand * delay / 2)

        raise if Process.clock_gettime(Process::CLOCK_MONOTONIC) + wait > deadline

        sleep wait

        delay = [delay * 2, RETRY_CAP].min

        retry
      end
    end

    def authorize(headers)
      @token ? headers.merge("Authorization" => "Bearer #{@token}") : headers
    end

    # JSON for a Hash, "" for nothing, an IO as it is (blob upload).
    def encode(body)
      return "" if body.nil?
      return body if body.respond_to?(:read)

      JSON.generate(body)
    end

    def json(response)
      JSON.parse(response.body)
    end

    def page(response)
      headers = response.headers

      Page.new(
        ndjson(response.body),
        headers["x-ndb-next"],
        headers["x-ndb-plan"],
        headers["x-ndb-examined"]&.to_i,
        headers["x-ndb-total"]&.to_i
      )
    end

    def ndjson(body)
      body.each_line.filter_map do |line|
        line = line.strip

        JSON.parse(line) unless line.empty?
      end
    end

    def stream_events(target, body, &block)
      pending = +""

      response = @pool.with do |conn|
        conn.stream("POST", target, JSON.generate(body), headers: authorize({})) do |chunk|
          pending << chunk

          while (newline = pending.index("\n"))
            emit_event(pending.slice!(0, newline + 1), &block)
          end
        end
      end

      raise_api_error(response) if response.status >= 300
    end

    # A heartbeat is skipped; an error line raises.
    def emit_event(line)
      line = line.strip

      return if line.empty?

      event = JSON.parse(line)

      return if event["heartbeat"]

      if (error = event["error"])
        raise Ndb.error_for(error["code"], error["message"], 200)
      end

      yield event
    end

    def raise_api_error(response)
      parsed = JSON.parse(response.body)

      raise Ndb.error_for("not_ready", "ndb is recovering", 503) if response.status == 503 && parsed["status"] == "recovering"

      error = parsed.fetch("error")

      raise Ndb.error_for(error["code"], error["message"], response.status)
    rescue JSON::ParserError, KeyError
      raise Error.new("HTTP #{response.status}: #{response.body.strip}", code: "http_status", status: response.status)
    end
  end
end

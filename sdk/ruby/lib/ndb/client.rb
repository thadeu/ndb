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

    # The document with this _id (and its "_v"), or nil.
    def get(coll, id)
      json(request("GET", id_path(coll, id), nil))
    rescue NotFound
      nil
    end

    # Each document gets a generated UUIDv7 _id unless it carries one.
    # expire_in (seconds, or "30m" / "1h" / "1d") sets the expires_field of
    # every document to now + expire_in, in the same transaction.
    def insert(coll, docs, expire_in: nil)
      body = { insert: docs }
      body[:expire_in] = expire_in if expire_in

      json(post(coll, "insert", body)).fetch("results")
    end

    # Atomic update. A String is the _id of one document (POST /{_id}/update);
    # a Hash is a filter (POST /update). The filter and the write operators go
    # in one body; the daemon matches and writes in one step on the newest state, so
    # concurrent updates never interleave.
    #
    #   client.update("stock", { sku: "a", qty: { "$gte" => 1 } }, { "$inc" => { qty: -1 } })
    #   # => {"matched" => 1, "modified" => 1, "doc" => {...with "_v"}}
    #
    # Operators: $set, $setOnInsert, $unset, $inc, $push, $pull, $min, $max (dot paths work).
    # Nothing matched is not an error: {"matched" => 0, "modified" => 0}. A
    # version guard is a filter: { _id: "x", _v: 7 }. many: true updates every
    # match (no "doc" in the answer) and needs _id or an indexed field in the
    # filter; it does not apply to an _id String. upsert: true creates the document from the equality fields
    # of the filter when nothing matches. expire_in (seconds, or "30m") sets
    # the expires_field.
    def update(coll, id_or_filter, ops, many: false, upsert: false, expire_in: nil)
      if id_or_filter.is_a?(String)
        query = write_query(upsert: upsert, expire_in: expire_in)

        return json(request("POST", "#{id_path(coll, id_or_filter)}/update#{query}", merge_update({}, ops)))
      end

      query = write_query(many: many, upsert: upsert, expire_in: expire_in)

      json(post(coll, "update#{query}", merge_update(id_or_filter, ops)))
    end

    # Deletes by _id (a String), or the documents that the filter matches (one,
    # or every match with many: true, which needs _id or an indexed field).
    # {"deleted" => n}
    def delete(coll, id_or_filter, many: false)
      return json(request("DELETE", id_path(coll, id_or_filter), nil)) if id_or_filter.is_a?(String)

      json(post(coll, "delete#{write_query(many: many)}", id_or_filter))
    end

    # Makes a document expire, like EXPIRE in Redis: in: seconds or "30m", or
    # at: a Time or an RFC 3339 String. Returns {"_id", "v", "expires_at"}.
    # Raises NotFound for a missing or expired document, TtlNotEnabled when
    # the collection has no expires_field, TtlDerived when the expiry comes
    # from ttl + time.
    def expire(coll, id, **time)
      body = { _id: id }
      body[:in] = time[:in] if time[:in]
      body[:at] = time[:at].respond_to?(:utc) ? time[:at].utc.iso8601(9) : time[:at] if time[:at]

      json(post(coll, "expire", body))
    end

    # Removes the expiry of a document, like PERSIST in Redis.
    def persist(coll, id)
      json(post(coll, "persist", _id: id))
    end

    # The seconds left of a document (an Integer, rounded up), -1 when it
    # never expires, nil when it is missing or expired.
    def ttl(coll, id)
      json(request("GET", "#{id_path(coll, id)}/ttl", nil)).fetch("ttl")
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

    # Removes every document of the collection and keeps its definition and
    # indexes. The cost does not depend on the number of documents. Watch
    # consumers get one event with op "truncate". {"collection", "truncated"}
    def truncate(coll)
      json(request("POST", "#{path(coll)}/truncate?confirm=#{confirm(coll)}", nil))
    end

    # Removes the collection: its documents, its definition and the positions
    # of its watch consumers. A collection declared in ndb.yml comes back,
    # empty, when the daemon restarts. {"collection", "dropped"}
    def drop(coll)
      json(request("DELETE", "#{path(coll)}?confirm=#{confirm(coll)}", nil))
    end

    def ensure_collection(coll, definition = {})
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

    def write_query(many: false, upsert: false, expire_in: nil)
      params = {}

      params["many"] = "true" if many
      params["upsert"] = "true" if upsert
      params["expire_in"] = expire_in.to_s if expire_in

      params.empty? ? "" : "?#{URI.encode_www_form(params)}"
    end

    # The filter and the operators in one body. An operator key without "$"
    # would be read as a filter key by the daemon, so it is refused here.
    def merge_update(filter, ops)
      ops.each_key do |key|
        next if key.to_s.start_with?("$")

        raise ArgumentError, "update operator #{key.to_s.inspect} must start with $ (for example \"$set\")"
      end

      filter.to_h.transform_keys(&:to_s).merge(ops.to_h.transform_keys(&:to_s))
    end

    def post(coll, verb, body)
      request("POST", "#{path(coll)}/#{verb}", body)
    end

    def path(coll)
      "/v1/c/#{URI.encode_www_form_component(coll).gsub('+', '%20')}"
    end

    def confirm(coll)
      URI.encode_www_form_component(coll)
    end

    def id_path(coll, id)
      "#{path(coll)}/#{URI.encode_www_form_component(id).gsub('+', '%20')}"
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

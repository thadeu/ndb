# frozen_string_literal: true

require "json"
require "uri"
require "time"

module Ndb
  # One page of a search or an aggregate: the rows, the cursor of the next
  # page (nil on the last one), the plan, how many rows were examined, and
  # the total (with count: true).
  Page = Data.define(:rows, :next, :plan, :examined, :total)

  # The HTTP API of the daemon, one method per route. It builds JSON and
  # reads the answers; it has no query semantics. Ndb::Collection is the
  # friendlier layer on top of it.
  class Client
    # The x* commands of redis-rb (xadd, xreadgroup, ...) on ndb streams.
    include RedisCommands

    DEFAULT_SOCKET = "/run/ndb/ndb.sock"

    attr_reader :pool, :endpoint, :user_agent

    # The User-Agent of the gem: its version, the Ruby version and the
    # platform.
    USER_AGENT = "ndb-ruby/#{VERSION} (ruby #{RUBY_VERSION}; #{RUBY_PLATFORM})".freeze

    RETRY_BASE = 0.05
    RETRY_CAP = 1.0

    # The address is, in order: url:, socket:, env NDB_URL, the default
    # socket. The token is token:, or env NDB_TOKEN. It goes only in the
    # Authorization header, and the unix socket does not need it.
    #
    # ready_timeout is how many seconds a call retries while the daemon is
    # starting or recovering (not_ready, socket missing or refusing), with
    # exponential backoff and jitter. 0 turns it off.
    #
    # user_agent is the token of the app (for example "myapp/1.2"). It goes
    # after the User-Agent of the gem, so the daemon can tell the apps apart.
    def initialize(url: nil, socket: nil, token: nil, timeout: 30, pool_size: 16, ready_timeout: 10, user_agent: nil)
      @endpoint = Endpoint.parse(url || socket || presence(ENV["NDB_URL"]) || DEFAULT_SOCKET)
      @token = presence(token) || presence(ENV["NDB_TOKEN"])
      @pool = Pool.new(@endpoint, timeout: timeout, size: pool_size)
      @ready_timeout = ready_timeout.to_f
      @user_agent = [USER_AGENT, presence(user_agent.to_s.strip)].compact.join(" ")
    end

    def socket = @endpoint.path

    # A collection (or a pattern such as "logs-2026-*") with the query
    # builder. definition is what the first call declares (PUT /{coll}).
    def collection(name, definition = {})
      Collection.new(self, name, definition)
    end

    # A stream (Redis Streams model) with its groups. See Ndb::Stream.
    def stream(name)
      Stream.new(self, name)
    end

    # One request to a stream route: /{coll}/{action} with a JSON body (nil:
    # none). It returns the parsed JSON, or an Array of rows for an NDJSON
    # answer. timeout (seconds) replaces the read timeout of this request.
    def stream_request(method, coll, action, body, timeout: nil)
      target = action.to_s.empty? ? path(coll) : "#{path(coll)}/#{action}"
      response = request(method, target, body, timeout: timeout)

      return ndjson(response.body) if response.headers["content-type"].to_s.include?("ndjson")

      body = response.body.to_s.strip

      body.empty? ? nil : JSON.parse(body)
    end

    # The timeout of the connections of this client, in seconds.
    def timeout = @pool.timeout

    # A path segment escaped for a URL (a group or consumer name).
    def escape_segment(segment) = escape(segment)

    # POST /{coll}/_search: body is { "query", "sort", "fields", "limit",
    # "skip", "after", "count", ... }. Returns a Page.
    def search(coll, body = {})
      page(post(coll, "_search", Conditions.regexps(body)))
    end

    # POST /{coll}/_count: how many documents match query.
    def count(coll, query = nil)
      body = query ? { query: Conditions.regexps(query) } : {}

      json(post(coll, "_count", body)).fetch("count")
    end

    # POST /{coll}/_aggregate: body is { "pipeline", "limit", "skip",
    # "after", "count" }. Returns a Page.
    def aggregate(coll, body)
      page(post(coll, "_aggregate", Conditions.regexps(body)))
    end

    # The document with this _id (and its "_v"), or nil.
    def get(coll, id)
      json(request("GET", doc_path(coll, "_doc", id), nil))
    rescue NotFound
      nil
    end

    # POST /{coll}/_doc: one document; it gets a generated UUIDv7 _id unless
    # it carries one. Returns {"_id", "_v"}.
    def insert(coll, doc)
      json(post(coll, "_doc", doc))
    end

    # POST /{coll}/_bulk: many documents in one transaction, all or nothing.
    # expire_in (seconds, or "30m" / "1h" / "1d") sets the expires_field of
    # every document to now + expire_in. Returns the results.
    #
    # upsert: true merges a document whose _id exists into the stored one
    # (its fields replace the fields of the same name; the other fields
    # stay), like doc_as_upsert in Elasticsearch, instead of duplicate_id.
    def bulk(coll, docs, expire_in: nil, upsert: false)
      body = { docs: docs }
      body[:expire_in] = expire_in if expire_in
      body[:upsert] = true if upsert

      json(post(coll, "_bulk", body)).fetch("results")
    end

    # POST /{coll}/_update/{id}: applies the write operators to the document
    # with this _id, in one atomic step. guard adds conditions on the same
    # document ({ _v: 7 }); matched is 0 when they fail.
    #
    #   client.update("stock", "a", { "$inc" => { qty: -1 } }, guard: { qty: { "$gte" => 1 } })
    #   # => {"matched" => 1, "modified" => 1, "doc" => {...with "_v"}}
    #
    # Operators: $set, $setOnInsert, $unset, $inc, $push, $pull, $min, $max
    # (dot paths work). upsert: true creates the document with this _id.
    #
    # lock: the token of a lock on the document (see lock): the write of
    # its holder. update_by, delete and delete_by take it too.
    def update(coll, id, ops, guard: nil, upsert: false, expire_in: nil, lock: nil)
      body = write_body(update: check_ops(ops), query: guard && Conditions.equality(guard), upsert: upsert, expire_in: expire_in)

      json(request("POST", held(doc_path(coll, "_update", id), lock), body))
    end

    # POST /{coll}/_lock/{id}: the pessimistic lock of a document. Until
    # unlock, or ttl (seconds or "30s"; default 30 s, up to 5 min), only a
    # write with the token changes the document; a write without it waits.
    # When another caller holds the lock, the call waits up to wait (default
    # 10 s, up to 1 min; 0 does not wait), then raises Locked. Returns
    # {"token", "expires_at", "doc"}. A missing document raises NotFound.
    def lock(coll, id, ttl: nil, wait: nil)
      timeout = wait.is_a?(Numeric) ? [self.timeout, wait + 5].max : nil

      json(request("POST", doc_path(coll, "_lock", id), write_body(ttl: ttl, wait: wait), timeout: timeout))
    end

    # POST /{coll}/_lock: the locks of a set of documents, all under one
    # token, or none of them (no deadlock between two sets). ttl and wait as
    # for lock; up to 100 ids. Returns {"token", "expires_at", "docs"}.
    def lock_set(coll, ids, ttl: nil, wait: nil)
      timeout = wait.is_a?(Numeric) ? [self.timeout, wait + 5].max : nil

      json(request("POST", "#{path(coll)}/_lock", write_body(ids: ids, ttl: ttl, wait: wait), timeout: timeout))
    end

    # POST /{coll}/_unlock: releases every lock of token. Returns
    # {"unlocked" => n}; a token that holds nothing raises LockExpired.
    def unlock_set(coll, token)
      json(request("POST", "#{path(coll)}/_unlock", { token: token }))
    end

    # POST /{coll}/_tx: inserts, updates and deletes of one collection in one
    # step, all of them or none. ops: [{op: "insert", doc:}, {op: "update",
    # id:, update:, query:}, {op: "delete", id:, query:}]. locks: the tokens
    # the transaction holds; unlock: true releases them after the call.
    # Returns {"results" => [...]}. An update or a delete that matches
    # nothing raises TxConflict.
    def tx(coll, ops, locks: [], unlock: false)
      json(request("POST", "#{path(coll)}/_tx", write_body(ops: ops, locks: locks, unlock: unlock)))
    end

    # POST /{coll}/_unlock/{id}: releases the lock of token. A token that
    # holds no lock (expired, released) raises LockExpired.
    def unlock(coll, id, token)
      json(request("POST", doc_path(coll, "_unlock", id), { token: token }))
    end

    # POST /{coll}/_update_by_query: the atomic update by query. The daemon
    # matches and writes in one step on the newest state, so concurrent
    # updates never interleave. Without many it updates the first match;
    # many: true updates every match and needs _id or an indexed field in
    # the query. upsert: true creates the document from the equality fields
    # of the query when nothing matches.
    def update_by(coll, query, ops, many: false, upsert: false, expire_in: nil, lock: nil)
      body = write_body(query: Conditions.equality(query), update: check_ops(ops), many: many, upsert: upsert, expire_in: expire_in)

      json(post(coll, held("_update_by_query", lock), body))
    end

    # DELETE /{coll}/_doc/{id}: {"deleted" => 0 | 1}.
    def delete(coll, id, lock: nil)
      json(request("DELETE", held(doc_path(coll, "_doc", id), lock), nil))
    end

    # POST /{coll}/_delete_by_query: the first match, or every match with
    # many: true (same rule as update_by). {"deleted" => n}
    def delete_by(coll, query, many: false, lock: nil)
      json(post(coll, held("_delete_by_query", lock), write_body(query: Conditions.equality(query), many: many)))
    end

    # Makes a document expire, like EXPIRE in Redis: in: seconds or "30m", or
    # at: a Time or an RFC 3339 String. Returns {"_id", "_v", "expires_at"}.
    # Raises NotFound for a missing or expired document, TtlNotEnabled when
    # the collection has no expires_field, TtlDerived when the expiry comes
    # from ttl + time.
    def expire(coll, id, **time)
      body = {}
      body[:in] = time[:in] if time[:in]
      body[:at] = time[:at].respond_to?(:utc) ? time[:at].utc.iso8601(9) : time[:at] if time[:at]

      json(request("POST", doc_path(coll, "_expire", id), body))
    end

    # Removes the expiry of a document, like PERSIST in Redis.
    def persist(coll, id)
      json(request("POST", doc_path(coll, "_persist", id), nil))
    end

    # The seconds left of a document (an Integer, rounded up), -1 when it
    # never expires, nil when it is missing or expired.
    def ttl(coll, id)
      json(request("GET", doc_path(coll, "_ttl", id), nil)).fetch("ttl")
    rescue NotFound
      nil
    end

    # Streams the changes of a collection for a named consumer: first the ones
    # after its last ack, then the new ones. Yields one Hash per event (pos,
    # op, _id, _v, doc). query is the same conditions as a search, and only
    # the matching events come; limit ends the stream after that many events.
    # Without a block it returns an Enumerator. Delivery is at least once: ack
    # the pos of an event when its work is done.
    #
    # The stream raises WatchLagged when the consumer is too slow, and
    # HistoryTruncated when its position is older than the kept WAL; call
    # watch again for the first, start over for the second.
    def watch(coll, consumer, query: nil, from: nil, limit: nil, &block)
      return enum_for(:watch, coll, consumer, query: query, from: from, limit: limit) unless block

      body = { consumer: consumer, from: from, limit: limit }.compact
      body[:query] = Conditions.equality(query) if query

      with_ready_retry { stream_events("#{path(coll)}/_watch", body, &block) }
    end

    # Stores the position of an event for its consumer: the next watch with
    # that consumer name starts after it.
    def ack(coll, consumer, pos)
      json(post(coll, "_ack", consumer: consumer, pos: pos))
    end

    # Removes every document of the collection and keeps its definition and
    # indexes. The cost does not depend on the number of documents. Watch
    # consumers get one event with op "truncate". {"collection", "truncated"}
    def truncate(coll)
      json(request("POST", "#{path(coll)}/_truncate?confirm=#{confirm(coll)}", nil))
    end

    # Removes the collection: its documents, its definition and the positions
    # of its watch consumers. A collection declared in ndb.yml comes back,
    # empty, when the daemon restarts. {"collection", "dropped"}
    def drop(coll)
      json(request("DELETE", "#{path(coll)}?confirm=#{confirm(coll)}", nil))
    end

    # Gives the collection the name +to+: documents, indexes and watch
    # positions move with it. With +replace+, an existing +to+ goes aside
    # first; without it, an existing +to+ raises CollectionExists.
    # {"collection", "renamed_from", "replaced"}
    def rename(coll, to, replace: false)
      json(post(coll, "_rename", { to: to, replace: replace }))
    end

    # Makes the collection +to+ hold +coll+ as it was at +at+ (a Time or
    # an RFC 3339 string), from the PITR window of the daemon (1 day by
    # default). +coll+ may be dropped while it is in the window. Raises
    # OutOfWindow, PitrOff or CollectionExists.
    # {"collection", "restored", "at", "duration_ms"}
    def restore(coll, at:, to:)
      at = at.utc.iso8601(9) if at.respond_to?(:utc)

      json(post(coll, "_restore", { at: at, to: to }))
    end

    # The PITR window of the collection: {"collection", "window", "oldest",
    # "newest", "snapshots", "bytes"}.
    def pitr(coll)
      json(request("GET", "#{path(coll)}/_pitr", nil))
    end

    # PUT /{coll}: creates the collection or merges the definition into it.
    def ensure_collection(coll, definition = {})
      json(request("PUT", path(coll), definition))
    end

    # PUT /{coll}/_indexes/{field}: adds an index, built in the background.
    def ensure_index(coll, field)
      json(request("PUT", doc_path(coll, "_indexes", field), nil)).fetch("indexes")
    end

    # DELETE /{coll}/_indexes/{field}.
    def drop_index(coll, field)
      json(request("DELETE", doc_path(coll, "_indexes", field), nil)).fetch("indexes")
    end

    # GET /{coll}/_stats: rows, indexes, files and bytes of one collection.
    def collection_stats(coll)
      json(request("GET", "#{path(coll)}/_stats", nil))
    end

    # POST /{coll}/_compact: seals and merges every segment now.
    def compact(coll)
      json(post(coll, "_compact", nil))
    end

    # GET /_health: true when the daemon is ready; raises NotReady while it
    # recovers. It does not wait.
    def health
      request("GET", "/_health", nil, wait: false)

      true
    end

    # GET /_collections: one definition per collection.
    def collections
      json(request("GET", "/_collections", nil)).fetch("collections")
    end

    # GET /_stats: every collection, totals, version and uptime.
    def stats
      json(request("GET", "/_stats", nil))
    end

    # GET /_size: disk use per collection and per part.
    def size
      json(request("GET", "/_size", nil))
    end

    # Uploads an IO (streamed) and returns the reference hash.
    def put_blob(io, content_type = nil)
      json(request("POST", "/_blobs", io, headers: { "Content-Type" => content_type || "application/octet-stream" }))
    end

    # The bytes of a blob, or of its range from..to (inclusive).
    def get_blob(ref, from: nil, to: nil)
      headers = from ? { "Range" => "bytes=#{from}-#{to}" } : {}

      request("GET", "/_blobs/#{ref}", nil, headers: headers).body
    end

    private

    def presence(value)
      value.to_s.strip.empty? ? nil : value
    end

    # The body of a write, without the options that are off.
    def write_body(**parts)
      parts.reject { |_, v| v.nil? || v == false }
    end

    # An operator key without "$" is a mistake, so it is refused here.
    def check_ops(ops)
      ops.each_key do |key|
        next if key.to_s.start_with?("$")

        raise ArgumentError, "update operator #{key.to_s.inspect} must start with $ (for example \"$set\")"
      end

      ops
    end

    def post(coll, action, body)
      request("POST", "#{path(coll)}/#{action}", body)
    end

    def path(coll)
      "/#{escape(coll)}"
    end

    # target as the write of the holder of a lock (?lock=<token>).
    def held(target, token)
      token ? "#{target}?lock=#{escape(token)}" : target
    end

    def escape(segment)
      URI.encode_www_form_component(segment).gsub("+", "%20")
    end

    def confirm(coll)
      URI.encode_www_form_component(coll)
    end

    def doc_path(coll, action, id)
      "#{path(coll)}/#{action}/#{escape(id)}"
    end

    def request(method, target, body, headers: {}, wait: true, timeout: nil)
      payload = encode(body)
      headers = authorize(headers)

      safe = idempotent?(method, target) && !payload.respond_to?(:read)
      ready = wait ? @ready_timeout : 0

      with_ready_retry(idempotent: safe, timeout: ready) do
        response = @pool.with { |conn| conn.request(method, target, payload, headers: headers, idempotent: safe, timeout: timeout) }

        raise_api_error(response) if response.status >= 300

        response
      end
    end

    def idempotent?(method, target)
      method == "GET" || target.match?(%r{/(_search|_count|_aggregate)\z})
    end

    # Runs the block again while the daemon is not ready. It repeats only
    # when the request certainly was not processed (not_ready, a failed
    # connect), or for a read when the connection dropped. Never a timeout.
    def with_ready_retry(idempotent: false, timeout: @ready_timeout)
      deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + timeout
      delay = RETRY_BASE

      begin
        yield
      rescue NotReady, ConnectError, ConnectionError => e
        raise if timeout <= 0 || e.is_a?(TimeoutError)
        raise unless e.is_a?(NotReady) || e.is_a?(ConnectError) || idempotent

        wait = (delay / 2) + (rand * delay / 2)

        raise if Process.clock_gettime(Process::CLOCK_MONOTONIC) + wait > deadline

        sleep wait

        delay = [delay * 2, RETRY_CAP].min

        retry
      end
    end

    # Adds the User-Agent, and the bearer token when the client has one.
    def authorize(headers)
      headers = headers.merge("User-Agent" => @user_agent)

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

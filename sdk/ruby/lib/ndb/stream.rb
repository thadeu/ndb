# frozen_string_literal: true

module Ndb
  # One entry of a stream. id is "<ms>-<seq>", time is the ms of the id,
  # doc is the Hash without "_id" and "_ts". A deleted entry was pending, but
  # it is no longer in the stream (a trim or a delete); its doc is nil.
  Entry = Data.define(:id, :time, :doc, :deleted, :raw) do
    def deleted? = deleted

    # The entry from one row of the daemon.
    def self.from(row)
      id = row["_id"]

      if row["_deleted"]
        return new(id: id, time: Stream.id_time(id), doc: nil, deleted: true, raw: row)
      end

      doc = row.reject { |key, _| key == "_id" || key == "_ts" }

      new(id: id, time: Stream.id_time(id), doc: doc, deleted: false, raw: row)
    end
  end

  # The answer of Group#claim and Group#auto_claim. cursor is where the next
  # auto_claim starts ("0-0" when the scan is complete; nil for claim).
  # deleted holds the pending IDs whose entry is no longer in the stream.
  Claimed = Data.define(:cursor, :entries, :deleted)

  # A stream: an append-only log with consumer groups, with the behavior of
  # Redis Streams (see docs/dev/streams.md). Get one with client.stream(name).
  # It sends nothing until the first call.
  class Stream
    # The group reads only the entries appended after it ($).
    START_NEW = "$"

    # The group reads every entry of the stream (0).
    START_ALL = "0"

    # A blocking read waits this many seconds more than block before the
    # client gives up on the answer.
    BLOCK_MARGIN = 5

    attr_reader :client, :name

    # The time of an entry ID (its ms part), or nil for a bad ID.
    def self.id_time(id)
      ms, _, seq = id.to_s.partition("-")

      return nil if seq.empty? || !ms.match?(/\A\d+\z/)

      Time.at(ms.to_i / 1000, ms.to_i % 1000, :millisecond).utc
    end

    def initialize(client, name)
      @client = client
      @name = name
    end

    # Makes the stream, or checks that name is one. append and Group#create
    # make it too; create is for a stream with options (indexes, retention).
    def create(definition = {})
      call("PUT", "", definition.merge(stream: true))
    end

    # Adds one entry and returns its ID (XADD). id: "<ms>-<seq>" or
    # "<ms>-*" (default "*": the daemon makes it). trim: a Hash as in #trim,
    # run after the append. no_create: true raises UnknownCollection when the
    # stream does not exist.
    def append(doc, id: nil, trim: nil, no_create: false)
      body = append_body({ doc: doc }, id, trim, no_create)

      call("POST", "_streams/append", body).fetch("_id")
    end

    # Adds the entries in order, with one fsync for all, and returns their
    # IDs. id: applies to the first entry.
    def append_many(docs, id: nil, trim: nil, no_create: false)
      body = append_body({ docs: docs }, id, trim, no_create)

      call("POST", "_streams/append", body).fetch("ids")
    end

    # Deletes old entries and returns how many (XTRIM). max_len: keeps the
    # newest N entries; min_id: deletes the entries with a lower ID.
    # approx: true deletes only whole blocks of 1 024 entries; limit caps
    # the count with approx.
    def trim(max_len: nil, min_id: nil, approx: false, limit: nil)
      body = trim_body(max_len: max_len, min_id: min_id, approx: approx, limit: limit)

      call("POST", "_streams/trim", body).fetch("deleted")
    end

    # The state of the stream (XINFO STREAM): "length", "first_id",
    # "last_id", "entries_added", "max_deleted_id", "groups".
    def info
      call("GET", "_streams", nil)
    end

    # The groups of the stream (XINFO GROUPS): one Hash per group with
    # "name", "consumers", "pending", "last_delivered_id", "entries_read",
    # "lag" (nil when it cannot be known).
    def groups
      call("GET", "_streams/groups", nil) || []
    end

    # Up to count entries with an ID from start to stop, both included, in
    # ID order (XRANGE). "-" and "+" are the lowest and the highest ID.
    def range(start = "-", stop = "+", count: nil)
      range_of(start, stop, count, 1)
    end

    # range from stop down to start (XREVRANGE).
    def rev_range(stop = "+", start = "-", count: nil)
      range_of(start, stop, count, -1)
    end

    # Up to count entries with an ID greater than after, in ID order, without
    # a group (XREAD). after is an ID ("0": from the start) or "$": only the
    # entries appended after the request starts. block (seconds, a Float is
    # fine) waits for an entry when there is none. To read on, give the last
    # ID of the answer as after (not "$" again). Returns an Array of Entry.
    def read(after: START_ALL, count: nil, block: nil, fields: nil)
      body = { after: after, count: count, fields: fields }.compact

      entries("_streams/read", body, **block_options(body, block))
    end

    # Removes one entry by ID (XDEL). Returns 1, or 0 when it is not there.
    def delete(id)
      call("DELETE", "_doc/#{@client.escape_segment(id)}", nil).fetch("deleted")
    end

    # The consumer group name of the stream. It sends nothing.
    def group(name)
      Group.new(self, name)
    end

    # One request to /{stream}/{action}. A Hash, an Array of rows, or nil.
    def call(method, action, body, timeout: nil)
      @client.stream_request(method, @name, action, body, timeout: timeout)
    end

    # The entries of a request whose answer is rows.
    def entries(action, body, timeout: nil)
      rows = call("POST", action, body, timeout: timeout) || []

      rows.map { |row| Entry.from(row) }
    end

    # Puts block (seconds) in body as block_ms. Returns the options of the
    # request: a read timeout long enough for the wait.
    def block_options(body, block)
      block_ms = block ? (block * 1000).round : 0

      return {} unless block_ms.positive?

      body[:block_ms] = block_ms

      { timeout: [@client.timeout, block + BLOCK_MARGIN].max }
    end

    private

    def append_body(body, id, trim, no_create)
      body[:id] = id if id
      body[:trim] = trim_body(**trim) if trim
      body[:no_create] = true if no_create

      body
    end

    def trim_body(max_len: nil, min_id: nil, approx: false, limit: nil)
      body = { max_len: max_len, min_id: min_id, limit: limit }.compact
      body[:approx] = true if approx

      body
    end

    def range_of(start, stop, count, order)
      body = { query: { _id: { "$gte" => start, "$lte" => stop } }, sort: { _id: order } }
      body[:limit] = count if count

      entries("_search", body)
    end
  end

  # One consumer group of a stream. Each entry goes to one consumer of the
  # group and stays pending until it is acked.
  class Group
    BLOCK_MARGIN = Stream::BLOCK_MARGIN

    attr_reader :stream, :name

    def initialize(stream, name)
      @stream = stream
      @name = name
    end

    # Makes the group (XGROUP CREATE ... MKSTREAM). start is Stream::START_NEW
    # ("$"), Stream::START_ALL ("0") or an ID. A group that exists raises
    # GroupExists. no_create: true raises UnknownCollection when the stream
    # does not exist.
    def create(start = Stream::START_NEW, no_create: false)
      body = { start: start }
      body[:no_create] = true if no_create

      @stream.call("PUT", path, body)
    end

    # Makes the group, or does nothing when it exists.
    def ensure(start = Stream::START_NEW)
      create(start)
    rescue GroupExists
      nil
    end

    # Moves the last delivered ID of the group (XGROUP SETID).
    def set_id(id)
      @stream.call("POST", path("setid"), { id: id })
    end

    # Deletes the group and its pending entries (XGROUP DESTROY).
    def destroy
      @stream.call("DELETE", path, nil)
    end

    # Makes a consumer. True when it is new. A read makes the consumer too.
    def create_consumer(consumer)
      @stream.call("PUT", path("consumers", consumer), nil).fetch("created")
    end

    # Deletes a consumer and its pending entries. Returns how many it held
    # (XGROUP DELCONSUMER).
    def delete_consumer(consumer)
      @stream.call("DELETE", path("consumers", consumer), nil).fetch("pending")
    end

    # The consumers of the group (XINFO CONSUMERS): "name", "pending",
    # "idle_ms", "inactive_ms".
    def consumers
      @stream.call("GET", path("consumers"), nil) || []
    end

    # Gives entries to consumer (XREADGROUP): new ones by default, which
    # become pending for consumer. pending: true gives its own pending ones
    # with an ID greater than after. block (seconds, a Float is fine) waits
    # for a new entry when there is none. Returns an Array of Entry.
    def read(consumer, count: nil, block: nil, pending: false, after: nil, no_ack: false, fields: nil)
      body = { consumer: consumer, count: count, after: after, fields: fields }.compact
      body[:pending] = true if pending
      body[:no_ack] = true if no_ack

      @stream.entries(path("read"), body, **@stream.block_options(body, block))
    end

    # Ends the work on the entries. Returns how many were pending (XACK). An
    # ID that is not pending counts 0.
    def ack(*ids)
      ids = ids.flatten

      return 0 if ids.empty?

      @stream.call("POST", path("ack"), { ids: ids }).fetch("acked")
    end

    # The pending list in short (XPENDING): "count", "lowest", "highest",
    # "consumers" ([{"name", "count"}]).
    def pending
      @stream.call("POST", path("pending"), {})
    end

    # Pending entries in ID order (XPENDING with a range). Each one is a Hash
    # with "_id", "consumer", "idle_ms", "deliveries". idle: seconds; only
    # the entries idle for at least this long.
    def pending_list(start: "-", stop: "+", count: 100, consumer: nil, idle: nil)
      body = { start: start, end: stop, count: count, consumer: consumer }.compact
      body[:idle_ms] = (idle * 1000).round if idle && idle.positive?

      @stream.call("POST", path("pending"), body) || []
    end

    # Moves the entries idle for at least min_idle seconds to consumer
    # (XCLAIM). Each one gets one more delivery. Returns a Claimed.
    # idle: (seconds) sets the idle time of the entries; retry_count: sets
    # their delivery count; force: true also takes an ID that is not pending.
    # just_ids: true gives IDs (Strings) as entries, and the delivery count
    # does not change.
    def claim(consumer, *ids, min_idle: 0, idle: nil, retry_count: nil, force: false, just_ids: false)
      body = { consumer: consumer, min_idle_ms: (min_idle * 1000).round, ids: ids.flatten }
      body[:idle_ms] = (idle * 1000).round if idle
      body[:retry_count] = retry_count if retry_count
      body[:force] = true if force

      claim_with(body, just_ids)
    end

    # Scans the pending list from cursor and moves up to count entries idle
    # for at least min_idle seconds to consumer (XAUTOCLAIM). just_ids: as
    # in #claim.
    def auto_claim(consumer, min_idle: 0, cursor: "0-0", count: nil, just_ids: false)
      body = { consumer: consumer, min_idle_ms: (min_idle * 1000).round, cursor: cursor, count: count }.compact

      claim_with(body, just_ids)
    end

    private

    def path(*parts)
      segments = ["_streams", "groups", @name, *parts].map { |part| @stream.client.escape_segment(part) }

      segments.join("/")
    end

    def claim_with(body, just_ids = false)
      body[:just_ids] = true if just_ids

      out = @stream.call("POST", path("claim"), body)
      entries = (out["entries"] || []).map { |row| row.is_a?(Hash) ? Entry.from(row) : row }

      Claimed.new(cursor: out["cursor"], entries: entries, deleted: out["deleted"] || [])
    end
  end
end

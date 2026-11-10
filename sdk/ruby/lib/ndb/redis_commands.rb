# frozen_string_literal: true

require "json"

module Ndb
  # The stream commands of the redis-rb gem (Redis::Commands::Streams), on ndb
  # streams. Ndb::Client includes it, so an app that uses Redis Streams picks
  # its backend in one place and keeps its calls:
  #
  #   STREAMS = ENV["STREAMS_BACKEND"] == "ndb" ? Ndb.connect : Redis.new
  #
  #   STREAMS.xadd("orders", { order_id: 42 }, maxlen: 1000)
  #
  # The arguments and the answers have the shapes of redis-rb 5. What is
  # different from Redis:
  #
  # - Values. Redis keeps strings. ndb keeps the JSON value that xadd got
  #   (a number stays a number, a nested Hash stays an object). A read gives
  #   each value as a String: a string as it is, a number or a boolean with
  #   to_s, null as "", a nested value as its JSON text.
  # - A field name cannot start with "_".
  # - One stream for each request. Many keys without block: are one request
  #   for each key. Many keys with block: raise Ndb::NotSupported.
  # - Errors are Ndb::Error subclasses, not Redis::CommandError. The message
  #   starts as in Redis where an app looks at it ("BUSYGROUP", "NOGROUP").
  # - xclaim has no time: option. xinfo(:stream) has no radix-tree keys.
  module RedisCommands
    # A read with block: 0 waits with no limit, as in Redis. It is a loop of
    # reads that wait this many seconds each.
    FOREVER_STEP = 30

    # A blocking xread on a stream that does not exist looks for it again
    # after this many seconds.
    MISSING_STEP = 0.5

    # A read without count: gives up to this many entries for each stream
    # (the most of one ndb read). Redis gives every entry.
    REDIS_READ_COUNT = 10_000

    # XADD. Returns the ID of the entry (a String), or nil with nomkstream:
    # when the stream does not exist. The values of entry are kept as given.
    def xadd(key, entry, approximate: nil, maxlen: nil, minid: nil, nomkstream: nil, id: "*")
      raise ArgumentError, "can't supply both maxlen and minid" if maxlen && minid

      trim = { max_len: Integer(maxlen), approx: approximate ? true : false } if maxlen
      trim = { min_id: minid.to_s, approx: approximate ? true : false } if minid

      doc = entry.to_h.transform_keys(&:to_s)
      entry_id = id.to_s == "*" ? nil : id.to_s

      stream(key).append(doc, id: entry_id, trim: trim, no_create: nomkstream ? true : false)
    rescue UnknownCollection
      raise unless nomkstream

      nil
    end

    # XTRIM. Returns how many entries it deleted.
    def xtrim(key, len_or_id, strategy: "MAXLEN", approximate: false, limit: nil)
      options = { approx: approximate ? true : false, limit: limit }

      case strategy.to_s.upcase
      when "MAXLEN" then options[:max_len] = Integer(len_or_id)
      when "MINID" then options[:min_id] = len_or_id.to_s
      else raise ArgumentError, "strategy must be MAXLEN or MINID, not #{strategy.inspect}"
      end

      stream(key).trim(**options)
    rescue UnknownCollection
      0
    end

    # XDEL. Returns how many entries it deleted.
    def xdel(key, *ids)
      ids.flatten.sum { |id| stream(key).delete(id) }
    rescue UnknownCollection
      0
    end

    # XRANGE. Returns [[id, { "field" => "value" }], ...] in ID order. An ID
    # with "(" in front is not included, as in Redis.
    def xrange(key, start = "-", range_end = "+", count: nil)
      redis_range(key, start, range_end, count, 1)
    end

    # XREVRANGE: xrange from range_end down to start.
    def xrevrange(key, range_end = "+", start = "-", count: nil)
      redis_range(key, start, range_end, count, -1)
    end

    # XLEN. 0 for a stream that does not exist.
    def xlen(key)
      stream(key).info.fetch("length")
    rescue UnknownCollection
      0
    end

    # XREAD. keys and ids are one value or an Array of each. Returns
    # { "stream" => [[id, fields], ...] } with only the streams that have
    # entries ({} when there is none). block: is in milliseconds (0: no
    # limit) and is for one key only.
    def xread(keys, ids, count: nil, block: nil)
      pairs = redis_stream_pairs(keys, ids, block)
      out = {}

      pairs.each do |key, id|
        entries = redis_block(block) { |seconds| redis_read(key, id.to_s, count || REDIS_READ_COUNT, seconds) }

        out[key.to_s] = redis_entries(entries) unless entries.empty?
      end

      out
    end

    # XGROUP. subcommand is create, setid, destroy, createconsumer or
    # delconsumer. Returns "OK" for create and setid, and a count for the
    # others. create without mkstream: needs a stream that exists.
    def xgroup(subcommand, key, group, id_or_consumer = nil, mkstream: false)
      target = stream(key).group(group)

      case subcommand.to_s.downcase
      when "create" then redis_group_create(key, group, target, id_or_consumer, mkstream)
      when "setid" then redis_group_setid(key, group, target, id_or_consumer)
      when "destroy" then redis_group_destroy(target)
      when "createconsumer"
        redis_group_errors(key, group) { target.create_consumer(redis_required(id_or_consumer, "consumer")) ? 1 : 0 }
      when "delconsumer"
        redis_group_errors(key, group) { target.delete_consumer(redis_required(id_or_consumer, "consumer")) }
      else
        raise ArgumentError, "unknown xgroup subcommand #{subcommand.inspect}"
      end
    end

    # XREADGROUP. An ID ">" gives new entries; any other ID gives the pending
    # entries of consumer after that ID (and the key is in the answer also
    # when it has none, as in Redis). A pending entry that was deleted from
    # the stream comes as [id, nil]. block: is in milliseconds (0: no limit),
    # is for one key only, and is not used for a pending read.
    def xreadgroup(group, consumer, keys, ids, count: nil, block: nil, noack: nil)
      pairs = redis_stream_pairs(keys, ids, block)
      out = {}

      pairs.each do |key, id|
        target = stream(key).group(group)
        pending = id.to_s != ">"

        entries = redis_group_errors(key, group) do
          if pending
            target.read(consumer, count: count || REDIS_READ_COUNT, pending: true, after: id.to_s)
          else
            redis_block(block) { |seconds| target.read(consumer, count: count || REDIS_READ_COUNT, block: seconds, no_ack: noack ? true : false) }
          end
        end

        out[key.to_s] = redis_entries(entries) if pending || !entries.empty?
      end

      out
    end

    # XACK. Returns how many entries were pending.
    def xack(key, group, *ids)
      stream(key).group(group).ack(*ids)
    rescue UnknownGroup, UnknownCollection
      0
    end

    # XCLAIM. min_idle_time and idle: are in milliseconds. Returns
    # [[id, fields], ...], or the IDs with justid: true. Options: idle:,
    # retrycount:, force:, justid:. time: is not supported.
    def xclaim(key, group, consumer, min_idle_time, *ids, **opts)
      redis_claim_options(opts)

      claimed = redis_group_errors(key, group) do
        stream(key).group(group).claim(
          consumer,
          *ids,
          min_idle: Integer(min_idle_time) / 1000.0,
          idle: opts[:idle] && (opts[:idle].to_i / 1000.0),
          retry_count: opts[:retrycount],
          force: opts[:force] ? true : false,
          just_ids: opts[:justid] ? true : false
        )
      end

      opts[:justid] ? claimed.entries : redis_entries(claimed.entries)
    end

    # XAUTOCLAIM. min_idle_time is in milliseconds. Returns
    # { "next" => cursor, "entries" => [[id, fields], ...] } ("entries" are
    # IDs with justid: true). "next" is "0-0" when the scan is complete.
    def xautoclaim(key, group, consumer, min_idle_time, start, count: nil, justid: false)
      claimed = redis_group_errors(key, group) do
        stream(key).group(group).auto_claim(
          consumer,
          min_idle: Integer(min_idle_time) / 1000.0,
          cursor: start.to_s,
          count: count && Integer(count),
          just_ids: justid ? true : false
        )
      end

      { "next" => claimed.cursor, "entries" => justid ? claimed.entries : redis_entries(claimed.entries) }
    end

    # XPENDING. xpending(key, group) returns the summary: { "size",
    # "min_entry_id", "max_entry_id", "consumers" => { name => "count" } }.
    # xpending(key, group, start, end, count[, consumer]) returns one Hash
    # for each entry: { "entry_id", "consumer", "elapsed" (ms), "count" }.
    # idle: (milliseconds) keeps only the entries idle for at least this long.
    def xpending(key, group, *args, idle: nil)
      unless [0, 3, 4].include?(args.size)
        raise ArgumentError, "wrong number of arguments (given #{args.size + 2}, expected 2, 5 or 6)"
      end

      target = stream(key).group(group)

      redis_group_errors(key, group) do
        next redis_pending_summary(target.pending) if args.empty?

        start, stop, count, consumer = args
        seconds = idle && (Integer(idle) / 1000.0)

        rows = target.pending_list(start: start.to_s, stop: stop.to_s, count: Integer(count), consumer: consumer, idle: seconds)

        rows.map { |row| redis_pending_row(row) }
      end
    end

    # XINFO. subcommand is stream, groups or consumers (with group). Returns
    # a Hash (stream) or an Array of Hash, with the key names of Redis.
    def xinfo(subcommand, key, group = nil)
      case subcommand.to_s.downcase
      when "stream" then redis_no_key(key) { redis_stream_info(key) }
      when "groups" then redis_no_key(key) { stream(key).groups.map { |row| redis_group_info(row) } }
      when "consumers"
        target = stream(key).group(redis_required(group, "group"))

        redis_group_errors(key, group) { target.consumers.map { |row| redis_consumer_info(row) } }
      else
        raise ArgumentError, "unknown xinfo subcommand #{subcommand.inspect}"
      end
    end

    private

    # The fields of an entry as Redis gives them: every value is a String.
    def redis_fields(doc)
      doc.to_h { |field, value| [field.to_s, redis_value(value)] }
    end

    def redis_value(value)
      case value
      when String then value
      when Hash, Array then JSON.generate(value)
      else value.to_s
      end
    end

    # [[id, fields], ...] from Ndb::Entry values. A deleted entry has nil
    # fields.
    def redis_entries(entries)
      entries.map { |entry| [entry.id, entry.deleted? ? nil : redis_fields(entry.doc)] }
    end

    def redis_range(key, start, stop, count, order)
      low = redis_bound(start, "$gte", "$gt")
      high = redis_bound(stop, "$lte", "$lt")

      body = { query: { _id: low.merge(high) }, sort: { _id: order } }
      body[:limit] = Integer(count) if count

      redis_entries(stream(key).entries("_search", body))
    rescue UnknownCollection
      []
    end

    # One side of a range: "(id" leaves the ID out.
    def redis_bound(id, inclusive, exclusive)
      id = id.to_s

      return { exclusive => id[1..] } if id.start_with?("(")

      { inclusive => id }
    end

    # The [key, id] pairs of xread and xreadgroup.
    def redis_stream_pairs(keys, ids, block)
      keys = Array(keys)
      ids = Array(ids)

      if keys.size != ids.size
        raise ArgumentError, "Unbalanced list of streams: for each stream key an ID must be specified"
      end

      if block && keys.size > 1
        raise NotSupported, "ndb reads one stream for each request: block: with #{keys.size} streams is not supported"
      end

      keys.zip(ids)
    end

    # Runs the read with its wait in seconds. block is in milliseconds:
    # nil does not wait, 0 waits until there is an entry.
    def redis_block(block)
      return yield(nil) if block.nil?

      ms = block.to_i

      return yield(ms / 1000.0) if ms.positive?

      loop do
        entries = yield(FOREVER_STEP)

        return entries unless entries.empty?
      end
    end

    # One xread of one stream. A stream that does not exist has no entries;
    # with a wait, the read looks for the stream until the time ends.
    def redis_read(key, after, count, seconds)
      target = stream(key)

      if seconds.nil?
        return after == "$" ? [] : redis_missing_empty { target.read(after: after, count: count) }
      end

      deadline = redis_now + seconds

      loop do
        left = deadline - redis_now

        return [] unless left.positive?

        begin
          return target.read(after: after, count: count, block: left)
        rescue UnknownCollection
          after = Stream::START_ALL if after == "$"

          sleep [left, MISSING_STEP].min
        end
      end
    end

    def redis_missing_empty
      yield
    rescue UnknownCollection
      []
    end

    def redis_now = Process.clock_gettime(Process::CLOCK_MONOTONIC)

    def redis_group_create(key, group, target, id, mkstream)
      redis_group_errors(key, group, missing_key: !mkstream) do
        target.create(redis_required(id, "id").to_s, no_create: !mkstream)
      end

      "OK"
    end

    def redis_group_setid(key, group, target, id)
      redis_group_errors(key, group) { target.set_id(redis_required(id, "id").to_s) }

      "OK"
    end

    def redis_group_destroy(target)
      target.destroy

      1
    rescue UnknownGroup, UnknownCollection
      0
    end

    def redis_required(value, name)
      raise ArgumentError, "#{name} is required for this subcommand" if value.nil?

      value
    end

    def redis_claim_options(opts)
      unknown = opts.keys - %i[idle time retrycount force justid]

      raise ArgumentError, "unknown xclaim options: #{unknown.join(", ")}" unless unknown.empty?

      raise NotSupported, "xclaim time: is not supported by ndb; use idle: (milliseconds)" if opts[:time]
    end

    def redis_pending_summary(row)
      consumers = (row["consumers"] || []).to_h { |c| [c["name"], c["count"].to_s] }

      {
        "size" => row["count"].to_i,
        "min_entry_id" => redis_presence(row["lowest"]),
        "max_entry_id" => redis_presence(row["highest"]),
        "consumers" => consumers
      }
    end

    def redis_pending_row(row)
      {
        "entry_id" => row["_id"],
        "consumer" => row["consumer"],
        "elapsed" => row["idle_ms"],
        "count" => row["deliveries"]
      }
    end

    def redis_stream_info(key)
      target = stream(key)
      info = target.info

      {
        "length" => info["length"],
        "last-generated-id" => redis_id(info["last_id"]),
        "max-deleted-entry-id" => redis_id(info["max_deleted_id"]),
        "entries-added" => info["entries_added"],
        "recorded-first-entry-id" => redis_id(info["first_id"]),
        "groups" => info["groups"],
        "first-entry" => redis_raw_entry(target.range("-", "+", count: 1).first),
        "last-entry" => redis_raw_entry(target.rev_range("+", "-", count: 1).first)
      }
    end

    # An entry as redis-rb leaves it in xinfo: [id, [field, value, ...]].
    def redis_raw_entry(entry)
      return nil if entry.nil?

      [entry.id, redis_fields(entry.doc).to_a.flatten(1)]
    end

    def redis_group_info(row)
      {
        "name" => row["name"],
        "consumers" => row["consumers"],
        "pending" => row["pending"],
        "last-delivered-id" => redis_id(row["last_delivered_id"]),
        "entries-read" => row["entries_read"],
        "lag" => row["lag"]
      }
    end

    def redis_consumer_info(row)
      {
        "name" => row["name"],
        "pending" => row["pending"],
        "idle" => row["idle_ms"],
        "inactive" => row["inactive_ms"]
      }
    end

    def redis_presence(value)
      value.to_s.empty? ? nil : value
    end

    # An ID that ndb does not have yet is "0-0" in Redis.
    def redis_id(value)
      redis_presence(value) || "0-0"
    end

    # Gives the errors of a group command the message that Redis gives. The
    # class stays the Ndb::Error subclass.
    def redis_group_errors(key, group, missing_key: false)
      yield
    rescue GroupExists => e
      raise redis_error(e, "BUSYGROUP Consumer Group name already exists")
    rescue UnknownCollection => e
      raise redis_error(e, redis_no_key_message) if missing_key

      raise redis_error(e, "NOGROUP No such key '#{key}' or consumer group '#{group}'")
    rescue UnknownGroup => e
      raise redis_error(e, "NOGROUP No such key '#{key}' or consumer group '#{group}'")
    end

    def redis_no_key(_key)
      yield
    rescue UnknownCollection => e
      raise redis_error(e, "ERR no such key")
    end

    def redis_no_key_message
      "ERR The XGROUP subcommand requires the key to exist. " \
        "Note that for CREATE you may want to use the MKSTREAM option to create an empty stream automatically."
    end

    def redis_error(error, message)
      error.class.new("#{message} (#{error.message})", code: error.code, status: error.status)
    end
  end
end

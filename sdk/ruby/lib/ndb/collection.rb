# frozen_string_literal: true

module Ndb
  # One collection of the daemon, with the query builder. The first call
  # declares it (PUT /{coll}) with its definition. A name with a "*" at the
  # end is a collection pattern ("logs-2026-*"): it reads every collection
  # whose name starts with the prefix, declares nothing, and its writes raise
  # Ndb::Error with code pattern_read_only.
  #
  #   db = Ndb.connect("unix:///run/ndb/ndb.sock")
  #   calls = db.collection("calls", time: "ts", indexes: ["status"])
  #   calls.insert(status: "answered", ts: Time.now)
  #   calls.query(status: "answered").sort(ts: -1).limit(10).all
  class Collection
    attr_reader :client, :name, :definition

    def initialize(client, name, definition = {})
      @client = client
      @name = name
      @definition = definition
      @ensured = false
    end

    def pattern? = name.include?("*")

    # A Hash is one document (_doc) and returns {"_id", "_v"}; an Array is
    # many in one transaction (_bulk) and returns the results. expire_in
    # applies to every document.
    def insert(doc_or_docs = nil, expire_in: nil, **fields)
      doc_or_docs ||= fields
      ready!

      return client.bulk(name, doc_or_docs, expire_in: expire_in) if doc_or_docs.is_a?(Array)
      return client.bulk(name, [doc_or_docs], expire_in: expire_in).first if expire_in

      client.insert(name, doc_or_docs)
    end

    # Writes a document by its _id: creates it when the _id is free, and
    # merges its fields into the stored document when it exists (the other
    # fields stay), like doc_as_upsert in Elasticsearch. No _id is an insert.
    # Returns {"_id", "_v"}.
    def upsert(doc = nil, expire_in: nil, **fields)
      upsert_all([doc || fields], expire_in: expire_in).first
    end

    # upsert for many documents in one transaction, all or nothing.
    def upsert_all(docs, expire_in: nil)
      ready!
      client.bulk(name, docs, expire_in: expire_in, upsert: true)
    end

    # The document with this _id, or nil.
    def get(id)
      ready!
      client.get(name, id)
    end

    # Applies the write operators to the document with this _id (atomic).
    # guard adds conditions on the same document, such as { _v: 7 }.
    def update(id, ops, guard: nil, upsert: false, expire_in: nil, lock: nil)
      ready!
      client.update(name, id, ops, guard: guard, upsert: upsert, expire_in: expire_in, lock: lock)
    end

    # The optimistic lock with the retry done for you. Reads the document
    # with this _id, yields it, and writes the fields the block changed (set,
    # or deleted), guarded by the version it read. When another write came
    # first, it reads the document again and yields again, up to retries
    # times; then it raises StaleObjectError. So the block can run more than
    # once: when it has an effect outside ndb, use lock. An exception in the
    # block stops it and nothing is written. A missing document raises
    # NotFound. Returns the document as written.
    #
    #   wallets.with_retry("w1") do |w|
    #     raise InsufficientFunds if w["balance_cents"] < 500
    #
    #     w["balance_cents"] -= 500
    #   end
    def with_retry(id, retries: 5, &block)
      retries = [retries, 1].max

      retries.times do |attempt|
        sleep((attempt * 5 + rand(10)) / 1000.0) if attempt.positive?

        doc = get(id) or raise NotFound.new("#{name} #{id}", code: "not_found", status: 404)
        written = change(id, doc, guard: { _v: doc["_v"] }, &block)

        return written if written
      end

      raise StaleObjectError.new("#{name} #{id} changed on every try", code: "stale_object", status: 409)
    end

    # The pessimistic lock. Takes the lock of the document, yields it, writes
    # the fields the block changed, and releases the lock. While the block
    # runs, no other caller holds the lock and no other write changes the
    # document: another lock waits (wait, then Locked) and a plain write
    # waits for the release. So the block runs once: use it when the block
    # has an effect outside ndb (a payment, an email). An exception in the
    # block writes nothing and releases the lock. A lock held for longer
    # than ttl is gone: the write raises LockExpired. See Client#lock.
    #
    #   payments.lock("p1") do |p|
    #     next if p["status"] == "refunded"
    #
    #     Gateway.refund(p["charge_id"])
    #     p["status"] = "refunded"
    #   end
    def lock(id, ttl: nil, wait: nil, &block)
      ready!
      held = client.lock(name, id, ttl: ttl, wait: wait)

      begin
        change(id, held["doc"], lock: held["token"], &block) or raise NotFound.new("#{name} #{id}", code: "not_found", status: 404)
      ensure
        release(id, held["token"])
      end
    end

    # A transaction of this collection. With lock: (ids), it first locks
    # those documents, all of them or none, and yields them by _id, read
    # under the lock. The block records the writes with tx.insert, tx.update
    # and tx.delete; they are written when the block ends, all of them or
    # none, and the locks are released.
    #
    # An exception in the block writes nothing and goes on; Ndb::Rollback
    # writes nothing and the call returns nil. A refused commit writes
    # nothing: TxConflict when an update or a delete matches nothing (a
    # missing document, or a stale "_v"), LockExpired when the locks
    # outlived ttl (default 30 s: the time limit of the transaction). wait is
    # how long to wait for a document that another caller holds (then
    # Locked). Reads inside the block see the stored data, not its writes.
    #
    #   accounts.transaction(lock: %w[a1 a2], ttl: 5) do |tx, docs|
    #     raise Ndb::Rollback if docs["a1"]["balance"] < amount
    #
    #     docs["a1"]["balance"] -= amount
    #     docs["a2"]["balance"] += amount
    #
    #     tx.update(docs["a1"])
    #     tx.update(docs["a2"])
    #   end
    def transaction(lock: [], ttl: nil, wait: nil)
      ready!

      tx = Transaction.new
      docs = {}
      tokens = []

      unless lock.empty?
        held = client.lock_set(name, lock, ttl: ttl, wait: wait)
        tokens << held["token"]

        held["docs"].each do |doc|
          docs[doc["_id"]] = doc
          tx.locked(doc)
        end
      end

      begin
        yield tx, docs
      rescue Rollback
        release_set(tokens)

        return nil
      rescue Exception # rubocop:disable Lint/RescueException
        release_set(tokens)

        raise
      end

      if tx.ops.empty?
        release_set(tokens)

        return nil
      end

      client.tx(name, tx.ops, locks: tokens, unlock: true)
    end

    # The atomic update by query: the first match, or every match with many.
    def update_by(query, ops, many: false, upsert: false, expire_in: nil)
      ready!
      client.update_by(name, query, ops, many: many, upsert: upsert, expire_in: expire_in)
    end

    def delete(id)
      ready!
      client.delete(name, id)
    end

    def delete_by(query = nil, many: false, **conditions)
      query ||= conditions
      ready!
      client.delete_by(name, query, many: many)
    end

    # A search; the conditions use the Ruby forms of Ndb::Conditions
    # (Ranges, Times, Regexps, an Array as $in).
    def query(conditions = nil, **keywords)
      Query.new(self).query(conditions || keywords)
    end

    # A query built elsewhere, such as from a search form, sent as is.
    def raw_query(hash)
      Query.new(self).raw_query(hash)
    end

    # How many documents match.
    def count(conditions = nil, **keywords)
      query(conditions || keywords).count
    end

    # A whole search body built elsewhere: { "query", "sort", "limit",
    # "after", "count" }. Returns an Ndb::Page.
    def search(body)
      ready!
      client.search(name, body)
    end

    def aggregate(pipeline, **opts)
      ready!
      client.aggregate(name, { pipeline: pipeline }.merge(opts))
    end

    # Each event Hash also has "consumer", so ack(event) needs nothing else.
    def watch(consumer, query: nil, from: nil, limit: nil, &block)
      return enum_for(:watch, consumer, query: query, from: from, limit: limit) unless block

      ready!
      client.watch(name, consumer, query: query, from: from, limit: limit) do |event|
        block.call(event.merge("consumer" => consumer))
      end
    end

    # Stores the position of an event from watch for its consumer: the next
    # watch with that consumer name starts after it.
    def ack(event, consumer = nil)
      client.ack(name, consumer || event.fetch("consumer"), event.fetch("pos"))
    end

    def expire(id, **time)
      ready!
      client.expire(name, id, **time)
    end

    def persist(id)
      ready!
      client.persist(name, id)
    end

    def ttl(id)
      ready!
      client.ttl(name, id)
    end

    def truncate = client.truncate(name)

    def drop
      @ensured = false
      client.drop(name)
    end

    # Renames the collection on the daemon; this object keeps the old name.
    def rename(to, replace: false) = client.rename(name, to, replace: replace)

    # A copy of the collection as it was at +at+, in the collection +to+.
    def restore(at:, to:) = client.restore(name, at: at, to: to)

    def pitr = client.pitr(name)

    def stats
      ready!
      client.collection_stats(name)
    end

    def compact
      ready!
      client.compact(name)
    end

    def ensure_index(field)
      ready!
      client.ensure_index(name, field)
    end

    def drop_index(field)
      ready!
      client.drop_index(name, field)
    end

    # Declares the collection once (not a pattern).
    def ready!
      return if @ensured || pattern?

      client.ensure_collection(name, definition)
      @ensured = true
    end

    private

    # Yields doc and writes the top-level fields the block changed, in one
    # update with options (a guard, or the token of a lock). Returns the
    # document as written (doc when nothing changed), or nil when the update
    # matched nothing.
    def change(id, doc, **options)
      before = Marshal.load(Marshal.dump(doc))

      yield doc

      ops = lock_changes(before, doc)
      return doc if ops.empty?

      result = update(id, ops, **options)

      result["doc"] if result["matched"].positive?
    end

    # A lock that cannot be released ends with its ttl.
    def release(id, token)
      client.unlock(name, id, token)
    rescue Error
      nil
    end

    def release_set(tokens)
      tokens.each do |token|
        client.unlock_set(name, token)
      rescue Error
        nil
      end
    end

    # The $set and $unset of the top-level fields that differ.
    def lock_changes(before, after) = Transaction.changes(before, after)
  end

  # An immutable search on a collection: every step returns a new Query.
  class Query
    include Enumerable

    def initialize(collection, clauses: [], options: {})
      @collection = collection
      @clauses = clauses
      @options = options
    end

    # Adds conditions (Ruby forms), ANDed with the ones already there.
    def query(conditions)
      return self if conditions.empty?

      with(clauses: @clauses + [Conditions.equality(conditions)])
    end

    # Adds a query built elsewhere, sent as is.
    def raw_query(hash)
      with(clauses: @clauses + [Conditions.json_value(hash)])
    end

    def sort(spec) = with(sort: spec.to_h { |k, v| [k.to_s, direction(v)] })
    def limit(count) = with(limit: count)
    def skip(count) = with(skip: count)
    def after(token) = with(after: token)
    def fields(*names) = with(fields: names.flatten.map(&:to_s))

    # Adds relations of other collections to each row ("lookup"):
    #
    #   orders.query(status: "paid").lookup(
    #     customer: { belongs_to: "customers", fk: "customer_id" },
    #     items: { has_many: "order_items", fk: "order_id", sort: { pos: 1 }, limit: 50 }
    #   )
    def lookup(relations) = with(lookup: (@options[:lookup] || {}).merge(Ndb.deep_stringify(relations)))

    # The rows of one request: one page with limit, every row without it.
    def all = page.rows

    def first = limit(1).all.first

    # One page: rows, next cursor, total (with count: true via #page(count: true)).
    def page(count: false)
      body = body_hash
      body["count"] = true if count

      @collection.search(body)
    end

    # How many documents match (POST /{coll}/_count).
    def count
      @collection.ready!
      @collection.client.count(@collection.name, where)
    end

    # Every row, following the cursor page by page (limit is the page size).
    def each(&block)
      return enum_for(:each) unless block

      query = self

      loop do
        result = query.page
        result.rows.each(&block)

        break unless result.next

        query = query.after(result.next).with(skip: nil)
      end
    end

    def body_hash
      body = { "query" => where }.merge(@options.compact.transform_keys(&:to_s))
      body.delete("query") if body["query"].empty?
      body
    end

    protected

    def with(**options)
      clauses = options.delete(:clauses) || @clauses

      self.class.new(@collection, clauses: clauses, options: @options.merge(options))
    end

    private

    def where = Conditions.combine(@clauses)

    def direction(value)
      %w[desc -1].include?(value.to_s.downcase) ? -1 : 1
    end
  end

  # A client for addr: unix:///path/to/ndb.sock or tcp://host:port. Without
  # addr it reads NDB_URL. See Ndb::Client for the options.
  # deep_stringify gives value with every Hash key as a String (Symbols
  # come from Ruby code, the daemon reads JSON).
  def self.deep_stringify(value)
    case value
    when Hash then value.to_h { |k, v| [k.to_s, deep_stringify(v)] }
    when Array then value.map { |v| deep_stringify(v) }
    when Symbol then value.to_s
    else value
    end
  end

  def self.connect(addr = nil, **opts)
    Client.new(url: addr, **opts)
  end
end

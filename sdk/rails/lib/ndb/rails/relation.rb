# frozen_string_literal: true

module Ndb
  # A lazy, immutable query. Every builder returns a new relation; the
  # request goes to the daemon on each, to_a, first, count, pluck, ...
  class Relation
    include Enumerable
    include DynamicFinders

    DEFAULT_PER_PAGE = 25
    BATCH_SIZE = 1000
    EMPTY = { where: [].freeze, order: {}.freeze, group: [].freeze }.freeze
    PRELOADED = Data.define(:rows, :next, :total).new(rows: [].freeze, next: nil, total: nil)
    NONE = PRELOADED.with(total: 0)

    attr_reader :klass, :values

    def initialize(klass, values = EMPTY)
      @klass = klass
      @values = values
      @result = nil
    end

    # Builders

    def where(attrs = :chain)
      return WhereChain.new(self) if attrs == :chain
      raise ArgumentError, "where takes a Hash, got #{attrs.class}" unless attrs.is_a?(Hash)

      add_clause(Conditions.equality(alias_id(attrs)))
    end

    # A where Hash built elsewhere, such as from a search form, sent as is:
    # no Rails conventions (an Array stays an Array, not $in). The daemon
    # validates it. ANDed with the other clauses.
    def where_raw(where)
      raise ArgumentError, "where_raw takes a Hash, got #{where.class}" unless where.is_a?(Hash)

      add_clause(Conditions.json_value(where))
    end

    def or(other)
      clauses = [Conditions.combine(values[:where]), Conditions.combine(other.values[:where])]

      with(where: [{ "$or" => clauses }])
    end

    # order(:n), order(n: :desc) or the Active Record String forms "n",
    # "n DESC" and "n asc, m desc". Any other String (a SQL expression)
    # raises ArgumentError.
    def order(*args)
      sort = args.each_with_object(values[:order].dup) do |arg, out|
        case arg
        when Hash then arg.each { |f, dir| out[f.to_s] = direction(dir) }
        when String then out.merge!(parse_order(arg))
        else out[arg.to_s] = 1
        end
      end

      with(order: sort)
    end

    def limit(count)
      with(limit: count)
    end

    # Makes find, find_by and first take the pessimistic lock of the record
    # they return, like ActiveRecord lock: `Account.lock.find(id)`. With a
    # block, the record is yielded and the lock ends with the block; without
    # one, it ends at the first save or destroy, or at unlock!. See
    # Ndb::Locking. ttl and wait: see Ndb::Client#lock.
    # Reads that keep the encrypted fields sealed ("ndb:e1:k1:..."): a list
    # of secrets that shows none of them. The other reads get the values.
    #
    #   Secret.encrypted.where(owner: user.id)
    def encrypted
      with(keep_encrypted: true)
    end

    def lock(ttl: nil, wait: nil)
      with(lock: { ttl: ttl, wait: wait }.compact)
    end

    # Loads associations with the rows, in the same call (the daemon
    # lookup): includes(:customer, items: :product).
    def includes(*names)
      with(includes: Array(values[:includes]) + names)
    end

    def offset(count)
      with(offset: count)
    end

    def page(number)
      with(page: number.to_i)
    end

    def per(size)
      with(per: size.to_i)
    end

    def after(token)
      with(after: token)
    end

    def group(*fields)
      with(group: fields.flatten.map(&:to_s))
    end

    # Makes the aggregates of the relation (count, sum, average, minimum,
    # maximum, with or without group) read one row per element of an array
    # attribute, as $unwind: `Order.unwind(:items).group("items.sku")
    # .sum("items.qty")`. A document with no element gives no row. It is for
    # the aggregates only: reading the records of such a relation raises
    # ArgumentError.
    def unwind(field)
      with(unwind: field.to_s)
    end

    # The order replaced, not added to: order(:a).reorder(b: :desc).
    def reorder(*args)
      with(order: {}).order(*args)
    end

    # The where of these attributes replaced: where(n: 1).rewhere(n: 2).
    # Only the equality clauses of these fields are removed.
    def rewhere(attrs)
      keys = alias_id(attrs).keys.map(&:to_s)
      kept = values[:where].map { |clause| clause.except(*keys) }.reject(&:empty?)

      with(where: kept).where(attrs)
    end

    UNSCOPE = {
      where: { where: [].freeze }, order: { order: {}.freeze }, limit: { limit: nil }, offset: { offset: nil },
      page: { page: nil, per: nil }, after: { after: nil }, group: { group: [].freeze }, includes: { includes: nil },
      select: { select: nil }, distinct: { distinct: nil }
    }.freeze

    # The relation without these parts: unscope(:where, :order, :limit...).
    def unscope(*types)
      changes = types.reduce({}) do |out, type|
        out.merge(UNSCOPE.fetch(type.to_sym) { raise ArgumentError, "unscope takes #{UNSCOPE.keys.join(", ")}, got #{type.inspect}" })
      end

      with(changes)
    end

    # Makes pluck give each value (or row of values) once.
    def distinct(value = true)
      with(distinct: value)
    end

    # select(:a, :b): the records with only these attributes loaded (the
    # daemon projection; the primary key is always read). With a block, it
    # is Enumerable#select.
    def select(*fields, &block)
      return super(&block) if block

      raise ArgumentError, "select needs at least one field" if fields.empty?

      with(select: fields.map(&:to_s))
    end

    # An empty relation: it sends no request. to_a is [], count is 0, and
    # update and delete_all write nothing.
    def none
      with(none: true)
    end

    # The JSON body that this relation sends to the daemon, as a String,
    # with no request (like to_sql of ActiveRecord). It is the body of
    # POST /{collection}/_search; pretty: true gives it on many lines.
    #
    #   Call.where(status: "failed").order(created_at: :desc).limit(10).to_ndb
    #   # => {"query":{"status":"failed"},"sort":{"created_at":-1},"limit":10,"versions":true}
    def to_ndb(pretty: false)
      body = to_query.merge("versions" => true)

      pretty ? JSON.pretty_generate(body) : JSON.generate(body)
    end

    # The find body: JSON only, the daemon decides what it means.
    def to_query
      raise ArgumentError, "unwind is for count, sum, average, minimum and maximum; it does not change the records of a relation" if values[:unwind]

      query = {}
      where = Conditions.combine(values[:where])

      query["query"] = where unless where.empty?
      query["sort"] = values[:order] unless values[:order].empty?
      query.merge!(window)
      query["after"] = values[:after] if values[:after]
      query["lookup"] = klass.lookup_for(values[:includes]) if values[:includes]&.any?
      query["fields"] = ([klass.primary_key] + values[:select]).uniq if values[:select]

      query
    end

    # Atomic writes. The daemon matches the where and writes in one step on
    # every match, so the where needs the id or an indexed field
    # (Ndb::UnindexedMany otherwise). Returns the number of modified
    # documents. It takes attributes, as Active Record (update(status: "x")
    # is a $set, cast by the attribute types), the operators of
    # Ndb::Client#update ("$inc" => { "n" => 1 }), or both: the attributes
    # join the $set. Unlike Active Record, it runs no validation and no
    # callback (as update_all).
    def update(ops = nil, expire_in: nil, **ops_kw)
      ops = (ops || {}).merge(ops_kw).transform_keys(&:to_s)
      attrs, ops = ops.partition { |key, _| !key.start_with?("$") }.map(&:to_h)

      raise ArgumentError, "update takes attributes (status: \"x\") or operators (\"$inc\" => { n: 1 })" if attrs.empty? && ops.empty?

      ops = with_attributes(ops, attrs) unless attrs.empty?

      where = write_where

      return 0 unless where

      klass.ndb_update(where, with_updated_at(ops), many: true, expire_in: expire_in).fetch("modified")
    end

    # One daemon delete on every match, no callbacks (same rule as update).
    # Returns how many. An unfiltered relation is refused by the daemon
    # (Ndb::EmptyFilter). A limit, offset, page or cursor narrows it to
    # those rows, as for update.
    def delete_all
      where = write_where

      return 0 unless where

      klass.ndb_delete(where, many: true).fetch("deleted")
    end

    alias delete delete_all
    alias update_all update

    # A new record with the equality attributes of the where, as the
    # scoping of Active Record: Call.where(status: "new").new(caller: "a").
    def new(attrs = {}, &block)
      klass.new(scope_attributes.merge(attrs.transform_keys(&:to_s))).tap { |record| block&.call(record) }
    end

    alias build new

    def create(attrs = {}, &)
      new(attrs, &).tap(&:save)
    end

    def create!(attrs = {}, &)
      new(attrs, &).tap(&:save!)
    end

    def find_or_initialize_by(attrs, &)
      klass.find_or_initialize_by(scope_attributes.merge(attrs.transform_keys(&:to_s)), &)
    end

    def find_or_create_by(attrs, &)
      klass.find_or_create_by(scope_attributes.merge(attrs.transform_keys(&:to_s)), &)
    end

    def find_or_create_by!(attrs, &)
      klass.find_or_create_by!(scope_attributes.merge(attrs.transform_keys(&:to_s)), &)
    end

    def destroy_by(attrs)
      where(attrs).destroy_all
    end

    def delete_by(attrs)
      where(attrs).delete_all
    end

    # Loads the matches and destroys each one, with its callbacks. Returns
    # the records.
    def destroy_all
      to_a.each(&:destroy)
    end

    # Loaders

    def load
      return self if loaded?

      @result = fetch(to_query)
      @records = @result.rows.map { |row| klass.instantiate(row) }

      self
    end

    def loaded?
      !@result.nil?
    end

    def records
      load

      @records
    end

    def to_a
      records.dup
    end

    def each(&)
      records.each(&)
    end

    def length
      records.length
    end

    # Like ActiveRecord: the number of loaded records when loaded, else a
    # count in the daemon.
    def size
      loaded? ? @records.length : count
    end

    def empty?
      size.zero?
    end

    # Holds records that were read already (an includes): reading the
    # relation does not query; a builder on it (where, order...) does.
    def preload(records)
      @records = records
      @result = PRELOADED

      self
    end

    def next_cursor
      load

      @result.next
    end

    # The first record (or count of them), in the order of the relation.
    # With no order, the order is the primary key (as ActiveRecord does),
    # so first and last are the two ends of one order. The daemon gives
    # only count rows (limit), not the whole relation.
    def first(count = nil, &block)
      return lock_found(with(lock: nil).first, &block) if values[:lock] && count.nil?
      return count ? records.first(count) : records.first if loaded?

      found = by_key.limit(count || 1).to_a

      count ? found : found.first
    end

    # The last record (or count of them, in the order of the relation). The
    # order is turned around and the daemon gives only count rows; a
    # relation with a limit, an offset or a page is loaded instead, because
    # its last row depends on that window.
    def last(count = nil)
      return count ? records.last(count) : records.last if loaded? || windowed?

      found = by_key.reverse_order.limit(count || 1).to_a.reverse

      count ? found : found.last
    end

    def first!
      first || raise(not_found)
    end

    def last!
      last || raise(not_found)
    end

    # A record (or count of them) in no given order: the order of the
    # relation, else the order of the daemon.
    def take(count = nil)
      return count ? records.first(count) : records.first if loaded?

      found = limit(count || 1).to_a

      count ? found : found.first
    end

    def take!
      take || raise(not_found)
    end

    # The one record of the relation: Ndb::NotFound when none,
    # Ndb::SoleRecordExceeded when more than one.
    def sole
      found = limit(2).to_a

      raise not_found if found.empty?
      raise SoleRecordExceeded, "Wanted only one #{klass.name}" if found.size > 1

      found.first
    end

    def find_sole_by(attrs)
      where(attrs).sole
    end

    # The relation with its order turned around (asc and desc swap). With no
    # order, it is the primary key in descending order.
    def reverse_order
      sort = by_key.values[:order].transform_values { |dir| -dir }

      with(order: sort)
    end

    def find(*ids, &block)
      ids = ids.flatten

      return lock_one(ids, &block) if values[:lock]
      return find_one(ids.first) if ids.size == 1 && !ids.first.is_a?(Array)

      found = where(klass.primary_key => ids).to_a
      raise NotFound.new("Couldn't find all #{klass.name} with ids #{ids}", code: "not_found") if found.size != ids.size

      found
    end

    def find_by(attrs, &block)
      where(attrs).first(&block)
    end

    def find_by!(attrs, &block)
      find_by(attrs, &block) || raise(NotFound.new("Couldn't find #{klass.name}", code: "not_found"))
    end

    # Like ActiveRecord: exists?, exists?("id") or exists?(status: "x").
    # false or nil is false, with no request.
    def exists?(conditions = :none)
      return false if conditions.nil? || conditions == false
      return where(conditions).exists? if conditions.is_a?(Hash)
      return where(klass.primary_key => conditions).exists? unless conditions == :none

      limit = [window["limit"], 1].compact.min

      return false if limit.zero?

      fetch(to_query.except("lookup").merge("limit" => limit, "fields" => [klass.primary_key])).rows.any?
    end

    # The primary keys of the relation, in its order (one request).
    def ids
      return records.map { |record| record.public_send(klass.primary_key) } if loaded?

      fetch(to_query.except("lookup").merge("fields" => [klass.primary_key])).rows.map { |row| row[klass.primary_key] }
    end

    def pluck(*fields)
      raise ArgumentError, "pluck needs at least one field" if fields.empty?

      names = fields.map(&:to_s)
      rows = fetch(to_query.merge("fields" => names)).rows

      plucked = rows.map do |row|
        values = names.map { |name| klass.attribute_types[name].cast(row.dig(*name.split("."))) }

        values.size == 1 ? values.first : values
      end

      values[:distinct] ? plucked.uniq : plucked
    end

    # pluck of the first row: a value, an Array of values, or nil.
    def pick(*fields)
      limit(1).pluck(*fields).first
    end

    def count
      return calculate(:count) unless values[:group].empty? && values[:unwind].nil?
      return ids.size if narrowed?

      fetch(to_query.slice("query").merge("count" => true, "limit" => 1, "fields" => [klass.primary_key])).total
    end

    # Pagination

    def find_each(batch_size: BATCH_SIZE, &block)
      return enum_for(:find_each, batch_size: batch_size) unless block

      in_batches(of: batch_size) { |batch| batch.each(&block) }
    end

    # Yields loaded relations, one per cursor page.
    def in_batches(of: BATCH_SIZE)
      return enum_for(:in_batches, of: of) unless block_given?

      cursor = values[:after]

      loop do
        batch = with(limit: of, after: cursor, page: nil, offset: nil).load

        break if batch.records.empty?

        yield batch

        cursor = batch.next_cursor

        break unless cursor
      end
    end

    # Aggregates

    def sum(field) = calculate(:sum, field)
    def average(field) = calculate(:avg, field)
    def minimum(field) = calculate(:min, field)
    def maximum(field) = calculate(:max, field)

    def aggregate_body(op, field = nil)
      stages = []
      where = Conditions.combine(values[:where])

      stages << { "$match" => where } unless where.empty?
      stages << { "$unwind" => "$#{values[:unwind]}" } if values[:unwind]
      stages << { "$group" => { "_id" => group_id, op.to_s => accumulator(op, field) } }

      { "pipeline" => stages }
    end

    # A scope or a class method of the model, called on this relation: it
    # runs with this relation as the model's all. Only the methods the model
    # declares; the methods of Ndb::Record (truncate!, upsert...) are not
    # run on a relation.
    def method_missing(name, ...)
      return super unless model_method?(name)

      klass.scoping(self) { klass.public_send(name, ...) }
    end

    def respond_to_missing?(name, include_private = false)
      model_method?(name) || super
    end

    protected

    def dynamic_finder_klass = klass
    def dynamic_finder_scope = self

    def with(changes)
      self.class.new(klass, values.merge(changes))
    end

    private

    def fetch(body)
      return NONE if values[:none]

      sealed_or_plain { klass.ndb_find(body) }
    end

    def not_found
      NotFound.new("Couldn't find #{klass.name}", code: "not_found")
    end

    # The fields of the equality clauses of the where (status: "x"), for
    # new and create. Operators ($in, $gte...) are not values.
    def scope_attributes
      values[:where].each_with_object({}) do |clause, out|
        clause.each { |key, value| out[key] = value unless key.start_with?("$") || value.is_a?(Hash) || value.is_a?(Array) }
      end
    end

    def sealed_or_plain(&)
      values[:keep_encrypted] ? Rails.client.keep_encrypted(&) : yield
    end

    # ops with attrs, cast by their types, in its $set. A field in both is
    # refused: it is not clear which value wins.
    def with_attributes(ops, attrs)
      set = (ops["$set"] || {}).transform_keys(&:to_s)
      doc = klass.send(:upsert_document, attrs)
      both = set.keys & doc.keys

      raise ArgumentError, "update sets #{both.join(", ")} twice: as an attribute and in $set" if both.any?

      ops.merge("$set" => set.merge(doc))
    end

    # The operators with $set of updated_at, unless they write it already.
    def with_updated_at(ops)
      ops = ops.transform_keys(&:to_s)

      return ops if ops.values.any? { |fields| fields.is_a?(Hash) && fields.keys.map(&:to_s).include?("updated_at") }

      set = (ops["$set"] || {}).merge("updated_at" => Conditions.json_value(klass.current_time))

      ops.merge("$set" => set)
    end

    def model_method?(name)
      klass.respond_to?(name) && !Record.respond_to?(name, true)
    end

    # id is an alias of _id, as on a record, unless the model has its own
    # id attribute.
    def alias_id(attrs)
      return attrs if klass.attribute_names.include?("id")

      attrs.transform_keys { |key| key.to_s == "id" ? klass.primary_key : key }
    end

    def add_clause(clause)
      with(where: values[:where] + [clause])
    end

    # The relation, ordered by the primary key when it has no order.
    def by_key
      return self unless values[:order].empty?

      with(order: { klass.primary_key => 1 })
    end

    def windowed?
      values[:limit] || values[:offset] || values[:page]
    end

    # A window or a cursor: the relation is fewer rows than its where.
    def narrowed?
      windowed? || values[:after]
    end

    # The filter of update and delete_all: the where, and the ids of the
    # rows when the relation is narrowed (the daemon writes every match of
    # a filter). nil when a narrowed relation has no row.
    def write_where
      return nil if values[:none]
      return Conditions.combine(values[:where]) unless narrowed?

      found = ids

      return nil if found.empty?

      Conditions.combine(values[:where] + [{ klass.primary_key => { "$in" => found } }])
    end

    ORDER_PART = /\A([\w.]+)(?:\s+(asc|desc))?\z/i

    def parse_order(text)
      text.split(",").to_h do |part|
        match = ORDER_PART.match(part.strip)

        raise ArgumentError, "order takes \"field\" or \"field asc|desc\", got #{text.inspect}" unless match

        [match[1], direction(match[2])]
      end
    end

    def direction(value)
      %w[desc -1].include?(value.to_s.downcase) ? -1 : 1
    end

    def window
      if values[:page]
        per = values[:per] || DEFAULT_PER_PAGE

        return { "skip" => (values[:page] - 1) * per, "limit" => per }
      end

      out = {}
      out["skip"] = values[:offset] if values[:offset]
      out["limit"] = values[:limit] if values[:limit]

      out
    end

    # find of a relation with lock: one id, locked by the daemon and read in
    # the same step.
    def lock_one(ids, &block)
      raise ArgumentError, "lock.find takes one id" unless ids.size == 1

      held = klass.ndb_lock(ids.first, **values[:lock])
      record = klass.instantiate_locked(held)

      block ? record.scoped_lock { yield record } : record
    rescue NotFound
      raise NotFound.new("Couldn't find #{klass.name} with #{klass.primary_key}=#{ids.first}", code: "not_found")
    end

    # A record that a query found, locked and read again (nil stays nil).
    def lock_found(record, &block)
      return nil unless record

      record.lock!(**values[:lock])

      block ? record.scoped_lock { yield record } : record
    end

    def find_one(id)
      where(klass.primary_key => id).first ||
        raise(NotFound.new("Couldn't find #{klass.name} with #{klass.primary_key}=#{id}", code: "not_found"))
    end

    def group_id
      fields = values[:group]

      return nil if fields.empty?
      return "$#{fields.first}" if fields.one?

      fields.to_h { |f| [f, "$#{f}"] }
    end

    def accumulator(op, field)
      op == :count ? { "$count" => {} } : { "$#{op}" => "$#{field}" }
    end

    def calculate(op, field = nil)
      key = op.to_s
      rows = values[:none] ? [] : sealed_or_plain { klass.ndb_aggregate(aggregate_body(op, field)) }.rows

      return rows.to_h { |row| [group_key(row["_id"]), row[key]] } unless values[:group].empty?

      rows.first&.fetch(key) || (%i[sum count].include?(op) ? 0 : nil)
    end

    def group_key(id)
      values[:group].one? ? id : values[:group].map { |f| id[f] }
    end

    # where.not(...)
    class WhereChain
      def initialize(relation)
        @relation = relation
      end

      def not(attrs)
        @relation.send(:add_clause, Conditions.negation(@relation.send(:alias_id, attrs)))
      end
    end
  end
end

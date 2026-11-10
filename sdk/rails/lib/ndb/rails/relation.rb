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

      add_clause(Conditions.equality(attrs))
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

    def order(*args)
      sort = args.each_with_object(values[:order].dup) do |arg, out|
        arg.is_a?(Hash) ? arg.each { |f, dir| out[f.to_s] = direction(dir) } : out[arg.to_s] = 1
      end

      with(order: sort)
    end

    def limit(count)
      with(limit: count)
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

    # The find body: JSON only, the daemon decides what it means.
    def to_query
      query = {}
      where = Conditions.combine(values[:where])

      query["query"] = where unless where.empty?
      query["sort"] = values[:order] unless values[:order].empty?
      query.merge!(window)
      query["after"] = values[:after] if values[:after]
      query["lookup"] = klass.lookup_for(values[:includes]) if values[:includes]&.any?

      query
    end

    # Atomic writes. The daemon matches the where and writes in one step on
    # every match, so the where needs the id or an indexed field
    # (Ndb::UnindexedMany otherwise). ops is the operator Hash of
    # Ndb::Client#update ({ "$inc" => { "n" => 1 } }); returns the number of
    # modified documents.
    def update(ops, expire_in: nil)
      klass.ndb_update(Conditions.combine(values[:where]), ops, many: true, expire_in: expire_in).fetch("modified")
    end

    # One daemon delete on every match, no callbacks (same rule as update).
    # Returns how many. An unfiltered relation is refused by the daemon
    # (Ndb::EmptyFilter).
    def delete_all
      klass.ndb_delete(Conditions.combine(values[:where]), many: true).fetch("deleted")
    end

    alias delete delete_all

    # Loads the matches and destroys each one, with its callbacks. Returns
    # the records.
    def destroy_all
      to_a.each(&:destroy)
    end

    # Loaders

    def load
      return self if loaded?

      @result = klass.ndb_find(to_query)
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
    def first(count = nil)
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

    # The relation with its order turned around (asc and desc swap). With no
    # order, it is the primary key in descending order.
    def reverse_order
      sort = by_key.values[:order].transform_values { |dir| -dir }

      with(order: sort)
    end

    def find(*ids)
      ids = ids.flatten

      return find_one(ids.first) if ids.size == 1 && !ids.first.is_a?(Array)

      found = where(klass.primary_key => ids).to_a
      raise NotFound.new("Couldn't find all #{klass.name} with ids #{ids}", code: "not_found") if found.size != ids.size

      found
    end

    def find_by(attrs)
      where(attrs).first
    end

    def find_by!(attrs)
      find_by(attrs) || raise(NotFound.new("Couldn't find #{klass.name}", code: "not_found"))
    end

    def exists?
      klass.ndb_find(to_query.slice("query").merge("limit" => 1, "fields" => [klass.primary_key])).rows.any?
    end

    def pluck(*fields)
      raise ArgumentError, "pluck needs at least one field" if fields.empty?

      names = fields.map(&:to_s)
      rows = klass.ndb_find(to_query.merge("fields" => names)).rows

      rows.map do |row|
        values = names.map { |name| klass.attribute_types[name].cast(row.dig(*name.split("."))) }

        values.size == 1 ? values.first : values
      end
    end

    def count
      return calculate(:count) unless values[:group].empty?

      klass.ndb_find(to_query.slice("query").merge("count" => true, "limit" => 1, "fields" => [klass.primary_key])).total
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
      stages << { "$group" => { "_id" => group_id, op.to_s => accumulator(op, field) } }

      { "pipeline" => stages }
    end

    protected

    def dynamic_finder_klass = klass
    def dynamic_finder_scope = self

    def with(changes)
      self.class.new(klass, values.merge(changes))
    end

    private

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
      rows = klass.ndb_aggregate(aggregate_body(op, field)).rows

      return rows.to_h { |row| [group_key(row["_id"]), row[key]] } unless values[:group].empty?

      rows.first&.fetch(key) || (op == :sum ? 0 : nil)
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
        @relation.send(:add_clause, Conditions.negation(attrs))
      end
    end
  end
end

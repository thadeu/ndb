# frozen_string_literal: true

module Ndb
  # belongs_to, has_many and has_one, read like ActiveRecord: includes loads
  # them with the rows in one call (the daemon lookup), and a record without
  # includes loads one on first use (one more call, like an N+1).
  #
  #   class Order < Ndb::Record
  #     belongs_to :customer                    # customer_id -> Customer._id
  #     has_many :items, sort: { pos: 1 }       # Item.order_id -> _id
  #     has_one :invoice                        # Invoice.order_id -> _id
  #   end
  #
  #   Order.where(status: "paid").includes(:customer, items: :product).to_a
  #
  # has_many and has_one index the foreign key of the other model by
  # themselves (the lookup needs it).
  module Associations
    extend ActiveSupport::Concern

    # One declared association.
    Reflection = Data.define(:name, :kind, :class_name, :foreign_key, :primary_key, :options) do
      def klass = class_name.constantize

      def many? = kind == :has_many

      # The relation for the lookup key of a search.
      def lookup(nested = {})
        relation = { kind.to_s => klass.collection_name, "fk" => foreign_key.to_s }

        relation["pk"] = primary_key.to_s if primary_key.to_s != "_id"

        options.each { |k, v| relation[k.to_s] = Ndb.deep_stringify(v) }

        relation["lookup"] = klass.lookup_for(nested) if nested.any?
        relation
      end
    end

    class_methods do
      # The keys are inferred like Rails (customer -> customer_id, Customer;
      # has_many in Order -> order_id). foreign_key: (fk:) and primary_key:
      # (pk:) replace them.
      def belongs_to(name, class_name: nil, foreign_key: nil, fk: nil, primary_key: nil, pk: nil)
        foreign_key = either(name, :foreign_key, foreign_key, :fk, fk) || "#{name}_id"

        associate(name, :belongs_to, class_name || name.to_s.camelize, foreign_key, keys_pk(name, primary_key, pk), {})
      end

      def has_many(name, class_name: nil, foreign_key: nil, fk: nil, primary_key: nil, pk: nil, sort: nil, limit: nil, where: nil)
        options = { sort: sort, limit: limit, query: where }.compact
        foreign_key = either(name, :foreign_key, foreign_key, :fk, fk) || "#{model_name.singular}_id"

        associate(name, :has_many, class_name || name.to_s.singularize.camelize, foreign_key, keys_pk(name, primary_key, pk), options)
      end

      def has_one(name, class_name: nil, foreign_key: nil, fk: nil, primary_key: nil, pk: nil, sort: nil, where: nil)
        options = { sort: sort, query: where }.compact
        foreign_key = either(name, :foreign_key, foreign_key, :fk, fk) || "#{model_name.singular}_id"

        associate(name, :has_one, class_name || name.to_s.camelize, foreign_key, keys_pk(name, primary_key, pk), options)
      end

      def reflections
        inherited = superclass.respond_to?(:reflections) ? superclass.reflections : {}

        inherited.merge(own_reflections)
      end

      # The lookup key for includes(:a, b: :c).
      def lookup_for(names)
        normalize_includes(names).to_h do |name, nested|
          reflection = reflections[name] || raise(ArgumentError, "#{self.name} has no association #{name}")

          reflection.klass.prepare_lookup(reflection)

          [name, reflection.lookup(nested)]
        end
      end

      # Declares the collection of the other side, and the index the lookup
      # needs there (once per process).
      def prepare_lookup(reflection)
        ensure_collection!

        return if reflection.kind == :belongs_to || reflection.foreign_key.to_s == "_id"

        @lookup_indexes ||= Set.new

        return if @lookup_indexes.include?(reflection.foreign_key.to_s)

        Rails.client.ensure_index(collection_name, reflection.foreign_key.to_s)
        @lookup_indexes << reflection.foreign_key.to_s
      end

      private

      def keys_pk(name, primary_key, pk)
        either(name, :primary_key, primary_key, :pk, pk) || "_id"
      end

      # either takes one of two names of the same option.
      def either(name, long, long_value, short, short_value)
        if long_value && short_value
          raise ArgumentError, "#{self.name}.#{name}: #{long}: and #{short}: are the same option; pass one"
        end

        (long_value || short_value)&.to_s
      end

      def own_reflections
        @own_reflections ||= {}
      end

      def associate(name, kind, class_name, foreign_key, primary_key, options)
        name = name.to_s

        own_reflections[name] = Reflection.new(name: name, kind: kind, class_name: class_name.to_s, foreign_key: foreign_key.to_s,
                                               primary_key: primary_key.to_s, options: options)

        define_method(name) { association(name) }
      end

      # includes(:a, b: :c) -> { "a" => {}, "b" => { "c" => {} } }
      def normalize_includes(names)
        return names.to_h { |k, v| [k.to_s, normalize_includes(v)] } if names.is_a?(Hash)

        Array(names).each_with_object({}) do |item, out|
          item.is_a?(Hash) ? out.merge!(normalize_includes(item)) : out[item.to_s] = {}
        end
      end
    end

    # The associated record, or for a has_many a Ndb::Relation (lazy and
    # chainable, like ActiveRecord). It is kept on the record for the key it
    # was read with: a change of the foreign key (or reload) reads it again.
    def association(name)
      reflection = self.class.reflections.fetch(name)
      key = association_key(reflection)
      cached = (@associations ||= {})[name]

      return cached[:value] if cached && cached[:key] == key

      value = load_association(reflection, key)

      @associations[name] = { key: key, value: value }

      value
    end

    # Sets the associations that a lookup brought in row.
    def load_associations(row)
      self.class.reflections.each do |name, reflection|
        next unless row.key?(name)

        key = association_key(reflection)

        (@associations ||= {})[name] = { key: key, value: build_association(reflection, key, row[name]) }
      end
    end

    private

    # The field of this record the association reads: the foreign key of a
    # belongs_to, the primary key of a has_many or has_one.
    def association_key(reflection)
      field = reflection.kind == :belongs_to ? reflection.foreign_key : reflection.primary_key

      respond_to?(field) ? public_send(field) : nil
    end

    def build_association(reflection, key, value)
      target = reflection.klass

      return association_scope(reflection, key).preload(Array(value).map { |doc| target.instantiate(doc) }) if reflection.many?
      return nil if value.nil?
      return value.map { |doc| target.instantiate(doc) } if value.is_a?(Array)

      target.instantiate(value)
    end

    def load_association(reflection, key)
      if reflection.kind == :belongs_to
        return key.nil? ? nil : reflection.klass.find_by(reflection.primary_key => key)
      end

      scope = association_scope(reflection, key)

      reflection.many? ? scope : scope.first
    end

    # The relation of a has_many or has_one: the other side whose foreign
    # key is key, with the declared where, sort and limit.
    def association_scope(reflection, key)
      scope = reflection.klass.where(reflection.foreign_key => key)
      scope = scope.where_raw(reflection.options[:query]) if reflection.options[:query]
      scope = scope.order(reflection.options[:sort]) if reflection.options[:sort]
      scope = scope.limit(reflection.options[:limit]) if reflection.options[:limit]

      scope
    end
  end
end

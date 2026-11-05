# frozen_string_literal: true

module NdbRecord
  # The model class, like ActiveRecord::Base: ActiveModel + a lazy Relation.
  class Base
    include ActiveModel::Model
    include ActiveModel::Attributes
    include ActiveModel::Validations
    include ActiveModel::Callbacks
    include ActiveModel::Dirty
    include ActiveModel::Serializers::JSON
    include Persistence

    extend ActiveSupport::DescendantsTracker

    ENSURE_LOCK = Mutex.new

    attribute :_id, :string

    class << self
      QUERY_METHODS = %i[
        where order limit offset page after group find find_by find_by! first count pluck exists?
        find_each in_batches sum average minimum maximum to_a each
      ].freeze

      delegate(*QUERY_METHODS, to: :all)

      attr_writer :abstract_class

      def abstract_class?
        @abstract_class == true
      end

      def all
        Relation.new(self)
      end

      def collection_name
        @collection_name || model_name.plural
      end

      def collection_name=(name)
        @collection_name = name.to_s
      end

      def primary_key(name = nil)
        if name
          @primary_key = name.to_s
          attribute(name, :string) unless attribute_types.key?(@primary_key)
        end

        @primary_key || (superclass.respond_to?(:primary_key) ? superclass.primary_key : "_id")
      end

      def primary_key=(name)
        primary_key(name)
      end

      def source(glob = nil)
        @source = glob if glob

        @source
      end

      def attribute(name, *args, index: false, unique: false, time: false, **options)
        own_indexed << name.to_s if index
        own_unique << name.to_s if unique
        @time_field = name.to_s if time

        super(name, *args, **options)
      end

      # The collection time field (plan §5): zone maps, $dateTrunc, TTL.
      def time_field
        @time_field || (superclass.respond_to?(:time_field) ? superclass.time_field : nil)
      end

      def indexed_fields
        inherited_list(:indexed_fields) | own_indexed
      end

      def unique_fields
        inherited_list(:unique_fields) | own_unique
      end

      # The ensure_collection body for this model.
      def collection_definition
        definition = { "id" => primary_key }

        definition["indexes"] = indexed_fields if indexed_fields.any?
        definition["unique"] = unique_fields if unique_fields.any?
        definition["time"] = time_field if time_field
        definition.merge!("write" => "direct", "path" => source) if source

        definition
      end

      # Declares the collection once per process (and once per client).
      def ensure_collection!
        client = NdbRecord.client

        return if @ensured_client.equal?(client)

        ENSURE_LOCK.synchronize do
          client.ensure_collection(collection_name, collection_definition)

          @ensured_client = client
        end
      end

      # Every load asks for "_v", so a loaded record can send if_version.
      def ndb_find(body)
        ensure_collection!

        NdbRecord.client.find(collection_name, body.merge("versions" => true))
      end

      def ndb_aggregate(body)
        ensure_collection!

        NdbRecord.client.aggregate(collection_name, body)
      end

      def ndb_insert(doc)
        ensure_collection!

        NdbRecord.client.insert(collection_name, [doc]).first
      end

      def ndb_update(id, **change)
        ensure_collection!

        NdbRecord.client.update(collection_name, id, **change)
      end

      def ndb_delete(id, if_version: nil)
        ensure_collection!

        NdbRecord.client.delete(collection_name, id, if_version: if_version)
      end

      private

      def own_indexed
        @own_indexed ||= []
      end

      def own_unique
        @own_unique ||= []
      end

      def inherited_list(name)
        superclass.respond_to?(name) ? superclass.public_send(name) : []
      end
    end

    # The generated _id stays out of the JSON when another field is the id.
    def serializable_hash(options = nil)
      hash = super

      hash.delete("_id") if self.class.primary_key != "_id" && hash["_id"].nil?

      hash
    end
  end
end

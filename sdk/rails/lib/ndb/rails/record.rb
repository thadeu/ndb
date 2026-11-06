# frozen_string_literal: true

module Ndb
  # The model class, like ActiveRecord::Base: ActiveModel + a lazy Relation.
  class Record
    include ActiveModel::Model
    include ActiveModel::Attributes
    include ActiveModel::Validations
    include ActiveModel::Callbacks
    include ActiveModel::Dirty
    include ActiveModel::Serializers::JSON
    include Persistence

    extend ActiveSupport::DescendantsTracker
    extend DynamicFinders

    ENSURE_LOCK = Mutex.new

    attribute :_id, :string

    class << self
      QUERY_METHODS = %i[
        where order limit offset page after group find find_by find_by! first count pluck exists?
        find_each in_batches sum average minimum maximum to_a each destroy_all delete_all
      ].freeze

      delegate(*QUERY_METHODS, to: :all)

      attr_writer :abstract_class

      def abstract_class?
        @abstract_class == true
      end

      def all
        Relation.new(self)
      end

      # Finds the record with these attributes, or builds an unsaved one.
      def find_or_initialize_by(attrs, &block)
        find_by(attrs) || new(attrs).tap { |record| block&.call(record) }
      end

      # Atomic through the daemon: two callers with the same attrs get one
      # document. An invalid record is returned unsaved.
      def find_or_create_by(attrs, &)
        find_or_create(attrs, false, &)
      end

      def find_or_create_by!(attrs, &)
        find_or_create(attrs, true, &)
      end

      # Destroys the records with these ids (callbacks run). Returns the
      # record, or an array for an array of ids.
      def destroy(id_or_ids)
        return find(id_or_ids).each(&:destroy) if id_or_ids.is_a?(Array)

        find(id_or_ids).tap(&:destroy)
      end

      # Deletes by _id in one daemon call, without callbacks. Returns the count.
      def delete(id_or_ids)
        ids = Array(id_or_ids)

        return 0 if ids.empty?

        where(primary_key => ids).delete_all
      end

      def collection_name
        @collection_name || model_name.plural
      end

      def collection_name=(name)
        @collection_name = name.to_s
      end

      # The primary key is always _id. For a natural key (an email, a SKU),
      # keep _id and mark that attribute `unique: true`.
      def primary_key
        "_id"
      end

      def source(glob = nil)
        @source = glob if glob

        @source
      end

      def attribute(name, *args, index: false, unique: false, time: false, expires: false, **options)
        own_indexed << name.to_s if index
        own_unique << name.to_s if unique
        @time_field = name.to_s if time
        @expires_field = name.to_s if expires

        super(name, *args, **options)
      end

      # The collection time field (plan §5): zone maps, $dateTrunc, TTL.
      def time_field
        @time_field || (superclass.respond_to?(:time_field) ? superclass.time_field : nil)
      end

      # The field that holds the expiry time of each document (plan §11.5).
      def expires_field
        @expires_field || (superclass.respond_to?(:expires_field) ? superclass.expires_field : nil)
      end

      # Documents expire this long after the time field: `ttl 24.hours`.
      # Returns the Go-style duration the daemon reads, such as "86400s".
      def ttl(duration = nil)
        @ttl = "#{duration.to_i}s" if duration

        @ttl || (superclass.respond_to?(:ttl) ? superclass.ttl : nil)
      end

      def indexed_fields
        inherited_list(:indexed_fields) | own_indexed
      end

      def unique_fields
        inherited_list(:unique_fields) | own_unique
      end

      # The ensure_collection body for this model.
      def collection_definition
        definition = {}

        definition["indexes"] = indexed_fields if indexed_fields.any?
        definition["unique"] = unique_fields if unique_fields.any?
        definition["time"] = time_field if time_field
        definition["expires_field"] = expires_field if expires_field
        definition["ttl"] = ttl if ttl
        definition.merge!("write" => "direct", "path" => source) if source

        definition
      end

      # Declares the collection once per process (and once per client).
      def ensure_collection!
        client = Rails.client

        return if @ensured_client.equal?(client)

        ENSURE_LOCK.synchronize do
          client.ensure_collection(collection_name, collection_definition)

          @ensured_client = client
        end
      end

      # Every load asks for "_v", so a loaded record can guard its write with it.
      def ndb_find(body)
        ensure_collection!

        Rails.client.find(collection_name, body.merge("versions" => true))
      end

      def ndb_aggregate(body)
        ensure_collection!

        Rails.client.aggregate(collection_name, body)
      end

      def ndb_insert(doc, expire_in: nil)
        ensure_collection!

        Rails.client.insert(collection_name, [doc], expire_in: expire_in).first
      end

      # Atomic update by filter, see Ndb::Client#update.
      def ndb_update(filter, ops, **options)
        ensure_collection!

        Rails.client.update(collection_name, filter, ops, **options)
      end

      def ndb_delete(filter, **options)
        ensure_collection!

        Rails.client.delete(collection_name, filter, **options)
      end

      private

      def dynamic_finder_klass = self
      def dynamic_finder_scope = all

      def find_or_create(attrs, bang, &block)
        found = find_by(attrs)

        return found if found

        record = new(attrs)

        block&.call(record)

        bang ? record.create_unless_exists!(attrs) : record.create_unless_exists(attrs)
      end

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
  end
end

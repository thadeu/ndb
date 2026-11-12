# frozen_string_literal: true

module Ndb
  # The model class, like ActiveRecord::Base: ActiveModel + a lazy Relation.
  class Record
    include ActiveModel::Model
    include ActiveModel::Attributes
    include ActiveModel::Validations
    include ActiveModel::Validations::Callbacks
    include Validations
    include ActiveModel::Callbacks
    include ActiveModel::Dirty
    include ActiveModel::Serializers::JSON
    include Persistence
    include Locking
    include Transactions
    include Associations

    extend ActiveSupport::DescendantsTracker
    extend DynamicFinders

    ENSURE_LOCK = Mutex.new

    attribute :_id, :string

    # The answer of Model.search.
    SearchResult = Data.define(:records, :next_cursor, :total)

    class << self
      QUERY_METHODS = %i[
        where where_raw order limit offset page after group unwind includes lock encrypted find find_by find_by! first last count pluck exists?
        find_each in_batches sum average minimum maximum to_a each destroy_all delete_all to_ndb
      ].freeze

      delegate(*QUERY_METHODS, to: :all)

      attr_writer :abstract_class

      def abstract_class?
        @abstract_class == true
      end

      # Inside scoping (a scope or a class method called on a relation), all
      # is that relation, so where, order... add to it.
      def all
        Thread.current[:ndb_scopes]&.[](self) || Relation.new(self)
      end

      # Runs the block with relation as all, like ActiveRecord scoping.
      def scoping(relation)
        scopes = (Thread.current[:ndb_scopes] ||= {})
        before = scopes[self]

        scopes[self] = relation

        yield
      ensure
        scopes[self] = before
      end

      # A named query, like ActiveRecord scope. The body runs on the
      # relation; it can also be called on a relation:
      # `Message.where(app_id: id).recent`.
      #
      #   scope :recent, -> { order(created_at: :desc) }
      #   scope :top, ->(n) { order(score: :desc).limit(n) }
      def scope(name, body)
        raise ArgumentError, "the body of scope #{name} must be callable" unless body.respond_to?(:call)

        if Relation.method_defined?(name) || Relation.private_method_defined?(name)
          raise ArgumentError, "#{self.name} cannot have a scope named #{name}: a relation already has that method"
        end

        define_singleton_method(name) do |*args, **kwargs|
          relation = all

          scoping(relation) { relation.instance_exec(*args, **kwargs, &body) } || relation
        end
      end

      # Writes a document by its _id, like ActiveRecord upsert: it creates
      # the document when the _id is free, and merges the given attributes
      # into the stored document when it exists (the others stay). No
      # validations or callbacks run. id is an alias of _id. Returns
      # {"_id", "_v"}.
      #
      #   User.upsert(id: "u1", plan: "pro")
      def upsert(attributes = nil, expire_in: nil, **attrs)
        upsert_all([attributes || attrs], expire_in: expire_in).first
      end

      # upsert for many documents in one transaction, all or nothing.
      def upsert_all(attributes, expire_in: nil)
        ensure_collection!

        docs = attributes.map { |attrs| upsert_document(attrs) }

        expire_in = expire_in.to_i if expire_in.is_a?(ActiveSupport::Duration)

        Rails.client.bulk(collection_name, docs, expire_in: expire_in, upsert: true)
      end

      # Runs a find with a whole body built elsewhere, such as from a search
      # form: { "query" => {...}, "sort" => {...}, "limit" => 50, "after" =>
      # token, "count" => true }. The body is sent as is (the daemon
      # validates it). Returns the records, the next cursor (nil on the last
      # page) and the total (with "count" => true).
      def search(body)
        raise ArgumentError, "search takes a Hash, got #{body.class}" unless body.is_a?(Hash)

        page = ndb_find(body.transform_keys(&:to_s))

        SearchResult.new(page.rows.map { |row| instantiate(row) }, page.next, page.total)
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

      # Removes every document of the collection, with no callbacks, and
      # keeps its definition and indexes. The cost does not depend on the
      # number of documents. Watch consumers get one "truncate" event.
      def truncate!
        ensure_collection!

        Rails.client.truncate(collection_name)

        true
      end

      # Removes the collection: documents, definition and watch consumers.
      # The next use of the model declares it again, empty.
      def drop!
        Rails.client.drop(collection_name)

        @ensured_client = nil

        true
      end

      def collection_name
        "#{Rails.configuration.collection_prefix}#{@collection_name || model_name.plural}"
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

      # encrypted: true makes the daemon encrypt the attribute before it
      # stores it; blind: :numeric (or :exact, :lower, :upper) also lets where and
      # find_by find it by value, and index or unique name it.
      def attribute(name, *args, index: false, unique: false, time: false, expires: false, encrypted: false, blind: nil, **options)
        own_indexed << name.to_s if index
        own_unique << name.to_s if unique
        own_encrypted << name.to_s if encrypted && !blind
        own_blind[name.to_s] = blind.to_s if blind
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

      # An index on 1 to 4 attributes or paths. With one it is `index: true`
      # as a call, for a path that is not an attribute ("items.sku"). With
      # more it is a compound index, in this order: it serves a where with
      # equality on the first attributes and a range on the next one. With
      # unique: true it is a unique key instead (one live document per value
      # or tuple of values; Ndb::RecordNotUnique).
      #
      #   index :account_id, :created_at
      #   index :account_id, :email, unique: true
      #   index "items.sku"
      def index(*names, unique: false)
        raise ArgumentError, "index takes 1 to 4 attributes" unless (1..4).cover?(names.size)

        (unique ? own_unique : own_indexed) << names.join(",")
      end

      def indexed_fields
        inherited_list(:indexed_fields) | own_indexed
      end

      def unique_fields
        inherited_list(:unique_fields) | own_unique
      end

      def encrypted_fields
        inherited_list(:encrypted_fields) | own_encrypted
      end

      def blind_fields
        (superclass.respond_to?(:blind_fields) ? superclass.blind_fields : {}).merge(own_blind)
      end

      # The ensure_collection body for this model.
      def collection_definition
        definition = {}

        definition["indexes"] = indexed_fields if indexed_fields.any?
        definition["unique"] = unique_fields if unique_fields.any?
        definition["encrypted"] = encrypted_fields if encrypted_fields.any?
        definition["blind"] = blind_fields if blind_fields.any?
        definition["time"] = time_field if time_field
        definition["expires_field"] = expires_field if expires_field
        definition["ttl"] = ttl if ttl
        definition.merge!("write" => "direct", "path" => source) if source

        definition
      end

      # A collection pattern ("logs-2026-*") reads many collections and
      # writes none.
      def collection_pattern?
        collection_name.include?("*")
      end

      # Declares the collection once per process (and once per client). A
      # pattern declares nothing.
      def ensure_collection!
        return if collection_pattern?

        Testing.touch(collection_name) if defined?(Testing) && !source

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

        Rails.client.search(collection_name, body.merge("versions" => true))
      end

      def ndb_aggregate(body)
        ensure_collection!

        Rails.client.aggregate(collection_name, body)
      end

      # The given attributes, cast by their type, with id as _id.
      def upsert_document(attrs)
        attrs.to_h.each_with_object({}) do |(name, value), doc|
          key = name.to_s == "id" ? primary_key : name.to_s
          type = attribute_types[key]

          doc[key] = Conditions.json_value(type ? type.serialize(type.cast(value)) : value)
        end
      end

      def ndb_insert(doc, expire_in: nil)
        ensure_collection!

        return Rails.client.insert(collection_name, doc) unless expire_in

        Rails.client.bulk(collection_name, [doc], expire_in: expire_in).first
      end

      # Atomic update by query, see Ndb::Client#update_by.
      def ndb_update(query, ops, **options)
        ensure_collection!

        Rails.client.update_by(collection_name, query, ops, **options)
      end

      def ndb_delete(query, **options)
        ensure_collection!

        Rails.client.delete_by(collection_name, query, **options)
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

      def own_encrypted
        @own_encrypted ||= []
      end

      def own_blind
        @own_blind ||= {}
      end

      def inherited_list(name)
        superclass.respond_to?(name) ? superclass.public_send(name) : []
      end
    end
  end
end

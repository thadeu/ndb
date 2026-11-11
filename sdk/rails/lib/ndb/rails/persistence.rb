# frozen_string_literal: true

module Ndb
  class RecordNotSaved < Error; end

  # Writes: dirty attributes -> $set/$unset, filtered by the id and the loaded
  # version ("_v"). A write that matches nothing is a stale record.
  module Persistence
    extend ActiveSupport::Concern

    included do
      define_model_callbacks :save, :create, :update, :destroy
    end

    class_methods do
      def create(attrs = {}, expire_in: nil, **rest, &block)
        return attrs.map { |a| create(a, expire_in: expire_in, &block) } if attrs.is_a?(Array)

        attrs = attrs.merge(rest)

        record = new(attrs)

        yield record if block
        record.save(expire_in: expire_in)

        record
      end

      def create!(attrs = {}, expire_in: nil, **rest, &block)
        return attrs.map { |a| create!(a, expire_in: expire_in, &block) } if attrs.is_a?(Array)

        attrs = attrs.merge(rest)

        record = new(attrs)

        yield record if block
        record.save!(expire_in: expire_in)

        record
      end

      def instantiate(row)
        row = row.dup
        version = row.delete("_v")
        source = row.delete("_coll")
        record = new(row.slice(*attribute_names))

        record.send(:loaded!, version, source)
        record.load_associations(row) if record.respond_to?(:load_associations)

        record
      end
    end

    attr_reader :version

    # The collection a record was read from, when its model reads a
    # collection pattern (self.collection_name = "logs-2026-*"); nil otherwise.
    attr_reader :source_collection

    # id is an alias of _id.
    def id
      _id
    end

    def id=(value)
      self._id = value
    end

    def persisted?
      @persisted == true && !destroyed?
    end

    def new_record?
      !@persisted
    end

    def destroyed?
      @destroyed == true
    end

    def to_key
      persisted? ? [id] : nil
    end

    # expire_in (seconds, an ActiveSupport::Duration or "30m") makes the daemon
    # set the expires field to now + expire_in in the same write. The model
    # needs an `expires: true` attribute (Ndb::TtlNotEnabled otherwise).
    def save(validate: true, expire_in: nil)
      return false if validate && !valid?(new_record? ? :create : :update)

      @expire_in = wire_duration(expire_in)

      run_callbacks(:save) { new_record? ? create_record : update_record } ? true : false
    ensure
      @expire_in = nil
    end

    def save!(**)
      save(**) || raise(errors.any? ? RecordInvalid.new(self) : RecordNotSaved.new("Failed to save the record"))
    end

    def update(attrs = {}, expire_in: nil, **rest)
      assign_attributes(attrs.merge(rest))

      save(expire_in: expire_in)
    end

    def update!(attrs = {}, expire_in: nil, **rest)
      assign_attributes(attrs.merge(rest))

      save!(expire_in: expire_in)
    end

    # Makes the stored document expire in a duration (like EXPIRE in Redis):
    # `session.expire(30.minutes)`. The version and the expires attribute are
    # refreshed; other unsaved changes stay unsaved. Raises Ndb::NotFound for
    # a missing or expired document and Ndb::TtlNotEnabled without an
    # `expires: true` attribute.
    def expire(duration)
      result = Rails.client.expire(self.class.collection_name, id, in: wire_duration(duration))

      refresh_expiry(result, Time.iso8601(result["expires_at"]))
    end

    # Removes the expiry of the stored document (PERSIST in Redis). It is not
    # called `persist!` or `persisted?`, which mean something else in
    # ActiveModel.
    def persist_ttl!
      refresh_expiry(Rails.client.persist(self.class.collection_name, id), nil)
    end

    # The seconds left (Integer), -1 when the document never expires, nil when
    # it is missing or expired.
    def ttl
      Rails.client.ttl(self.class.collection_name, id)
    end

    def destroy
      result = run_callbacks(:destroy) do
        delete_record if persisted?
        @destroyed = true
      end

      result ? self : false
    end

    def destroy!
      destroy || raise(RecordNotDestroyed.new("Failed to destroy the record", self))
    end

    # Deletes by _id: no callbacks and no _v guard, like ActiveRecord.
    def delete
      self.class.ndb_delete({ self.class.primary_key => id }) if persisted?

      @destroyed = true

      self
    end

    # The save of find_or_create_by: validations, then the save and create
    # callbacks around one daemon upsert. The filter is attrs; the other
    # attributes are written only if the upsert creates the document
    # ($setOnInsert), so a writer that got there first is never overwritten.
    # Returns the new record, or the one that won the race, or this record
    # unsaved when it is invalid.
    def create_unless_exists(attrs)
      return self unless valid?(:create)

      saved = run_callbacks(:save) { run_callbacks(:create) { upsert_record(attrs) } }

      saved ? @upsert_winner || self : self
    ensure
      @upsert_winner = nil
    end

    def create_unless_exists!(attrs)
      raise RecordInvalid, self unless valid?(:create)

      record = create_unless_exists(attrs)

      raise RecordNotSaved, "Failed to save the record" unless record.persisted?

      record
    end

    # Adds by (default 1) to a number attribute in one atomic write on the
    # daemon, and refreshes the attribute and the version. Other unsaved
    # changes stay unsaved.
    def increment!(attribute, by = 1)
      add_to!(attribute, by)
    end

    def decrement!(attribute, by = 1)
      add_to!(attribute, -by)
    end

    # The optimistic lock with the retry done for you. Reads the document
    # again, yields the record, and saves it, guarded by the version it read.
    # When another write came first (Ndb::StaleObjectError), it reads the
    # document again and yields again, up to retries times; then it raises
    # the error. So the block can run more than once: when it has an effect
    # outside ndb, use with_lock. An exception in the block stops it and
    # nothing is written. Returns the record. Nothing waits on the daemon.
    #
    #   wallet.with_retry do |w|
    #     raise InsufficientFunds if w.balance_cents < 500
    #
    #     w.balance_cents -= 500
    #   end
    def with_retry(retries: 5)
      retries = [retries, 1].max

      retries.times do |attempt|
        sleep((attempt * 5 + rand(10)) / 1000.0) if attempt.positive?

        reload
        yield self
        save!

        return self
      rescue StaleObjectError
        raise if attempt == retries - 1
      end
    end

    # Reads the document again, like ActiveRecord reload: the attributes,
    # the version and the associations (they load again on next use).
    def reload
      raise RecordNotSaved, "Cannot reload a record that is not saved" unless persisted?

      fresh = self.class.find(id)

      @attributes = fresh.instance_variable_get(:@attributes)
      @version = fresh.version
      @associations = nil

      clear_changes_information

      self
    end

    def ==(other)
      other.instance_of?(self.class) && !id.nil? && id == other.id
    end

    private

    def wire_duration(value)
      value.is_a?(ActiveSupport::Duration) ? value.to_i : value
    end

    def refresh_expiry(result, expires_at)
      @version = result["_v"]

      field = self.class.expires_field

      return self unless field

      public_send("#{field}=", expires_at)
      clear_attribute_changes([field])

      self
    end

    def record_filter
      filter = { self.class.primary_key => id }

      filter["_v"] = version if version

      filter
    end

    def delete_record
      if (tx = current_transaction)
        tx.delete(self)

        return
      end

      deleted = holding { self.class.ndb_delete(record_filter, **lock_options) }["deleted"]

      raise StaleObjectError.new("#{self.class.name} #{id} changed or was deleted", code: "stale_object") if deleted.zero?

      written_with_lock
    end

    def add_to!(attribute, by)
      name = attribute.to_s

      raise RecordNotSaved, "Cannot update a record that is not saved" unless persisted?
      raise ArgumentError, "#{name} is not an attribute of #{self.class.name}" unless self.class.attribute_names.include?(name)

      if (tx = current_transaction)
        tx.update(self, { "$inc" => { name => by } })
        public_send("#{name}=", (public_send(name) || 0) + by)
        clear_attribute_changes([name])

        return self
      end

      result = holding { self.class.ndb_update({ self.class.primary_key => id }, { "$inc" => { name => by } }, **lock_options) }

      raise NotFound.new("#{self.class.name} #{id} was deleted", code: "not_found") if result["matched"].zero?

      @version = result["doc"]["_v"]

      public_send("#{name}=", result["doc"][name])
      clear_attribute_changes([name])

      self
    end

    def loaded!(version, source = nil)
      @persisted = true
      @version = version
      @source_collection = source if source

      changes_applied
    end

    def upsert_record(attrs)
      filter = attrs.keys.to_h do |key|
        name = key.to_s == "id" ? self.class.primary_key : key.to_s

        [name, json_attribute(name)]
      end

      on_insert = insert_document.except(*filter.keys)
      result = self.class.ndb_update(filter, { "$setOnInsert" => on_insert }, upsert: true)

      if result["matched"].zero?
        self.id = result["doc"]["_id"] if id.nil?
        loaded!(result["doc"]["_v"])
      else
        @upsert_winner = self.class.instantiate(result["doc"])
      end

      true
    end

    def create_record
      run_callbacks(:create) do
        if (tx = current_transaction)
          raise ArgumentError, "expire_in is not supported inside a transaction" if @expire_in

          self.id ||= Ndb.new_id
          tx.insert(self, insert_document)
          loaded!(nil)

          next true
        end

        result = self.class.ndb_insert(insert_document, expire_in: @expire_in)

        self.id = result["_id"] if id.nil?
        loaded!(result["_v"])

        true
      end
    end

    def update_record
      run_callbacks(:update) do
        set, unset = changed_fields

        if (tx = current_transaction)
          raise ArgumentError, "expire_in is not supported inside a transaction" if @expire_in

          tx.update(self, update_ops(set, unset)) unless set.empty? && unset.empty?
        elsif !(set.empty? && unset.empty? && @expire_in.nil?)
          result = holding { self.class.ndb_update(record_filter, update_ops(set, unset), expire_in: @expire_in, **lock_options) }

          raise StaleObjectError.new("#{self.class.name} #{id} changed or was deleted", code: "stale_object") if result["matched"].zero?

          @version = result["doc"]["_v"]
        end

        changes_applied
        written_with_lock

        true
      end
    end

    def update_ops(set, unset)
      ops = {}

      ops["$set"] = set unless set.empty?
      ops["$unset"] = unset.to_h { |name| [name, true] } unless unset.empty?

      ops.empty? ? { "$set" => {} } : ops
    end

    def insert_document
      attribute_names.each_with_object({}) do |name, doc|
        value = json_attribute(name)

        doc[name] = value unless value.nil?
      end
    end

    def changed_fields
      names = changed - [self.class.primary_key]
      set = {}
      unset = []

      names.each do |name|
        value = json_attribute(name)
        value.nil? ? unset << name : set[name] = value
      end

      [set, unset]
    end

    def json_attribute(name)
      Conditions.json_value(self.class.attribute_types[name].serialize(public_send(name)))
    end
  end
end

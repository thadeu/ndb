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
        record = new(row.slice(*attribute_names))

        record.send(:loaded!, version)

        record
      end
    end

    attr_reader :version

    def id
      public_send(self.class.primary_key)
    end

    def id=(value)
      public_send("#{self.class.primary_key}=", value)
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

    # Adds by (default 1) to a number attribute in one atomic write on the
    # daemon, and refreshes the attribute and the version. Other unsaved
    # changes stay unsaved.
    def increment!(attribute, by = 1)
      add_to!(attribute, by)
    end

    def decrement!(attribute, by = 1)
      add_to!(attribute, -by)
    end

    def ==(other)
      other.instance_of?(self.class) && !id.nil? && id == other.id
    end

    private

    def wire_duration(value)
      value.is_a?(ActiveSupport::Duration) ? value.to_i : value
    end

    def refresh_expiry(result, expires_at)
      @version = result["v"]

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
      deleted = self.class.ndb_delete(record_filter)["deleted"]

      raise StaleObjectError.new("#{self.class.name} #{id} changed or was deleted", code: "stale_object") if deleted.zero?
    end

    def add_to!(attribute, by)
      name = attribute.to_s

      raise RecordNotSaved, "Cannot update a record that is not saved" unless persisted?
      raise ArgumentError, "#{name} is not an attribute of #{self.class.name}" unless self.class.attribute_names.include?(name)

      result = self.class.ndb_update({ self.class.primary_key => id }, { "$inc" => { name => by } })

      raise NotFound.new("#{self.class.name} #{id} was deleted", code: "not_found") if result["matched"].zero?

      @version = result["doc"]["_v"]

      public_send("#{name}=", result["doc"][name])
      clear_attribute_changes([name])

      self
    end

    def loaded!(version)
      @persisted = true
      @version = version

      changes_applied
    end

    def create_record
      run_callbacks(:create) do
        result = self.class.ndb_insert(insert_document, expire_in: @expire_in)

        self.id = result["id"] if id.nil?
        loaded!(result["v"])

        true
      end
    end

    def update_record
      run_callbacks(:update) do
        set, unset = changed_fields

        unless set.empty? && unset.empty? && @expire_in.nil?
          result = self.class.ndb_update(record_filter, update_ops(set, unset), expire_in: @expire_in)

          raise StaleObjectError.new("#{self.class.name} #{id} changed or was deleted", code: "stale_object") if result["matched"].zero?

          @version = result["doc"]["_v"]
        end

        changes_applied

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

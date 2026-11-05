# frozen_string_literal: true

module Ndb
  class RecordNotSaved < Error; end

  # Writes: dirty attributes -> set/unset, the loaded version -> if_version.
  module Persistence
    extend ActiveSupport::Concern

    included do
      define_model_callbacks :save, :create, :update, :destroy
    end

    class_methods do
      def create(attrs = {}, &block)
        return attrs.map { |a| create(a, &block) } if attrs.is_a?(Array)

        record = new(attrs)

        yield record if block
        record.save

        record
      end

      def create!(attrs = {}, &block)
        return attrs.map { |a| create!(a, &block) } if attrs.is_a?(Array)

        record = new(attrs)

        yield record if block
        record.save!

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

    def save(validate: true)
      return false if validate && !valid?(new_record? ? :create : :update)

      run_callbacks(:save) { new_record? ? create_record : update_record } ? true : false
    end

    def save!(**)
      save(**) || raise(errors.any? ? RecordInvalid.new(self) : RecordNotSaved.new("Failed to save the record"))
    end

    def update(attrs)
      assign_attributes(attrs)

      save
    end

    def update!(attrs)
      assign_attributes(attrs)

      save!
    end

    def destroy
      result = run_callbacks(:destroy) do
        self.class.ndb_delete(id, if_version: version) if persisted?
        @destroyed = true
      end

      result ? self : false
    end

    def ==(other)
      other.instance_of?(self.class) && !id.nil? && id == other.id
    end

    private

    def loaded!(version)
      @persisted = true
      @version = version

      changes_applied
    end

    def create_record
      run_callbacks(:create) do
        result = self.class.ndb_insert(insert_document)

        self.id = result["id"] if id.nil?
        loaded!(result["v"])

        true
      end
    end

    def update_record
      run_callbacks(:update) do
        set, unset = changed_fields

        unless set.empty? && unset.empty?
          result = self.class.ndb_update(id, set: set, unset: unset, if_version: version)

          @version = result["v"]
        end

        changes_applied

        true
      end
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

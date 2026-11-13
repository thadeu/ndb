# frozen_string_literal: true

module Ndb
  # The small writes of ActiveRecord: touch, update_columns, toggle!,
  # update_attribute, insert_all, the counters, the saved changes of Dirty
  # and the cache keys.
  module RecordWrites
    extend ActiveSupport::Concern

    included do
      define_model_callbacks :touch, only: :after

      attribute_method_affix prefix: "saved_change_to_", suffix: "?"
      attribute_method_affix prefix: "will_save_change_to_", suffix: "?"
      attribute_method_suffix "_before_last_save"
    end

    class_methods do
      # Inserts many documents in one _bulk: no validations, no callbacks.
      # Each gets created_at and updated_at. An _id that exists raises
      # Ndb::RecordNotUnique (code duplicate_id), as insert_all! of
      # ActiveRecord.
      def insert_all(rows)
        ensure_collection!

        now = Conditions.json_value(current_time)

        docs = rows.map do |attrs|
          doc = upsert_document(attrs)

          doc["created_at"] ||= now
          doc["updated_at"] ||= now

          doc
        end

        Rails.client.bulk(collection_name, docs)
      end

      alias_method :insert_all!, :insert_all

      # Adds to number attributes of the documents with these ids in one
      # atomic write, and sets updated_at. Returns the count of matches.
      #
      #   Post.update_counters("p1", views: 1, likes: -1)
      def update_counters(id_or_ids, counters)
        ids = Array(id_or_ids)
        inc = counters.to_h { |name, by| [name.to_s, by] }
        ops = { "$inc" => inc, "$set" => { "updated_at" => Conditions.json_value(current_time) } }

        ndb_update({ primary_key => { "$in" => ids } }, ops, many: true)["matched"]
      end

      def increment_counter(name, id, by: 1)
        update_counters(id, name => by)
      end

      def decrement_counter(name, id, by: 1)
        update_counters(id, name => -by)
      end
    end

    # Sets updated_at, and the named datetime attributes, to now (or time)
    # in one atomic write. No validations, no save callbacks; after_touch
    # runs. Other unsaved changes stay unsaved.
    def touch(*names, time: nil)
      now = time || self.class.current_time
      values = ["updated_at", *names.map(&:to_s)].to_h { |name| [name, now] }

      write_columns(values)
      run_callbacks(:touch)

      true
    end

    # $set of these attributes, cast by type, in one atomic write. No
    # validations, no callbacks, and updated_at does not change.
    def update_columns(attrs)
      write_columns(attrs.to_h { |name, value| [name.to_s, value] })

      true
    end

    def update_column(name, value)
      update_columns(name => value)
    end

    def toggle(attribute)
      public_send("#{attribute}=", !public_send(attribute))

      self
    end

    def toggle!(attribute)
      toggle(attribute)

      update_attribute(attribute, public_send(attribute))
    end

    # Saves with the callbacks and without the validations.
    def update_attribute(name, value)
      public_send("#{name}=", value)

      save(validate: false)
    end

    def saved_changes = previous_changes

    def saved_change_to_attribute?(name) = attribute_previously_changed?(name.to_s)

    def saved_change_to_attribute(name) = attribute_previous_change(name.to_s)

    def attribute_before_last_save(name) = attribute_previously_was(name.to_s)

    def will_save_change_to_attribute?(name) = attribute_changed?(name.to_s)

    # "posts/p1", or "posts/new" for a new record. The version is apart, in
    # cache_version, as ActiveRecord with cache versioning.
    def cache_key
      "#{model_name.cache_key}/#{persisted? ? id : 'new'}"
    end

    # updated_at and the document version: it changes on every write.
    def cache_version
      return unless persisted?

      [updated_at&.utc&.to_fs(:usec), version].compact.join("-")
    end

    def cache_key_with_version
      version = cache_version

      version.present? ? "#{cache_key}-#{version}" : cache_key
    end

    private

    def write_columns(values)
      raise RecordNotSaved, "Cannot update a record that is not saved" unless persisted?

      raise_if_readonly

      names = values.keys

      unknown = names - self.class.attribute_names

      raise ArgumentError, "#{unknown.join(', ')} is not an attribute of #{self.class.name}" if unknown.any?

      values.each { |name, value| public_send("#{name}=", value) }

      set = {}
      unset = {}

      names.each do |name|
        value = json_attribute(name)

        value.nil? ? unset[name] = true : set[name] = value
      end

      ops = {}
      ops["$set"] = set unless set.empty?
      ops["$unset"] = unset unless unset.empty?

      send_columns(ops)
      clear_attribute_changes(names)
    end

    def send_columns(ops)
      if (tx = current_transaction)
        tx.update(self, ops)

        return
      end

      result = holding { self.class.ndb_update({ self.class.primary_key => id }, ops, **lock_options) }

      raise NotFound.new("#{self.class.name} #{id} was deleted", code: "not_found") if result["matched"].zero?

      @version = result["doc"]["_v"]
    end
  end
end

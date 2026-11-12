# frozen_string_literal: true

module Ndb
  # Validators that ActiveRecord has and ActiveModel does not.
  module Validations
    # validates :name, uniqueness: true (or { scope: :app_id }). It asks the
    # daemon if another document has the value, so a form gets an error on
    # the field. Two saves at the same time can both pass: the unique index
    # (unique: true) stays the real guard (Ndb::RecordNotUnique).
    class UniquenessValidator < ActiveModel::EachValidator
      def validate_each(record, attribute, value)
        conditions = { attribute => value }

        Array(options[:scope]).each { |name| conditions[name] = record.public_send(name) }

        others = record.class.where(conditions)
        others = others.where.not(_id: record.id) if record.persisted?

        record.errors.add(attribute, :taken, **options.except(:scope), value: value) if others.exists?
      end
    end
  end
end

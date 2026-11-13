# frozen_string_literal: true

module Ndb
  # after_commit and after_rollback, as ActiveRecord. They run when the write
  # is durable: after the write itself, or, inside Model.transaction, after
  # the transaction commits. after_rollback runs when the transaction writes
  # nothing (an exception or Ndb::Rollback).
  #
  #   after_create_commit { Billing.notify(id) }
  #   after_commit :reindex, on: %i[create update]
  module CommitCallbacks
    extend ActiveSupport::Concern

    included do
      define_callbacks :commit, :rollback
    end

    class_methods do
      def after_commit(*args, on: nil, **options, &block)
        set_callback(:commit, :after, *args, **on_action(on, options), &block)
      end

      def after_rollback(*args, on: nil, **options, &block)
        set_callback(:rollback, :after, *args, **on_action(on, options), &block)
      end

      def after_create_commit(*args, **options, &block)
        after_commit(*args, on: :create, **options, &block)
      end

      def after_update_commit(*args, **options, &block)
        after_commit(*args, on: :update, **options, &block)
      end

      def after_save_commit(*args, **options, &block)
        after_commit(*args, on: %i[create update], **options, &block)
      end

      def after_destroy_commit(*args, **options, &block)
        after_commit(*args, on: :destroy, **options, &block)
      end

      private

      def on_action(on, options)
        return options unless on

        actions = Array(on)

        options.merge(if: Array(options[:if]) + [-> { actions.include?(@commit_action) }])
      end
    end

    private

    # A write of action (:create, :update, :destroy) is done: its commit
    # callbacks run now, or when the transaction it joined ends.
    def committed(action)
      if (tx = RecordTransaction.current)
        tx.on_commit(self, action)

        return
      end

      run_commit_callbacks(:commit, action)
    end

    def run_commit_callbacks(kind, action)
      @commit_action = action

      run_callbacks(kind)
    ensure
      @commit_action = nil
    end
  end
end

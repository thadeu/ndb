# frozen_string_literal: true

module Ndb
  # A write of a transaction block that cannot join it: a record of another
  # collection. Nothing of the block is sent.
  class TransactionError < Error; end

  # The transaction of one collection, as `transaction` of ActiveRecord:
  #
  #   Account.transaction(lock: [from, to], ttl: 5) do
  #     raise Ndb::Rollback if from.balance < amount
  #
  #     from.update!(balance: from.balance - amount)
  #     to.update!(balance: to.balance + amount)
  #   end
  #
  # Inside the block, save, update, create, destroy, increment! and
  # decrement! run their validations and callbacks as always, but send
  # nothing: the writes are collected and go in one call at the end of the
  # block (POST /{coll}/_tx), all of them or none. Then the records get their
  # new versions and the locks are released.
  #
  # - An exception in the block writes nothing and goes on. Ndb::Rollback
  #   writes nothing, and the block returns nil. The records keep the values
  #   set in the block (ActiveRecord does not undo them either).
  # - A refused commit writes nothing: Ndb::TxConflict when a record changed
  #   since it was read (its "_v") or is gone, Ndb::LockExpired when the
  #   locks outlived ttl (the time limit of the transaction).
  # - lock: takes the locks of those records (or ids) in one call, all or
  #   none, and reads the records again under the lock. `lock!` inside the
  #   block also joins the transaction (one call per record; sort them).
  # - Reads inside the block see the stored data, not the writes of the
  #   block.
  # - One collection: a write of another model raises Ndb::TransactionError.
  #   A nested block joins the outer one.
  module Transactions
    extend ActiveSupport::Concern

    class_methods do
      def transaction(lock: nil, ttl: nil, wait: nil)
        outer = RecordTransaction.current

        if outer
          outer.check!(self)

          return yield
        end

        tx = RecordTransaction.new(self)
        RecordTransaction.current = tx

        begin
          tx.lock_records(Array(lock), ttl: ttl, wait: wait) if lock
          result = yield
          tx.commit!

          result
        rescue Rollback
          tx.abort!

          nil
        rescue Exception # rubocop:disable Lint/RescueException
          tx.abort!

          raise
        ensure
          RecordTransaction.current = nil
        end
      end
    end

    private

    # The transaction that this record's writes join, or nil.
    def current_transaction
      tx = RecordTransaction.current

      tx&.check!(self.class)

      tx
    end
  end

  # The state of one Model.transaction block: the writes, the records they
  # belong to, and the lock tokens.
  class RecordTransaction
    KEY = :ndb_record_transaction

    def self.current = Thread.current[KEY]

    def self.current=(tx)
      Thread.current[KEY] = tx
    end

    def initialize(klass)
      @klass = klass
      @ops = []
      @records = []
      @tokens = []
      @guarded = {}
      @locked = {}
    end

    def collection = @klass.collection_name

    def check!(klass)
      return if klass.collection_name == collection

      raise TransactionError.new("a transaction of #{collection} cannot write #{klass.collection_name}", code: "transaction_error")
    end

    # Takes the locks of records (or ids) in one call and reads the records
    # again under the lock.
    def lock_records(list, ttl:, wait:)
      return if list.empty?

      ids = list.map { |r| r.respond_to?(:id) ? r.id : r }
      held = @klass.ndb_lock_set(ids, **{ ttl: ttl, wait: wait }.compact)

      @tokens << held["token"]

      by_id = held["docs"].to_h { |doc| [doc[@klass.primary_key], doc] }

      list.each do |record|
        next unless record.respond_to?(:id)

        record.send(:refresh_from!, by_id.fetch(record.id))
        joined(record, held["token"])
      end

      ids.each { |id| @locked[id] = true }
    end

    # A lock! of a record inside the block.
    def joined(record, token)
      @tokens << token unless @tokens.include?(token)
      @locked[record.id] = true
      @records << record unless @records.include?(record)

      record.send(:hold_in_transaction!, token)
    end

    def insert(record, doc)
      add({ op: "insert", doc: doc }, record)
    end

    def update(record, ops)
      add({ op: "update", id: record.id, update: ops, query: guard(record) }.compact, record)
    end

    def delete(record)
      add({ op: "delete", id: record.id, query: guard(record) }.compact, record)
    end

    def commit!
      if @ops.empty?
        release

        return
      end

      results = Rails.client.tx(collection, @ops.map(&:first), locks: @tokens, unlock: true)["results"]

      @ops.zip(results).each do |(_, record), result|
        record.send(:written_in_transaction!, result["_v"]) if result["_v"]
      end
    ensure
      finish
    end

    def abort!
      release
      finish
    end

    private

    def add(op, record)
      @ops << [op, record]
      @records << record unless @records.include?(record)
    end

    # The "_v" guard of the first write of a record that the transaction
    # did not lock: a record that changed since it was read fails the
    # commit. The later writes of the same record come after it in the same
    # step, so they need none.
    def guard(record)
      return nil if @locked[record.id] || @guarded[record.id] || record.version.nil?

      @guarded[record.id] = true

      { "_v" => record.version }
    end

    def release
      @tokens.each do |token|
        Rails.client.unlock_set(collection, token)
      rescue Error
        nil
      end
    end

    def finish
      @records.each { |record| record.send(:leave_transaction!) }
    end
  end
end

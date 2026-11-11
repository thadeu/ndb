# frozen_string_literal: true

module Ndb
  # Raise it in a transaction block to stop it: nothing is written, the
  # locks are released, and the block returns nil (as ActiveRecord::Rollback).
  class Rollback < StandardError; end

  # The writes of a transaction block of one collection. Nothing is sent
  # until the block ends; then Collection#transaction sends them in one call
  # (POST /{coll}/_tx), all of them or none.
  class Transaction
    SKIP = %w[_id _v _coll].freeze

    attr_reader :ops

    def initialize
      @ops = []
      @before = {}
    end

    # The $set and $unset of the top-level fields that differ.
    def self.changes(before, after)
      set = after.reject { |k, v| SKIP.include?(k) || before.key?(k) && before[k] == v }
      unset = (before.keys - after.keys - SKIP).to_h { |k| [k, ""] }

      ops = {}
      ops["$set"] = set unless set.empty?
      ops["$unset"] = unset unless unset.empty?
      ops
    end

    # Records the insert of doc. A doc without _id gets one from the daemon.
    def insert(doc = nil, **fields)
      @ops << { op: "insert", doc: Ndb.deep_stringify(doc || fields) }

      self
    end

    # Records the write of doc. A document that the transaction locked gets
    # the fields that changed since the lock; any other gets all its fields,
    # guarded by its "_v": a document that changed since it was read fails
    # the transaction (TxConflict).
    def update(doc)
      doc = Ndb.deep_stringify(doc)
      id = doc["_id"] or raise ArgumentError, "update needs a document with _id"

      if (before = @before[id])
        ops = self.class.changes(before, doc)
        @before[id] = Marshal.load(Marshal.dump(doc))
        @ops << { op: "update", id: id, update: ops } unless ops.empty?
      else
        op = { op: "update", id: id, update: { "$set" => doc.except(*SKIP) } }
        op[:query] = { "_v" => doc["_v"] } if doc["_v"]
        @ops << op
      end

      self
    end

    # Records the delete of the document with this _id. A missing document
    # fails the transaction (TxConflict).
    def delete(id)
      @ops << { op: "delete", id: id }

      self
    end

    # Keeps a locked document as it was, for update.
    def locked(doc)
      @before[doc["_id"]] = Marshal.load(Marshal.dump(doc))
    end
  end
end

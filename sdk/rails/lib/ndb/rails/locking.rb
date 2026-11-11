# frozen_string_literal: true

module Ndb
  # The pessimistic lock of a record, like `lock` and `with_lock` of
  # ActiveRecord. The daemon keeps the lock (POST /{coll}/_lock/{id}): until
  # it is released, only this record writes the document. Another lock of
  # the same document waits (wait, then Ndb::Locked), and a write from any
  # other place waits for the release. ActiveRecord releases a lock at the
  # end of its transaction; ndb has no transaction, so a lock ends:
  #
  # - at the end of the block: `Account.lock.find(id) { |a| ... }` and
  #   `account.with_lock { |a| ... }`;
  # - without a block, at the first save or destroy of the record, or at
  #   `unlock!`;
  # - after ttl in any case (default 30 s), so a process that dies holds
  #   nothing. A save after the ttl raises Ndb::LockExpired.
  #
  # The block runs once. Use the lock when the code has an effect outside
  # ndb (a payment, an email); use with_retry when it only changes the record.
  module Locking
    extend ActiveSupport::Concern

    class_methods do
      # POST /{coll}/_lock/{id}, see Ndb::Client#lock.
      def ndb_lock(id, **options)
        ensure_collection!

        Rails.client.lock(collection_name, id, **options)
      end

      # The record of a lock answer, holding the lock.
      def instantiate_locked(held)
        record = instantiate(held["doc"])

        record.send(:hold!, held["token"])

        record
      end
    end

    # Takes the lock and reads the record again, like ActiveRecord lock!.
    # Unsaved changes are dropped. ttl and wait: see Ndb::Client#lock.
    def lock!(ttl: nil, wait: nil)
      raise RecordNotSaved, "Cannot lock a record that is not saved" unless persisted?

      held = self.class.ndb_lock(id, **{ ttl: ttl, wait: wait }.compact)
      fresh = self.class.instantiate(held["doc"])

      @attributes = fresh.instance_variable_get(:@attributes)
      @version = fresh.version
      @associations = nil

      clear_changes_information
      hold!(held["token"])

      self
    end

    # Releases the lock. A lock that is gone already (expired) is not an
    # error.
    def unlock!
      token = @lock_token
      @lock_token = nil
      @lock_scoped = false

      Rails.client.unlock(self.class.collection_name, id, token) if token

      self
    rescue LockExpired
      self
    end

    def locked?
      !@lock_token.nil?
    end

    # Takes the lock, yields the record, and releases the lock at the end of
    # the block, also on an exception. Save inside the block. Returns the
    # record.
    def with_lock(ttl: nil, wait: nil)
      lock!(ttl: ttl, wait: wait)
      scoped_lock { yield self }
    end

    # Runs the block with the lock kept until its end (a save inside does
    # not release it), then releases.
    def scoped_lock
      @lock_scoped = true

      yield

      self
    ensure
      unlock!
    end

    private

    def hold!(token)
      @lock_token = token
      @lock_scoped = false
    end

    # The options of a write of this record: its lock token when it holds one.
    def lock_options
      @lock_token ? { lock: @lock_token } : {}
    end

    # Runs a write of this record. A lock that expired is forgotten, so the
    # record is a plain record again after the error.
    def holding
      yield
    rescue LockExpired
      @lock_token = nil
      @lock_scoped = false

      raise
    end

    # A write outside a block ends the lock, as a commit does.
    def written_with_lock
      unlock! if @lock_token && !@lock_scoped
    end
  end
end

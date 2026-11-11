# frozen_string_literal: true

require "test_helper"

class TransactionTest < Minitest::Test
  def setup
    @accounts = client.collection(NdbTestHelpers.next_name)
    @accounts.insert([{ _id: "a1", balance: 100, note: "x" }, { _id: "a2", balance: 0 }, { _id: "a9", balance: 1 }])
  end

  def transfer(from, to, amount)
    @accounts.transaction(lock: [from, to]) do |tx, docs|
      raise Ndb::Rollback if docs[from]["balance"] < amount

      docs[from]["balance"] -= amount
      docs[to]["balance"] += amount

      tx.update(docs[from])
      tx.update(docs[to])
    end
  end

  def test_transfers_from_crossed_callers
    10.times.map { |i| Thread.new { i.even? ? transfer("a1", "a2", 1) : transfer("a2", "a1", 1) } }.each(&:join)

    assert_equal 100, @accounts.get("a1")["balance"] + @accounts.get("a2")["balance"]
    assert_nil transfer("a2", "a1", 1000)

    @accounts.lock("a1", wait: 0) { nil }
  end

  def test_an_exception_writes_nothing_and_releases
    error = assert_raises(RuntimeError) do
      @accounts.transaction(lock: ["a1"]) do |tx, docs|
        docs["a1"]["balance"] = 0
        tx.update(docs["a1"])

        raise "gateway down"
      end
    end

    assert_equal "gateway down", error.message

    assert_raises(Ndb::TxConflict) do
      @accounts.transaction(lock: ["a1"]) do |tx, docs|
        docs["a1"]["balance"] = 1
        tx.update(docs["a1"])
        tx.delete("nope")
      end
    end

    assert_equal 100, @accounts.get("a1")["balance"]
    assert_raises(Ndb::NotFound) { @accounts.transaction(lock: %w[a1 nope]) { nil } }

    @accounts.lock("a1", wait: 0) { nil }
  end

  def test_without_locks_it_is_an_atomic_batch_with_the_version_guard
    stale = @accounts.get("a1")

    @accounts.transaction do |tx|
      tx.insert(_id: "a3", balance: 7)
      tx.delete("a9")
      tx.update(stale.merge("balance" => 6))
    end

    assert_equal 6, @accounts.get("a1")["balance"]
    assert_equal 7, @accounts.get("a3")["balance"]
    assert_nil @accounts.get("a9")

    assert_raises(Ndb::TxConflict) { @accounts.transaction { |tx| tx.update(stale.merge("balance" => 0)) } }

    @accounts.transaction { nil }
  end
end

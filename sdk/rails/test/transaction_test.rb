# frozen_string_literal: true

require "test_helper"

class TransactionTest < Minitest::Test
  def setup
    @account = model do
      attribute :balance, :integer
      attribute :name, :string

      validates :name, presence: true
    end

    @transfer = model do
      attribute :amount, :integer
    end

    @account.create!(_id: "a1", name: "Ana", balance: 100)
    @account.create!(_id: "a2", name: "Bia", balance: 0)
  end

  def transfer(from, to, amount)
    @account.transaction(lock: [from, to], ttl: 5) do
      raise Ndb::Rollback if from.balance < amount

      from.update!(balance: from.balance - amount)
      to.update!(balance: to.balance + amount)
    end
  end

  def balances = [@account.find("a1").balance, @account.find("a2").balance]

  def test_declared_locks_move_balances_from_crossed_callers
    10.times.map do |i|
      Thread.new do
        a1 = @account.find("a1")
        a2 = @account.find("a2")

        i.even? ? transfer(a1, a2, 1) : transfer(a2, a1, 1)
      end
    end.each(&:join)

    assert_equal 100, balances.sum
    assert_nil transfer(@account.find("a2"), @account.find("a1"), 1000)
    assert_predicate @account.lock(wait: 0).find("a1").unlock!, :persisted?
  end

  def test_the_records_are_read_again_under_the_lock
    stale = @account.find("a1")
    @account.find("a1").update!(balance: 50)

    @account.transaction(lock: [stale, "a2"]) do
      assert_equal 50, stale.balance
      assert_predicate stale, :locked?

      stale.update!(balance: stale.balance - 10)
    end

    refute_predicate stale, :locked?
    assert_equal 40, @account.find("a1").balance
    assert_equal stale.version, @account.find("a1").version
  end

  def test_lock_bang_inside_the_block_as_active_record
    a1 = @account.find("a1")
    a2 = @account.find("a2")

    result = @account.transaction do
      [a1, a2].sort_by(&:id).each(&:lock!)

      a1.update!(balance: a1.balance - 30)
      a2.update!(balance: a2.balance + 30)

      assert_equal [100, 0], balances

      :done
    end

    assert_equal :done, result
    assert_equal [70, 30], balances
    refute_predicate a1, :locked?
    assert_predicate @account.lock(wait: 0).find("a2").unlock!, :persisted?
  end

  def test_an_exception_writes_nothing_and_releases_the_locks
    a1 = @account.find("a1")

    assert_raises(RuntimeError) do
      @account.transaction(lock: [a1]) do
        a1.update!(balance: 0)
        @account.create!(_id: "a3", name: "Caio", balance: 1)

        raise "gateway down"
      end
    end

    assert_raises(Ndb::RecordInvalid) do
      @account.transaction do
        a1.lock!
        @account.create!(_id: "a4", name: nil)
      end
    end

    assert_equal [100, 0], balances
    assert_raises(Ndb::NotFound) { @account.find("a3") }
    assert_predicate @account.lock(wait: 0).find("a1").unlock!, :persisted?
  end

  def test_create_update_destroy_and_increment_in_one_step
    a2 = @account.find("a2")

    @account.transaction do
      @account.create!(name: "Caio", balance: 5)
      @account.find("a1").destroy!
      a2.increment!(:balance, 7)
      a2.update!(name: "Bia Lima")
    end

    assert_raises(Ndb::NotFound) { @account.find("a1") }
    assert_equal ["Bia Lima", 7], @account.find("a2").then { |a| [a.name, a.balance] }
    assert_equal 1, @account.where(name: "Caio").count
    assert_equal @account.find("a2").version, a2.version
  end

  def test_a_stale_record_fails_the_whole_transaction
    stale = @account.find("a1")
    @account.find("a1").update!(balance: 1)

    assert_raises(Ndb::TxConflict) do
      @account.transaction do
        @account.find("a2").update!(balance: 99)
        stale.update!(balance: 0)
      end
    end

    assert_equal [1, 0], balances
  end

  def test_another_collection_is_refused_before_anything_is_sent
    assert_raises(Ndb::TransactionError) do
      @account.transaction do
        @account.find("a1").update!(balance: 0)
        @transfer.create!(amount: 1)
      end
    end

    assert_equal [100, 0], balances
    assert_equal 0, @transfer.count
  end

  def test_a_nested_block_joins_the_outer_one
    @account.transaction do
      @account.find("a1").update!(balance: 1)

      @account.transaction { @account.find("a2").update!(balance: 2) }

      assert_equal [100, 0], balances
    end

    assert_equal [1, 2], balances
  end
end

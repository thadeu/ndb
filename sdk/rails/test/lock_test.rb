# frozen_string_literal: true

require "test_helper"

class LockTest < Minitest::Test
  def setup
    @wallet = model do
      attribute :balance_cents, :integer
      attribute :status, :string
    end
  end

  def test_with_lock_reads_again_after_a_conflict
    @wallet.create!(_id: "w1", balance_cents: 1000)
    calls = 0

    wallet = @wallet.find("w1")

    wallet.with_lock do |w|
      calls += 1
      @wallet.find("w1").decrement!(:balance_cents, 300) if calls == 1

      w.balance_cents -= 500
    end

    assert_equal 2, calls
    assert_equal 200, wallet.balance_cents
    assert_equal 200, @wallet.find("w1").balance_cents
  end

  def test_concurrent_debits_all_land
    @wallet.create!(_id: "w1", balance_cents: 1000)

    5.times.map do
      Thread.new { @wallet.find("w1").with_lock(retries: 20) { |w| w.balance_cents -= 100 } }
    end.each(&:join)

    assert_equal 500, @wallet.find("w1").balance_cents
  end

  def test_the_second_webhook_sees_paid
    @wallet.create!(_id: "p1", status: "pending")
    first = @wallet.find("p1")
    second = @wallet.find("p1")
    writes = 0

    [first, second].each do |payment|
      payment.with_lock do |p|
        next if p.status == "paid"

        writes += 1
        p.status = "paid"
      end
    end

    assert_equal 1, writes
    assert_equal "paid", second.status
  end

  def test_an_exception_writes_nothing_and_gives_up_after_retries
    @wallet.create!(_id: "w1", balance_cents: 100)

    assert_raises(RuntimeError) { @wallet.find("w1").with_lock { raise "insufficient funds" } }
    assert_equal 100, @wallet.find("w1").balance_cents

    calls = 0

    assert_raises(Ndb::StaleObjectError) do
      @wallet.find("w1").with_lock(retries: 3) do |w|
        calls += 1
        @wallet.find("w1").increment!(:balance_cents)
        w.balance_cents = 0
      end
    end

    assert_equal 3, calls
  end
end

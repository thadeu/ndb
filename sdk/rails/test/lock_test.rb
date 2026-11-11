# frozen_string_literal: true

require "test_helper"

class LockTest < Minitest::Test
  def setup
    @payment = model do
      attribute :status, :string
      attribute :amount_cents, :integer
    end

    @payment.create!(_id: "p1", status: "pending", amount_cents: 500)
  end

  def test_lock_find_with_a_block_runs_one_holder_at_a_time
    refunds = 0
    inside = 0
    worst = 0
    mutex = Mutex.new

    4.times.map do
      Thread.new do
        @payment.lock.find("p1") do |p|
          mutex.synchronize { worst = [worst, inside += 1].max }

          unless p.status == "refunded"
            sleep 0.01
            mutex.synchronize { refunds += 1 }
            p.update!(status: "refunded")
          end

          mutex.synchronize { inside -= 1 }
        end
      end
    end.each(&:join)

    assert_equal 1, worst
    assert_equal 1, refunds
    assert_equal "refunded", @payment.find("p1").status
  end

  def test_lock_find_without_a_block_holds_until_the_save
    payment = @payment.lock.find("p1")

    assert_predicate payment, :locked?
    assert_raises(Ndb::Locked) { @payment.lock(wait: 0).find("p1") }

    payment.update!(status: "paid")

    refute_predicate payment, :locked?
    assert_equal "paid", @payment.lock(wait: 0).find("p1").unlock!.status
  end

  def test_lock_bang_reads_again_and_unlock_releases
    payment = @payment.find("p1")
    @payment.find("p1").update!(status: "paid")

    payment.lock!

    assert_equal "paid", payment.status
    assert_raises(Ndb::Locked) { @payment.find("p1").lock!(wait: 0) }

    payment.unlock!
    payment.unlock!

    assert_predicate @payment.find("p1").lock!(wait: 0), :locked?
  end

  def test_with_lock_keeps_the_lock_until_the_end_of_the_block
    payment = @payment.find("p1")

    payment.with_lock do |p|
      p.update!(status: "paid")

      assert_predicate p, :locked?
      assert_raises(Ndb::Locked) { @payment.lock(wait: 0).find("p1") }

      p.update!(amount_cents: 600)
    end

    refute_predicate payment, :locked?
    assert_equal ["paid", 600], @payment.find("p1").then { |p| [p.status, p.amount_cents] }
  end

  def test_an_exception_releases_and_other_writes_wait
    assert_raises(RuntimeError) { @payment.lock.find("p1") { raise "gateway down" } }

    held = @payment.lock(wait: 0).find("p1")
    done = false
    writer = Thread.new { @payment.find("p1").increment!(:amount_cents) and done = true }

    sleep 0.05

    refute done

    held.unlock!
    writer.join

    assert_equal 501, @payment.find("p1").amount_cents
  end

  def test_find_by_and_first_lock_and_missing_records
    assert_predicate @payment.lock.find_by(status: "pending"), :locked?
    assert_raises(Ndb::Locked) { @payment.where(status: "pending").lock(wait: 0).first }
    assert_nil @payment.lock.find_by(status: "nope")
    assert_raises(Ndb::NotFound) { @payment.lock.find("nope") }
    assert_raises(ArgumentError) { @payment.lock.find("p1", "p2") }
  end

  def test_a_save_after_the_ttl_is_refused
    payment = @payment.lock(ttl: 0.1).find("p1")

    sleep 0.15

    assert_raises(Ndb::LockExpired) { payment.update!(status: "paid") }
    assert_equal "pending", @payment.find("p1").status
    refute_predicate payment, :locked?

    payment.update!(status: "paid")

    assert_equal "paid", @payment.find("p1").status
  end
end

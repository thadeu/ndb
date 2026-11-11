# frozen_string_literal: true

require "test_helper"

class LockTest < Minitest::Test
  def setup
    @wallets = client.collection(NdbTestHelpers.next_name)
    @wallets.insert(_id: "w1", balance_cents: 1000)
  end

  def test_one_holder_at_a_time_each_once
    inside = 0
    worst = 0
    calls = 0
    mutex = Mutex.new

    5.times.map do
      Thread.new do
        @wallets.lock("w1") do |w|
          mutex.synchronize { worst = [worst, inside += 1].max and calls += 1 }
          sleep 0.005

          w["balance_cents"] -= 100
          mutex.synchronize { inside -= 1 }
        end
      end
    end.each(&:join)

    assert_equal 1, worst
    assert_equal 5, calls
    assert_equal 500, @wallets.get("w1")["balance_cents"]
  end

  def test_an_exception_writes_nothing_and_releases
    error = assert_raises(RuntimeError) do
      @wallets.lock("w1") do |w|
        w["balance_cents"] = 0

        assert_raises(Ndb::Locked) { @wallets.lock("w1", wait: 0) { nil } }

        raise "insufficient funds"
      end
    end

    assert_equal "insufficient funds", error.message
    assert_equal 1000, @wallets.get("w1")["balance_cents"]

    @wallets.lock("w1", wait: 0) { nil }

    assert_raises(Ndb::NotFound) { @wallets.lock("nope") { nil } }
  end

  def test_a_write_without_the_token_waits
    held = client.lock(@wallets.name, "w1")
    done = false
    writer = Thread.new { @wallets.update("w1", { "$inc" => { balance_cents: 1 } }) and done = true }

    sleep 0.05

    refute done

    @wallets.update("w1", { "$inc" => { balance_cents: 10 } }, lock: held["token"])
    client.unlock(@wallets.name, "w1", held["token"])
    writer.join

    assert_equal 1011, @wallets.get("w1")["balance_cents"]
  end

  def test_an_expired_lock_refuses_the_write
    assert_raises(Ndb::LockExpired) do
      @wallets.lock("w1", ttl: 0.1) do |w|
        sleep 0.15

        w["balance_cents"] = 1
      end
    end

    assert_equal 1000, @wallets.get("w1")["balance_cents"]
  end
end

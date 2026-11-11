# frozen_string_literal: true

require "test_helper"

class LockTest < Minitest::Test
  def setup
    @wallets = client.collection(NdbTestHelpers.next_name)
  end

  def test_reads_again_after_a_conflict
    @wallets.insert(_id: "w1", balance_cents: 1000, note: "x")
    calls = 0

    doc = @wallets.with_lock("w1") do |w|
      calls += 1
      @wallets.update("w1", { "$inc" => { balance_cents: -300 } }) if calls == 1

      w["balance_cents"] -= 500
      w.delete("note")
    end

    assert_equal 2, calls
    assert_equal 200, doc["balance_cents"]
    refute doc.key?("note")
    assert_equal 200, @wallets.get("w1")["balance_cents"]
  end

  def test_concurrent_debits_all_land
    @wallets.insert(_id: "w1", balance_cents: 1000)

    5.times.map do
      Thread.new { @wallets.with_lock("w1", retries: 20) { |w| w["balance_cents"] -= 100 } }
    end.each(&:join)

    assert_equal 500, @wallets.get("w1")["balance_cents"]
  end

  def test_an_error_in_the_block_writes_nothing
    @wallets.insert(_id: "w1", balance_cents: 100)

    error = assert_raises(RuntimeError) do
      @wallets.with_lock("w1") do |w|
        raise "insufficient funds" if w["balance_cents"] < 500

        w["balance_cents"] -= 500
      end
    end

    assert_equal "insufficient funds", error.message
    assert_equal 1, @wallets.get("w1")["_v"]
    assert_raises(Ndb::NotFound) { @wallets.with_lock("nope") { nil } }
  end

  def test_gives_up_with_stale_object_error
    @wallets.insert(_id: "w1", balance_cents: 100)
    calls = 0

    assert_raises(Ndb::StaleObjectError) do
      @wallets.with_lock("w1", retries: 3) do |w|
        calls += 1
        @wallets.update("w1", { "$inc" => { balance_cents: 1 } })
        w["balance_cents"] = 0
      end
    end

    assert_equal 3, calls
  end

  def test_no_change_no_write
    @wallets.insert(_id: "w1", balance_cents: 100)
    @wallets.with_lock("w1") { nil }

    assert_equal 1, @wallets.get("w1")["_v"]
  end
end

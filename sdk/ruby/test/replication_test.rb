# frozen_string_literal: true

require "test_helper"

# A copy forwards a write to its owner and answers after it has applied
# it, so the next read on the copy sees the write.
class ReplicationTest < Minitest::Test
  KEY = { "NDB_ENCRYPTION_KEY" => "cd" * 32 }.freeze

  def self.owner = (@owner ||= TcpDaemon.new(env: KEY))
  def self.copy = (@copy ||= TcpDaemon.new(env: KEY.merge("NDB_REPLICA_OF" => owner.url)))

  def test_read_after_a_write_through_a_copy
    coll = NdbTestHelpers.next_name
    db = Ndb::Client.new(url: "unix://#{self.class.copy.socket}")

    10.times do |i|
      db.insert(coll, { _id: "d#{i}", n: i })

      assert_equal i, db.get(coll, "d#{i}")["n"]
    end

    owner = Ndb::Client.new(url: self.class.owner.url)

    assert_equal 10, owner.count(coll)
  end
end

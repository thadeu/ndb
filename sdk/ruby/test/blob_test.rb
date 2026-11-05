# frozen_string_literal: true

require "test_helper"
require "stringio"
require "securerandom"
require "digest"

class BlobTest < Minitest::Test
  def test_put_and_get_round_trip
    stored = client.put_blob(StringIO.new("RIFF-fake-wav"), "audio/wav")

    assert_match(/\Asha256:\h{64}\z/, stored["$blob"])
    assert_equal 13, stored["size"]
    assert_equal "audio/wav", stored["type"]
    assert_equal "RIFF-fake-wav", client.get_blob(stored["$blob"])
  end

  def test_range_read
    stored = client.put_blob(StringIO.new("0123456789"))

    assert_equal "2345", client.get_blob(stored["$blob"], from: 2, to: 5)
  end

  def test_large_upload_is_streamed
    data = SecureRandom.random_bytes((2.5 * 1024 * 1024).to_i)
    stored = client.put_blob(StringIO.new(data))

    assert_equal Digest::SHA256.hexdigest(data), client.get_blob(stored["$blob"]).then { |bytes| Digest::SHA256.hexdigest(bytes) }
  end

  def test_same_content_has_the_same_reference
    a = client.put_blob(StringIO.new("same"))
    b = client.put_blob(StringIO.new("same"))

    assert_equal a["$blob"], b["$blob"]
  end
end

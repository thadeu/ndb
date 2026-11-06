# frozen_string_literal: true

require "test_helper"
require "stringio"
require "securerandom"
require "tempfile"
require "digest"

class BlobTest < Minitest::Test
  def call_model
    model do
      attribute :recording, :blob
    end
  end

  def test_upload_on_save_and_read_back
    calls = call_model
    call = calls.new(_id: "c1", recording: StringIO.new("RIFF-fake-wav"))

    refute call.recording.uploaded?

    call.save!

    assert call.recording.uploaded?
    assert_match(/\Asha256:\h{64}\z/, call.recording.ref)
    assert_equal 13, call.recording.size

    loaded = calls.find("c1")

    assert_equal call.recording.ref, loaded.recording.ref
    assert_equal "RIFF-fake-wav", loaded.recording.read
  end

  def test_read_is_io_like
    calls = call_model
    calls.create!(_id: "c1", recording: StringIO.new("0123456789"))

    blob = calls.find("c1").recording

    assert_equal "0123", blob.read(4)
    assert_equal "4567", blob.read(4)
    assert_equal "89", blob.read(4)
    assert_nil blob.read(4)
    assert_predicate blob, :eof?

    blob.rewind

    assert_equal "0123456789", blob.read
  end

  def test_large_file_round_trip_is_chunked
    data = SecureRandom.random_bytes((2.5 * 1024 * 1024).to_i)
    file = Tempfile.new("blob", binmode: true)
    file.write(data)
    file.rewind

    calls = call_model
    calls.create!(_id: "big", recording: file)

    digest = Digest::SHA256.new
    chunks = 0

    calls.find("big").recording.each_chunk do |chunk|
      digest << chunk
      chunks += 1
    end

    assert_equal Digest::SHA256.hexdigest(data), digest.hexdigest
    assert_operator chunks, :>=, 3
  end

  def test_replacing_the_blob_updates_the_reference
    calls = call_model
    call = calls.create!(_id: "c1", recording: StringIO.new("one"))
    first = call.recording.ref

    call.update!(recording: StringIO.new("two"))

    assert_equal "two", calls.find("c1").recording.read
    refute_equal first, calls.find("c1").recording.ref
  end

  def test_same_content_has_the_same_reference
    calls = call_model
    a = calls.create!(_id: "a", recording: StringIO.new("same"))
    b = calls.create!(_id: "b", recording: StringIO.new("same"))

    assert_equal a.recording.ref, b.recording.ref
  end

  def test_nil_blob_stays_out_of_the_document
    calls = call_model
    calls.create!(_id: "c1")

    assert_nil calls.find("c1").recording
  end
end

# frozen_string_literal: true

require "test_helper"

class PatternTest < Minitest::Test
  def setup
    base = "#{NdbTestHelpers.next_name}-logs-"

    client.insert("#{base}1", [{ _id: "a1", lvl: "info" }, { _id: "a2", lvl: "error" }])
    client.insert("#{base}2", [{ _id: "b1", lvl: "error" }])

    @logs = model("#{base}*") do
      attribute :lvl, :string
    end

    @base = base
  end

  def test_a_pattern_model_reads_every_collection
    rows = @logs.where(lvl: "error").order(:_id).to_a

    assert_equal %w[a2 b1], rows.map(&:id)
    assert_equal ["#{@base}1", "#{@base}2"], rows.map(&:source_collection)
    assert_equal 3, @logs.count
  end

  def test_a_pattern_model_does_not_write
    err = assert_raises(Ndb::Error) { @logs.create!(lvl: "x") }

    assert_equal "pattern_read_only", err.code
  end
end

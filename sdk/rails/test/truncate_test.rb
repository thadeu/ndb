# frozen_string_literal: true

require "test_helper"

class TruncateTest < Minitest::Test
  def setup
    @post = model do
      attribute :status, :string, index: true
    end
  end

  def test_truncate_bang_removes_every_record_and_keeps_the_model_working
    3.times { |i| @post.create!(_id: "p#{i}", status: "open") }

    assert @post.truncate!
    assert_equal 0, @post.count

    @post.create!(_id: "p9", status: "open")

    assert_equal ["p9"], @post.where(status: "open").map(&:id)
  end

  def test_drop_bang_removes_the_collection_and_the_next_use_declares_it_again
    @post.create!(_id: "p1", status: "open")

    assert @post.drop!
    assert_equal 0, @post.count

    @post.create!(_id: "p2", status: "open")

    assert_equal ["p2"], @post.where(status: "open").map(&:id)
  end
end

# frozen_string_literal: true

require "test_helper"

# Puma-like load: many threads write and read through the same models.
class ConcurrencyTest < Minitest::Test
  def test_threads_create_and_query_without_losing_writes
    post = model do
      primary_key :post_id

      attribute :n, :integer
    end

    threads = 8.times.map do |t|
      Thread.new do
        20.times { |i| post.create!(post_id: "t#{t}-#{i}", n: i) }
        post.where(n: 0..5).count
      end
    end

    threads.each(&:join)

    assert_equal 160, post.count
    assert_equal 8 * 6, post.where(n: 0..5).count
  end
end

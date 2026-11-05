# frozen_string_literal: true

require "test_helper"

# Runs against POST /v1/c/{coll}/aggregate (milestone M06). The tests skip
# while the daemon answers 404/405 for that route.
class AggregateTest < Minitest::Test
  def setup
    @post = model do
      primary_key :post_id

      attribute :status, :string
      attribute :duration, :integer
    end

    @post.create!([
                    { post_id: "a", status: "answered", duration: 10 },
                    { post_id: "b", status: "answered", duration: 30 },
                    { post_id: "c", status: "failed", duration: 5 }
                  ])

    skip "daemon has no /aggregate endpoint yet (M06): #{probe}" if probe
  end

  def test_group_count
    assert_equal({ "answered" => 2, "failed" => 1 }, @post.group(:status).count)
  end

  def test_group_sum_average_min_max
    assert_equal({ "answered" => 40, "failed" => 5 }, @post.group(:status).sum(:duration))
    assert_equal({ "answered" => 20.0, "failed" => 5.0 }, @post.group(:status).average(:duration))
    assert_equal({ "answered" => 10, "failed" => 5 }, @post.group(:status).minimum(:duration))
    assert_equal({ "answered" => 30, "failed" => 5 }, @post.group(:status).maximum(:duration))
  end

  def test_where_applies_before_the_group
    assert_equal({ "answered" => 2 }, @post.where(status: "answered").group(:status).count)
  end

  def test_ungrouped_aggregates
    assert_equal 45, @post.sum(:duration)
    assert_equal 30, @post.maximum(:duration)
  end

  private

  # Returns a reason when the route does not exist, nil when it answers.
  def probe
    @probe ||= begin
      client.aggregate(@post.collection_name, pipeline: [])

      false
    rescue Ndb::Error => e
      e.code == "http_status" && [404, 405].include?(e.status) ? "HTTP #{e.status}" : false
    end || nil
  end
end

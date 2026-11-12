# frozen_string_literal: true

require "test_helper"

class ScopeTest < Minitest::Test
  def setup
    @post = model do
      attribute :status, :string, index: true
      attribute :n, :integer

      scope :active, -> { where(status: "open") }
      scope :top, ->(count) { order(n: :desc).limit(count) }

      def self.big
        where(n: 10..)
      end
    end

    @post.create!(id: "a", status: "open", n: 1)
    @post.create!(id: "b", status: "open", n: 20)
    @post.create!(id: "c", status: "done", n: 30)
  end

  def test_a_scope_on_the_model
    assert_equal %w[a b], @post.active.order(:_id).pluck(:_id)
  end

  def test_a_scope_with_arguments
    assert_equal %w[c b], @post.top(2).pluck(:_id)
  end

  def test_scopes_and_class_methods_chain_on_a_relation
    assert_equal ["b"], @post.active.big.pluck(:_id)
    assert_equal ["b"], @post.big.active.pluck(:_id)
    assert_equal ["b"], @post.where(n: 1..).active.top(1).pluck(:_id)
  end

  def test_the_scope_ends_with_the_call
    @post.active.big.to_a

    assert_equal 3, @post.count
  end

  def test_a_relation_does_not_run_the_methods_of_ndb_record
    assert_raises(NoMethodError) { @post.active.truncate! }
    assert_equal 3, @post.count
  end

  def test_a_scope_name_that_a_relation_has_is_refused
    error = assert_raises(ArgumentError) { @post.scope(:open, -> { all }) }

    assert_match(/open/, error.message)
    assert_raises(ArgumentError) { @post.scope(:count, -> { all }) }
  end

  def test_respond_to
    assert_respond_to @post.active, :big
    refute_respond_to @post.active, :truncate!
  end
end

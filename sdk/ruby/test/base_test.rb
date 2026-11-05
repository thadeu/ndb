# frozen_string_literal: true

require "test_helper"

class ApplicationNdbRecord < NdbRecord::Base
  self.abstract_class = true
end

class Post < ApplicationNdbRecord
  primary_key :call_id

  attribute :status, :string, index: true
  attribute :dnis, :string, unique: true
  attribute :duration, :integer
  attribute :created_at, :datetime
end

class Comment < ApplicationNdbRecord; end

class BaseTest < Minitest::Test
  def test_collection_name_follows_rails_naming
    assert_equal "posts", Post.collection_name
    assert_equal "comments", Comment.collection_name
  end

  def test_collection_name_can_be_overridden
    klass = Class.new(Post)
    klass.collection_name = "calls"

    assert_equal "calls", klass.collection_name
  end

  def test_abstract_class_is_not_inherited
    assert_predicate ApplicationNdbRecord, :abstract_class?
    refute_predicate Post, :abstract_class?
  end

  def test_primary_key
    assert_equal "call_id", Post.primary_key
    assert_equal "_id", Comment.primary_key
    assert_includes Post.attribute_names, "call_id"
  end

  def test_collection_definition
    assert_equal({ "id" => "call_id", "indexes" => ["status"], "unique" => ["dnis"] }, Post.collection_definition)
  end

  def test_time_option_sets_the_collection_time_field
    klass = Class.new(Post) { attribute :ts, :datetime, time: true }

    assert_equal "ts", klass.collection_definition["time"]
    assert_nil Post.collection_definition["time"]
  end

  def test_source_makes_a_direct_collection
    klass = Class.new(Post) { source "/data/calls/**/*.ndjson" }

    assert_equal "direct", klass.collection_definition["write"]
    assert_equal "/data/calls/**/*.ndjson", klass.collection_definition["path"]
  end

  def test_attribute_casting_and_active_model_api
    post = Post.new(call_id: "c1", duration: "42", created_at: "2026-11-01T10:00:00Z")

    assert_equal 42, post.duration
    assert_kind_of Time, post.created_at
    assert_equal "post", Post.model_name.param_key
    assert_nil post.to_key
    assert_equal "c1", post.id
  end

  def test_as_json_hides_the_unused_generated_id
    json = Post.new(call_id: "c1", status: "x").as_json

    assert_equal "c1", json["call_id"]
    refute json.key?("_id")
  end

  def test_ensure_collection_declares_the_definition
    klass = model do
      primary_key :cid
      attribute :a, :string, index: true
    end

    klass.ensure_collection!

    stored = client.collections.find { |c| c["name"] == klass.collection_name }

    assert_equal "cid", stored["id"]
    assert_equal ["a"], stored["indexes"]
  end
end

# frozen_string_literal: true

require "test_helper"

class FindersTest < Minitest::Test
  def setup
    @user = model do
      attribute :email, :string, unique: true
      attribute :name, :string
      attribute :role, :string, index: true
      attribute :logins, :integer

      validates :email, presence: true
    end
  end

  def test_find_by_attribute
    @user.create!(_id: "u1", email: "a@x.io", name: "Ana")

    assert_equal "u1", @user.find_by_email("a@x.io").id
    assert_nil @user.find_by_email("none@x.io")
    assert_equal "u1", @user.find_by_id("u1").id
    assert_equal "u1", @user.find_by__id("u1").id
  end

  def test_find_by_two_attributes
    @user.create!(_id: "u1", email: "a@x.io", name: "Ana")

    assert_equal "u1", @user.find_by_email_and_name("a@x.io", "Ana").id
    assert_nil @user.find_by_email_and_name("a@x.io", "Bob")
  end

  def test_find_by_bang_raises_not_found
    assert_raises(Ndb::NotFound) { @user.find_by_email!("none@x.io") }

    @user.create!(_id: "u1", email: "a@x.io")

    assert_equal "u1", @user.find_by_email!("a@x.io").id
  end

  def test_unknown_attribute_and_wrong_arity
    assert_raises(NoMethodError) { @user.find_by_nope("x") }
    assert_raises(ArgumentError) { @user.find_by_email }
    assert_raises(ArgumentError) { @user.find_by_email_and_name("a") }

    assert_respond_to @user, :find_by_email
    refute_respond_to @user, :find_by_nope
  end

  def test_finder_is_defined_on_first_use_and_works_on_relations
    @user.create!(_id: "u1", email: "a@x.io", role: "admin")
    @user.create!(_id: "u2", email: "b@x.io", role: "guest")

    refute_includes @user.singleton_methods, :find_by_email

    @user.find_by_email("a@x.io")

    assert_includes @user.singleton_methods, :find_by_email
    assert_equal "u1", @user.where(role: "admin").find_by_email("a@x.io").id
    assert_nil @user.where(role: "guest").find_by_email("a@x.io")
    assert_raises(Ndb::NotFound) { @user.where(role: "guest").find_by_email!("a@x.io") }
    assert_raises(NoMethodError) { @user.where(role: "guest").find_by_nope("x") }
  end

  def test_find_or_initialize_by
    @user.create!(_id: "u1", email: "a@x.io")

    assert_equal "u1", @user.find_or_initialize_by(email: "a@x.io").id

    fresh = @user.find_or_initialize_by(email: "b@x.io") { |u| u.name = "Bia" }

    refute_predicate fresh, :persisted?
    assert_equal "Bia", fresh.name
    assert_equal 1, @user.count
  end

  def test_find_or_create_by_creates_once
    first = @user.find_or_create_by(email: "a@x.io") { |u| u.name = "Ana" }

    assert_predicate first, :persisted?
    assert_equal 36, first.id.length
    assert_equal "Ana", first.name

    again = @user.find_or_create_by(email: "a@x.io") { |u| u.name = "Other" }

    assert_equal first.id, again.id
    assert_equal "Ana", again.name
    assert_equal 1, @user.count
  end

  def test_find_or_create_by_returns_the_invalid_record
    user = @user.find_or_create_by(name: "Ana")

    refute_predicate user, :persisted?
    assert_includes user.errors[:email], "can't be blank"
    assert_equal 0, @user.count
  end

  def test_find_or_create_by_bang_raises_when_invalid
    assert_raises(Ndb::RecordInvalid) { @user.find_or_create_by!(name: "Ana") }

    assert_equal "a@x.io", @user.find_or_create_by!(email: "a@x.io").email
  end

  def test_find_or_create_by_runs_the_callbacks
    log = []
    klass = model do
      attribute :email, :string

      before_create { log << :before_create }
      after_save { log << :after_save }
    end

    klass.find_or_create_by(email: "a@x.io")
    klass.find_or_create_by(email: "a@x.io")

    assert_equal %i[before_create after_save], log
  end

  def test_find_or_create_by_does_not_overwrite_a_racing_writer
    @user.create!(_id: "u1", email: "a@x.io", name: "Ana")

    record = @user.new(email: "a@x.io", name: "Loser")

    assert_equal "Ana", record.create_unless_exists(email: "a@x.io").name
    assert_equal "Ana", @user.find("u1").name
  end

  def test_concurrent_find_or_create_by_makes_one_document
    results = 8.times.map do |i|
      Thread.new { @user.find_or_create_by(email: "race@x.io") { |u| u.name = "t#{i}" } }
    end.map(&:value)

    assert_equal 1, @user.count
    assert_equal 1, results.map(&:id).uniq.size
    assert_equal @user.find_by_email("race@x.io").name, @user.find_by_email("race@x.io").name
  end
end

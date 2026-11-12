# frozen_string_literal: true

require "test_helper"

class DependentKey < Ndb::Record
  self.collection_name = NdbTestHelpers.next_name

  attribute :dependent_app_id, :string
end

class DependentDomain < Ndb::Record
  self.collection_name = NdbTestHelpers.next_name

  attribute :dependent_app_id, :string

  after_destroy { DependentDomain.destroyed << id }

  def self.destroyed
    @destroyed ||= []
  end
end

class DependentPlan < Ndb::Record
  self.collection_name = NdbTestHelpers.next_name

  attribute :dependent_app_id, :string
end

class DependentApp < Ndb::Record
  self.collection_name = NdbTestHelpers.next_name

  has_many :dependent_domains, dependent: :destroy
  has_many :dependent_keys, dependent: :delete_all
  has_one :dependent_plan, dependent: :destroy
end

class DependentTest < Minitest::Test
  def setup
    DependentApp.create!(id: "a1")
    DependentApp.create!(id: "a2")
    DependentDomain.create!([{ id: "d1", dependent_app_id: "a1" }, { id: "d2", dependent_app_id: "a2" }])
    DependentKey.create!([{ id: "k1", dependent_app_id: "a1" }, { id: "k2", dependent_app_id: "a1" }, { id: "k3", dependent_app_id: "a2" }])
    DependentPlan.create!(id: "p1", dependent_app_id: "a1")
    DependentDomain.destroyed.clear
  end

  def teardown
    [DependentApp, DependentDomain, DependentKey, DependentPlan].each(&:truncate!)
  end

  def test_destroy_removes_the_children
    DependentApp.find("a1").destroy

    assert_equal ["d2"], DependentDomain.pluck(:_id)
    assert_equal ["k3"], DependentKey.pluck(:_id)
    assert_equal 0, DependentPlan.count
    assert_equal ["d1"], DependentDomain.destroyed
  end

  def test_an_unknown_dependent_is_refused
    assert_raises(ArgumentError) do
      Class.new(Ndb::Record) { has_many :things, dependent: :nullify_all }
    end
  end
end

class ReadOnlyTest < Minitest::Test
  def setup
    @event = model do
      attribute :kind, :string

      def readonly? = persisted?
    end
  end

  def test_a_readonly_record_is_not_saved_or_destroyed
    event = @event.create!(id: "e1", kind: "sent")

    event.kind = "bounced"

    assert_raises(Ndb::ReadOnlyRecord) { event.save }
    assert_raises(Ndb::ReadOnlyRecord) { event.destroy }
    assert_raises(Ndb::ReadOnlyRecord) { event.delete }
    assert_equal "sent", @event.find("e1").kind
  end

  def test_readonly_bang
    plain = model { attribute :kind, :string }
    event = plain.new(kind: "x")

    event.readonly!

    assert_raises(Ndb::ReadOnlyRecord) { event.save }
    assert_equal 0, plain.count
  end
end

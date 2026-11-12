# frozen_string_literal: true

require "test_helper"

class FkKey < Ndb::Record
  self.collection_name = NdbTestHelpers.next_name

  attribute :fk_app_id, :string
end

class FkApp < Ndb::Record
  self.collection_name = NdbTestHelpers.next_name

  has_many :fk_keys
end

# has_many indexes the foreign key on the other side. The index must be
# there on the first use of that model, not only after a lookup.
class ForeignKeyIndexTest < Minitest::Test
  def teardown
    [FkKey, FkApp].each(&:truncate!)
  end

  def test_the_child_model_has_the_index_from_its_first_use
    FkKey.create!(id: "k1", fk_app_id: "a1")

    assert_equal 1, FkKey.where(fk_app_id: "a1").delete_all
  end

  def test_the_association_can_write_many
    FkApp.create!(id: "a1")
    FkKey.create!(id: "k1", fk_app_id: "a1")

    assert_equal 1, FkApp.find("a1").fk_keys.update("$set" => { "fk_app_id" => "a2" })
  end
end

# frozen_string_literal: true

require "test_helper"

# Every Ndb::Record has a GlobalID, as an ActiveRecord model: turbo-rails
# names the stream of a record by it, and Active Job passes a record to a
# job by it.
class GlobalIdTest < Minitest::Test
  # A named class: the locator finds the model by its name.
  class Note < Ndb::Record
    self.collection_name = "gid_#{NdbTestHelpers.next_name}"

    attribute :title, :string
  end

  # The globalid railtie sets the app name from the Rails app.
  def setup
    GlobalID.app = "ndb-test"
  end

  def test_a_record_has_a_global_id
    note = Note.create!(title: "a")

    assert_equal "gid://ndb-test/GlobalIdTest::Note/#{note.id}", note.to_global_id.to_s
    assert_equal note.to_global_id.to_param, note.to_gid_param
  end

  def test_the_locator_finds_the_record_with_a_generated_id
    note = Note.create!(title: "generated")

    found = GlobalID::Locator.locate(note.to_gid_param)

    assert_instance_of Note, found
    assert_equal [note.id, "generated"], [found.id, found.title]
  end

  def test_the_locator_finds_the_record_with_a_given_string_id
    note = Note.create!(id: "user@example.com/1 é", title: "given")

    found = GlobalID::Locator.locate(note.to_global_id)

    assert_equal [note.id, "given"], [found.id, found.title]
  end

  def test_locate_many
    one = Note.create!(title: "one")
    two = Note.create!(title: "two")

    found = GlobalID::Locator.locate_many([two.to_global_id, one.to_global_id])

    assert_equal %w[two one], found.map(&:title)
  end

  def test_locate_many_can_ignore_a_missing_record
    one = Note.create!(title: "one")
    gone = Note.create!(title: "gone")
    gid = gone.to_global_id

    gone.destroy

    found = GlobalID::Locator.locate_many([one.to_global_id, gid], ignore_missing: true)

    assert_equal ["one"], found.map(&:title)
  end

  def test_a_missing_record_raises_not_found
    gid = Note.create!(title: "x").tap(&:destroy).to_global_id

    assert_raises(Ndb::NotFound) { GlobalID::Locator.locate(gid) }
  end

  def test_a_new_record_has_no_global_id
    assert_raises(URI::GID::MissingModelIdError) { Note.new.to_global_id }
  end
end

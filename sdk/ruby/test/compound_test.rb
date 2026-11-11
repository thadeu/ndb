# frozen_string_literal: true

require "test_helper"

class CompoundTest < Minitest::Test
  def test_compound_index_and_unique_as_arrays_or_strings
    members = client.collection(NdbTestHelpers.next_name, indexes: [%w[acc n], "status"], unique: [%i[acc email]])

    members.insert(_id: "m1", acc: "a", email: "x@x", n: 1)
    members.insert(_id: "m2", acc: "b", email: "x@x", n: 2)

    assert_raises(Ndb::UniqueViolation) { members.insert(_id: "m3", acc: "a", email: "x@x") }

    indexes = members.ensure_index("email,acc")

    assert_includes indexes, "acc,n"
    assert_includes indexes, "status"
    assert_includes indexes, "email,acc"
  end
end

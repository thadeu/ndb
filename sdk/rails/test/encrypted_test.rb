# frozen_string_literal: true

require "test_helper"

class EncryptedTest < Minitest::Test
  def test_encrypted_and_blind_attributes
    person = model do
      attribute :cpf, :string, blind: :digits, unique: true
      attribute :pem, :string, encrypted: true
    end

    assert_equal ["pem"], person.collection_definition["encrypted"]
    assert_equal({"cpf" => "digits"}, person.collection_definition["blind"])

    person.create!(_id: "p1", cpf: "390.533.447-05", pem: "PRIVATE")

    assert_raises(Ndb::RecordNotUnique) { person.create!(_id: "p2", cpf: "39053344705") }

    found = person.find_by(cpf: "39053344705")

    assert_equal "390.533.447-05", found.cpf
    assert_equal "PRIVATE", found.pem
  end
end

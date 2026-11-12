# frozen_string_literal: true

require "test_helper"

class EncryptedTest < Minitest::Test
  def test_encrypted_and_blind_fields
    people = client.collection(NdbTestHelpers.next_name, encrypted: %w[pem], blind: {cpf: "numeric"}, unique: %w[cpf])

    people.insert(_id: "p1", cpf: "390.533.447-05", pem: "PRIVATE")

    assert_raises(Ndb::UniqueViolation) { people.insert(_id: "p2", cpf: "39053344705") }

    found = people.query(cpf: "39053344705").first

    assert_equal "390.533.447-05", found["cpf"]
    assert_equal "PRIVATE", found["pem"]
  end
end

class NoDecryptTest < Minitest::Test
  def test_no_decrypt_keeps_the_values_sealed
    vault = client.collection(NdbTestHelpers.next_name, encrypted: %w[secret])

    vault.insert(_id: "s1", secret: "PRIVATE")

    assert_match(/\Andb:e1:k1:/, vault.no_decrypt.query.first["secret"])
    assert_match(/\Andb:e1:k1:/, vault.no_decrypt.get("s1")["secret"])
    assert_equal "PRIVATE", vault.get("s1")["secret"]
  end
end

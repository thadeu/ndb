# frozen_string_literal: true

require "test_helper"

class EncryptedTest < Minitest::Test
  def test_encrypted_and_blind_attributes
    person = model do
      attribute :cpf, :string, blind: :numeric, unique: true
      attribute :pem, :string, encrypted: true
    end

    assert_equal ["pem"], person.collection_definition["encrypted"]
    assert_equal({"cpf" => "numeric"}, person.collection_definition["blind"])

    person.create!(_id: "p1", cpf: "390.533.447-05", pem: "PRIVATE")

    assert_raises(Ndb::RecordNotUnique) { person.create!(_id: "p2", cpf: "39053344705") }

    found = person.find_by(cpf: "39053344705")

    assert_equal "390.533.447-05", found.cpf
    assert_equal "PRIVATE", found.pem
  end
end

class EncryptedScopeTest < Minitest::Test
  def test_encrypted_keeps_the_values_sealed
    secret = model do
      attribute :owner, :string
      attribute :value, :string, encrypted: true
    end

    secret.create!(_id: "s1", owner: "u1", value: "PRIVATE")

    assert_match(/\Andb:e1:k1:/, secret.encrypted.where(owner: "u1").first.value)
    assert_match(/\Andb:e1:k1:/, secret.encrypted.find("s1").value)
    assert_equal "PRIVATE", secret.find("s1").value
  end
end

class BlindGroupTest < Minitest::Test
  def test_group_by_a_blind_attribute_gives_the_values
    order = model do
      attribute :cpf, :string, blind: :numeric
      attribute :total, :integer
    end

    order.create!(cpf: "390.533.447-05", total: 10)
    order.create!(cpf: "39053344705", total: 5)
    order.create!(cpf: "111.222.333-44", total: 7)

    sums = order.group(:cpf).sum(:total)

    assert_equal [5 + 10, 7], sums.values.sort.reverse
    assert(sums.keys.all? { |k| k.is_a?(String) && !k.start_with?("ndb:") })
  end
end

# frozen_string_literal: true

require "test_helper"

class SettingsTest < Minitest::Test
  def test_reads_the_key_masked_and_stores_a_change
    got = client.settings

    assert_includes got["encryption"]["key"], "****"
    assert_equal "k1", got["encryption"]["key_id"]

    put = client.put_settings(log: { slow_ms: 900 })

    assert_equal({ "value" => 900, "source" => "stored" }, put["log"]["slow_ms"])
    assert_raises(Ndb::Error) { client.put_settings(encryption: { key: "x" }) }
  end
end

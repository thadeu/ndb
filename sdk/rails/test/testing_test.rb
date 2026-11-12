# frozen_string_literal: true

require "test_helper"
require "ndb/rails/testing"

class TestingTest < Minitest::Test
  def setup
    @calls = model { attribute :n, :integer }
    @other = model { attribute :n, :integer }
    Ndb::Testing.clean!(only: [])
  end

  def teardown
    Ndb::Rails.configuration.collection_prefix = nil
    Ndb::Rails.reset_client!
  end

  def test_clean_truncates_what_the_tests_used
    @calls.create!(n: 1)
    @other.create!(n: 2)

    assert_includes Ndb::Testing.touched, @calls.collection_name

    cleaned = Ndb::Testing.clean!(except: [@other.collection_name])

    assert_includes cleaned, @calls.collection_name
    assert_equal 0, @calls.count
    assert_equal 1, @other.count
    assert_empty Ndb::Testing.touched - [@calls.collection_name, @other.collection_name]
  end

  def test_the_definition_and_indexes_stay
    indexed = model { attribute :status, :string, index: true }
    indexed.create!(status: "a")

    Ndb::Testing.clean!

    definition = client.collections.find { |d| d["name"] == indexed.collection_name }

    assert_equal %w[created_at status updated_at], definition["indexes"].sort
  end

  def test_refused_outside_the_test_environment
    with_env("RAILS_ENV" => "production", "RACK_ENV" => nil, "NDB_ALLOW_CLEAN" => nil) do
      ::Rails.stub(:env, ActiveSupport::EnvironmentInquirer.new("production")) do
        assert_raises(Ndb::Testing::UnsafeClean) { Ndb::Testing.clean! }
      end
    end
  end

  def test_a_worker_gets_collections_of_its_own
    Ndb::Testing.worker!(3)

    assert_equal "test3_", Ndb::Rails.configuration.collection_prefix
    assert @calls.collection_name.start_with?("test3_")

    @calls.create!(n: 1)

    assert_equal 1, @calls.count

    Ndb::Testing.worker!("")

    assert @calls.collection_name.start_with?("test1_")
    assert_equal 0, @calls.count
  end

  def test_clean_all_keeps_other_workers
    Ndb::Testing.worker!(7)
    @calls.create!(n: 1)
    mine = @calls.collection_name

    Ndb::Testing.worker!(8)
    @calls.create!(n: 2)

    Ndb::Testing.clean_all!

    assert_equal 0, @calls.count

    Ndb::Testing.worker!(7)

    assert_equal mine, @calls.collection_name
    assert_equal 1, @calls.count
  end

  def test_minitest_hooks_clean_after_each_test
    calls = @calls
    klass = Class.new(ActiveSupport::TestCase) do
      include Ndb::Testing::Minitest

      define_method(:test_writes) { calls.create!(n: 1) }
    end

    klass.new("test_writes").run

    assert_equal 0, calls.count

    klass.ndb_clean = false
    klass.new("test_writes").run

    assert_equal 1, calls.count
  end

  def test_database_cleaner_strategies
    require "ndb/rails/database_cleaner"

    @calls.create!(n: 1)
    DatabaseCleaner[:ndb].clean

    assert_equal 0, @calls.count

    @calls.create!(n: 1)
    @other.create!(n: 1)
    DatabaseCleaner[:ndb].clean_with(:truncation, only: [@other.collection_name])

    assert_equal 1, @calls.count
    assert_equal 0, @other.count
  end

  def test_rspec_hooks
    require "rspec/core"
    require "ndb/rails/rspec"

    calls = @calls

    group = RSpec.describe("ndb") do
      it("writes") { calls.create!(n: 1) }
      it("keeps", ndb_clean: false) { calls.create!(n: 2) }
    end

    group.run(RSpec::Core::NullReporter)

    assert_equal [2], calls.all.map(&:n)
  end

  private

  def with_env(vars)
    old = vars.keys.to_h { |k| [k, ENV.fetch(k, nil)] }
    vars.each { |k, v| v.nil? ? ENV.delete(k) : ENV[k] = v }

    yield
  ensure
    old.each { |k, v| v.nil? ? ENV.delete(k) : ENV[k] = v }
  end
end

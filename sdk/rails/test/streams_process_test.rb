# frozen_string_literal: true

require "streams_helper"
require "open3"
require "tmpdir"
require "ndb/streams/puma_plugin"

class StreamsProcessTest < Minitest::Test
  include StreamsTestHelpers

  EXE = File.expand_path("../../ruby/exe/ndb", __dir__)
  LIBS = [File.expand_path("../../ruby/lib", __dir__), File.expand_path("../lib", __dir__)].freeze

  # The boot of an app

  def test_the_boot_makes_the_groups_of_all_consumers
    writer = stream_class
    consumer = stream_class(writer.stream_name, group: :billing) { def perform(_event) = nil }

    thread = Ndb::Streams::Boot.start(nil)

    assert thread.join(10)
    assert thread.value
    assert_equal ["billing"], consumer.ndb_stream.groups.map { |row| row["name"] }
  end

  def test_the_boot_warns_and_does_not_raise_when_ndb_is_down
    stream_class(group: :billing) { def perform(_event) = nil }

    Ndb::Streams.client = Ndb::Client.new(socket: "/tmp/ndb-not-here-#{Process.pid}.sock", ready_timeout: 0)

    refute Ndb::Streams::Boot.run(nil, attempts: 2, backoff: 0.01)
    assert_match(/the groups were not made at boot/, @log.string)
  ensure
    Ndb::Streams.client = nil
  end

  def test_the_boot_warns_on_a_wrong_class
    stream_class { def perform(_event) = nil }

    refute Ndb::Streams::Boot.run(nil)
    assert_match(/the boot failed \(Ndb::Streams::MissingGroup/, @log.string)
  end

  def test_ndb_yml_has_a_streams_block
    Dir.mktmpdir do |dir|
      path = File.join(dir, "ndb.yml")

      File.write(path, "production:\n  streams:\n    concurrency: 4\n    only: [BillingPaid]\ntest:\n  timeout: 3\n")

      production = Ndb::Rails::Configuration.new.load_file(path, "production")
      test = Ndb::Rails::Configuration.new.load_file(path, "test")

      assert_equal({ "concurrency" => 4, "only" => ["BillingPaid"] }, production.streams)
      assert_equal({}, test.streams)
    end
  end

  # The Puma plugin

  def test_the_puma_plugin_is_on_in_development_only_by_default
    plugin = Ndb::Streams::PumaPlugin

    assert plugin.enabled?({})
    assert plugin.enabled?("RAILS_ENV" => "development")
    refute plugin.enabled?("RAILS_ENV" => "production")
    refute plugin.enabled?("RACK_ENV" => "test")
    assert plugin.enabled?("RAILS_ENV" => "production", "NDB_STREAMS_IN_PUMA" => "true")
    assert plugin.enabled?("RAILS_ENV" => "production", "NDB_STREAMS_IN_PUMA" => "1")
    refute plugin.enabled?("RAILS_ENV" => "development", "NDB_STREAMS_IN_PUMA" => "off")
    assert_raises(ArgumentError) { plugin.enabled?("NDB_STREAMS_IN_PUMA" => "maybe") }
  end

  def test_the_puma_plugin_runs_the_binstub_or_bundle_exec
    Dir.mktmpdir do |dir|
      assert_equal %w[bundle exec ndb streams], Ndb::Streams::PumaPlugin.command(dir)

      FileUtils.mkdir_p(File.join(dir, "bin"))
      File.write(File.join(dir, "bin", "ndb"), "")

      assert_equal [Gem.ruby, File.join(dir, "bin", "ndb"), "streams"], Ndb::Streams::PumaPlugin.command(dir)
    end
  end

  # The ndb command of the gem

  def test_another_command_goes_to_the_binary_of_ndb_bin
    Dir.mktmpdir do |dir|
      out, _, status = ndb(%w[serve -unix /tmp/x.sock], dir, "NDB_BIN" => fake_daemon(dir, "daemon"))

      assert status.success?
      assert_equal "daemon serve -unix /tmp/x.sock\n", out
    end
  end

  def test_another_command_goes_to_the_daemon_of_path_and_not_to_a_ruby_stub
    Dir.mktmpdir do |dir|
      stubs = File.join(dir, "stubs")
      real = File.join(dir, "real")

      FileUtils.mkdir_p([stubs, real])
      File.write(File.join(stubs, "ndb"), "#!/usr/bin/env ruby\nputs 'the stub'\n")
      File.symlink(EXE, File.join(dir, "ndb"))
      FileUtils.chmod(0o755, File.join(stubs, "ndb"))
      fake_daemon(real, "ndb")

      path = [dir, stubs, real, File.dirname(Gem.ruby), "/usr/bin", "/bin"].join(File::PATH_SEPARATOR)
      out, _, status = ndb(%w[-h], dir, "PATH" => path, "NDB_BIN" => nil)

      assert status.success?
      assert_equal "daemon -h\n", out
    end
  end

  def test_without_the_daemon_binary_it_says_how_to_install_it
    Dir.mktmpdir do |dir|
      path = [dir, "/usr/bin", "/bin"].join(File::PATH_SEPARATOR)
      _, err, status = ndb(%w[serve], dir, "PATH" => path, "NDB_BIN" => nil)

      assert_equal 127, status.exitstatus
      assert_match(%r{curl -fsSL https://ndb\.clowk\.in/install \| sh}, err)
    end
  end

  def test_streams_checks_its_arguments
    Dir.mktmpdir do |dir|
      out, _, status = ndb(%w[streams -h], dir)

      assert status.success?
      assert_match(/Usage: ndb streams/, out)
      assert_match(/--only A,B/, out)

      _, err, status = ndb(%w[streams --nope], dir)

      assert_equal 2, status.exitstatus
      assert_match(/invalid option: --nope/, err)

      _, err, status = ndb(%w[streams], dir)

      assert_equal 2, status.exitstatus
      assert_match(/use -r to load the stream classes/, err)

      _, err, status = ndb(%w[streams -c 0 -r x.rb], dir)

      assert_equal 2, status.exitstatus
      assert_match(/concurrency/, err)
    end
  end

  def test_streams_runs_the_consumers_of_a_file_without_rails_and_stops_on_sigterm
    name = NdbTestHelpers.next_name
    writer = stream_class(name)

    writer.append(n: 1)

    Dir.mktmpdir do |dir|
      out = File.join(dir, "out.txt")

      File.write(File.join(dir, "consumers.rb"), <<~RUBY)
        require "ndb/streams"

        class FileWriter
          include Ndb::Streams

          stream "#{name}", group: :files, start: :all

          def perform(event)
            File.write("#{out}", "n=\#{event[:n]} rails=\#{defined?(::Rails).inspect} threads=\#{ENV['SEEN_THREADS']}\\n", mode: "a")
          end
        end

        class Skipped
          include Ndb::Streams

          stream "#{name}", group: :skipped, start: :all

          def perform(event) = File.write("#{out}", "skipped\\n", mode: "a")
        end
      RUBY

      env = { "NDB_URL" => TestDaemon.socket, "NDB_CONSUMER" => "cli" }
      pid = Process.spawn(env, Gem.ruby, *LIBS.flat_map { |lib| ["-I", lib] }, EXE, "streams", "-r", "consumers.rb", "--only", "FileWriter", "-c", "2", chdir: dir, out: File.join(dir, "log"), err: File.join(dir, "log"))

      begin
        wait_until(20) { File.exist?(out) }

        assert_equal "n=1 rails=nil threads=\n", File.read(out)
        assert_equal %w[cli:0 cli:1], writer.ndb_stream.group("files").consumers.map { |row| row["name"] }.sort
        assert_equal ["files"], writer.ndb_stream.groups.map { |row| row["name"] }
      ensure
        Process.kill("TERM", pid)

        _, status = Process.wait2(pid)
      end

      assert status.success?, File.read(File.join(dir, "log"))
    end
  end

  private

  # Runs the command of the gem in dir. Returns [stdout, stderr, status].
  def ndb(args, dir, env = {})
    Open3.capture3(env, Gem.ruby, *LIBS.flat_map { |lib| ["-I", lib] }, EXE, *args, chdir: dir)
  end

  # A script in place of the daemon binary: it prints its arguments.
  def fake_daemon(dir, name)
    path = File.join(dir, name)

    File.write(path, "#!/bin/sh\necho \"daemon $@\"\n")
    FileUtils.chmod(0o755, path)

    path
  end
end

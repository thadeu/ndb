# frozen_string_literal: true

require "streams_helper"
require "open3"
require "tmpdir"
require "ndb/streams/puma_plugin"

class StreamsProcessTest < Minitest::Test
  include StreamsTestHelpers

  EXE = File.expand_path("../../ruby/exe/ndb", __dir__)

  # The boot thread of the app waits before it loads app/streams.
  SLOW_BOOT = <<~RUBY
    Ndb::Streams::Boot.singleton_class.prepend(Module.new do
      def run(...)
        sleep 3

        super
      end
    end)
  RUBY
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

  # Only a native executable (ELF or Mach-O) of PATH is the daemon: not
  # this script, a Ruby or bash stub, or a file in a shims directory.
  def test_another_command_goes_to_the_native_daemon_of_path_and_not_to_a_stub
    Dir.mktmpdir do |dir|
      stubs = File.join(dir, "stubs")
      shell = File.join(dir, "shell")
      shims = File.join(dir, "shims")
      real = File.join(dir, "real")

      FileUtils.mkdir_p([stubs, shell, shims, real])
      File.write(File.join(stubs, "ndb"), "#!/usr/bin/env ruby\nputs 'the stub'\n")
      File.symlink(EXE, File.join(dir, "ndb"))
      FileUtils.chmod(0o755, File.join(stubs, "ndb"))
      fake_daemon(shell, "ndb")

      # The native binary is the daemon that the tests build (a system tool
      # such as /bin/echo is not the same on every runner). In shims it must
      # be skipped; in real it is the daemon.
      File.symlink(native("false"), File.join(shims, "ndb"))
      File.symlink(TestDaemon.binary, File.join(real, "ndb"))

      path = [dir, stubs, shell, shims, real, File.dirname(Gem.ruby), "/usr/bin", "/bin"].join(File::PATH_SEPARATOR)
      out, err, status = ndb(%w[-h], dir, "PATH" => path, "NDB_BIN" => nil)

      assert status.success?, "exit #{status.exitstatus}: #{err}"
      # The daemon prints its help on stderr.
      assert_includes out + err, "ndb serve"
    end
  end

  # A bash shim (rbenv) that runs this script again is not native, so it is
  # never the daemon: no exec loop. The shim stops itself after 3 runs
  # (exit 99), so a loop fails the test and does not run forever.
  def test_a_bash_shim_that_runs_the_script_again_is_not_the_daemon
    Dir.mktmpdir do |dir|
      bin = File.join(dir, "bin")
      again = File.join(dir, "again")
      count = File.join(dir, "count")
      libs = LIBS.flat_map { |lib| ["-I", lib] }.join(" ")

      FileUtils.mkdir_p(bin)

      File.write(File.join(bin, "ndb"), <<~SH)
        #!/bin/sh
        echo x >> #{count}
        [ "$(wc -l < #{count})" -gt 3 ] && exit 99
        exec #{again} "$@"
      SH

      File.write(again, "#!/bin/sh\nexec #{Gem.ruby} #{libs} #{EXE} \"$@\"\n")
      FileUtils.chmod(0o755, [File.join(bin, "ndb"), again])

      path = [bin, "/usr/bin", "/bin"].join(File::PATH_SEPARATOR)
      _, err, status = ndb(%w[serve], dir, "PATH" => path, "NDB_BIN" => nil)

      assert_equal 127, status.exitstatus
      assert_match(/daemon binary is not installed/, err)
      refute File.exist?(count), "the shim ran"
    end
  end

  # The exec sets NDB_GEM_EXEC. When the script runs again with it, it
  # stops: the binary it found came back to it.
  def test_the_exec_marker_stops_a_second_exec
    Dir.mktmpdir do |dir|
      _, err, status = ndb(%w[serve], dir, "NDB_BIN" => fake_daemon(dir, "daemon"), "NDB_GEM_EXEC" => "1")

      assert_equal 127, status.exitstatus
      assert_match(/runs this script again/, err)

      marker = File.join(dir, "marker")

      File.write(marker, "#!/bin/sh\necho \"marker=$NDB_GEM_EXEC\"\n")
      FileUtils.chmod(0o755, marker)

      out, = ndb(%w[x], dir, "NDB_BIN" => marker, "NDB_GEM_EXEC" => nil)

      assert_equal "marker=1\n", out
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

  # A child in its own process group gets no SIGINT of a Ctrl-C: only the
  # one TERM of the plugin.
  def test_the_puma_plugin_starts_the_child_in_its_own_process_group
    assert_equal({ pgroup: true }, Ndb::Streams::PumaPlugin.spawn_options)
  end

  # Only the errors of the arguments are a message and exit 2. An
  # ArgumentError of the app keeps its backtrace.
  def test_an_argument_error_of_the_app_keeps_its_backtrace
    Dir.mktmpdir do |dir|
      File.write(File.join(dir, "boot.rb"), "def boot = raise(ArgumentError, \"bad config\")\nboot\n")

      _, err, status = ndb(%w[streams -r boot.rb], dir)

      assert_equal 1, status.exitstatus
      assert_match(/boot\.rb:1:in .*bad config \(ArgumentError\)/, err)
    end
  end

  # A Rails app that does not eager load: `ndb streams` loads app/streams
  # before it reads the classes, so it does not race the boot thread.
  def test_streams_runs_the_consumers_of_a_rails_app_that_does_not_eager_load
    name = NdbTestHelpers.next_name
    writer = stream_class(name)

    writer.append(n: 1)

    Dir.mktmpdir do |dir|
      out = File.join(dir, "out.txt")

      rails_app(dir, name, out, slow_boot: true)

      pid = Process.spawn(Gem.ruby, *LIBS.flat_map { |lib| ["-I", lib] }, EXE, "streams", chdir: dir, out: File.join(dir, "log"), err: File.join(dir, "log"))

      begin
        wait_until(30) { File.exist?(out) }

        assert_equal "v1 1\n", File.read(out)
      ensure
        Process.kill("TERM", pid)

        _, status = Process.wait2(pid)
      end

      assert status.success?, File.read(File.join(dir, "log"))
      refute_match(/no consumer class to run/, File.read(File.join(dir, "log")))
    end
  end

  # Inline mode right after the boot gives the entry to the consumers of
  # app/streams, and a perform runs in the reloader: a changed consumer is
  # loaded again (development).
  def test_inline_in_a_rails_app_loads_the_consumers_and_reloads_them
    name = NdbTestHelpers.next_name

    Dir.mktmpdir do |dir|
      out = File.join(dir, "out.txt")
      consumer = rails_app(dir, name, out)

      File.write(File.join(dir, "check.rb"), <<~RUBY)
        require "./config/environment"

        Ndb::Streams.inline!
        FixWriter.append(n: 1)

        sleep 1.1
        File.write("#{consumer}", File.read("#{consumer}").sub("v1", "v2"))

        FixWriter.append(n: 2)
      RUBY

      _, err, status = Open3.capture3(Gem.ruby, *LIBS.flat_map { |lib| ["-I", lib] }, "check.rb", chdir: dir)

      assert status.success?, err
      assert_equal "v1 1\nv2 2\n", File.read(out)
    end
  end

  private

  # A Rails app in dir (no eager load, with reloading) with a writer and a
  # consumer of the stream name in app/streams. Returns the consumer file.
  # slow_boot: the boot thread waits 3 s before it loads app/streams.
  def rails_app(dir, name, out, slow_boot: false)
    FileUtils.mkdir_p([File.join(dir, "config"), File.join(dir, "app", "streams")])

    File.write(File.join(dir, "config", "ndb.yml"), "test:\n  socket: #{TestDaemon.socket}\ndevelopment:\n  socket: #{TestDaemon.socket}\n")

    File.write(File.join(dir, "config", "environment.rb"), <<~RUBY)
      require "rails"
      require "ndb/rails"

      #{slow_boot ? SLOW_BOOT : ''}

      class FixApp < Rails::Application
        config.root = File.expand_path("..", __dir__)
        config.eager_load = false
        config.enable_reloading = true
        config.file_watcher = ActiveSupport::FileUpdateChecker
        config.logger = Logger.new($stderr, level: :warn)
        config.secret_key_base = "x" * 64
      end

      FixApp.initialize!
    RUBY

    File.write(File.join(dir, "app", "streams", "fix_writer.rb"), <<~RUBY)
      class FixWriter
        include Ndb::Streams

        stream "#{name}"
      end
    RUBY

    consumer = File.join(dir, "app", "streams", "fix_consumer.rb")

    File.write(consumer, <<~RUBY)
      class FixConsumer
        include Ndb::Streams

        stream "#{name}", group: :fix, start: :all

        def perform(event) = File.write("#{out}", "v1 \#{event[:n]}\n", mode: "a")
      end
    RUBY

    consumer
  end

  # A native program of the system (echo, false).
  def native(name)
    ["/bin/#{name}", "/usr/bin/#{name}"].find { |path| File.exist?(path) }
  end

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

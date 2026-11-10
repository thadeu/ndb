# frozen_string_literal: true

require "puma/plugin"
require "ndb/streams/puma_plugin"

# `plugin :ndb` in config/puma.rb runs `bundle exec ndb streams` (or bin/ndb)
# next to Puma, and stops it with Puma. NDB_STREAMS_IN_PUMA turns it on or
# off. Without it, it runs only in development. When the consumers stop by
# themselves, Puma stops too, so a broken consumer is not hidden.
Puma::Plugin.create do
  def start(launcher)
    @launcher = launcher

    return unless Ndb::Streams::PumaPlugin.enabled?

    events = launcher.events

    if events.respond_to?(:after_booted)
      events.after_booted { start_streams }
      events.after_stopped { stop_streams }
      events.before_restart { stop_streams }
    else
      events.on_booted { start_streams }
      events.on_stopped { stop_streams }
      events.on_restart { stop_streams }
    end
  end

  private

  # The child has its own process group: a Ctrl-C in the terminal goes to
  # Puma only, and Puma sends one TERM to the child.
  def start_streams
    @stopping = false
    @pid = Process.spawn(*Ndb::Streams::PumaPlugin.command, **Ndb::Streams::PumaPlugin.spawn_options)

    @monitor = Thread.new(@pid) do |pid|
      _, status = Process.wait2(pid)

      unless @stopping
        log("ndb streams stopped with #{status}, so Puma stops")

        @launcher.stop
      end
    end

    log("ndb streams started with pid #{@pid}")
  end

  def stop_streams
    return unless @pid

    @stopping = true

    Process.kill("TERM", @pid)

    Process.kill("KILL", @pid) unless @monitor.join(Ndb::Streams::PumaPlugin::STOP_TIMEOUT)

    @monitor.join(5)
  rescue Errno::ESRCH
    nil
  ensure
    @pid = nil
  end

  def log(message)
    writer = @launcher.respond_to?(:log_writer) ? @launcher.log_writer : @launcher.events

    writer.log("* #{message}")
  end
end

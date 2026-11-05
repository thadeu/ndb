# frozen_string_literal: true

require "socket"

module NdbRecord
  Response = Struct.new(:status, :headers, :body)

  # One HTTP/1.1 keep-alive connection on a unix socket. Not thread-safe:
  # the Pool hands one connection to one thread at a time.
  class Connection
    STREAM_CHUNK = 256 * 1024

    attr_reader :socket

    def initialize(path, timeout)
      @path = path
      @timeout = timeout
      @socket = nil
    end

    # body is a String, or an IO that is sent chunked and never retried.
    def request(method, target, body, headers: {})
      reused = !@socket.nil?

      begin
        send_request(method, target, body, headers)
        read_response
      rescue EOFError, Errno::EPIPE, Errno::ECONNRESET, IOError
        close

        raise unless reused && !body.respond_to?(:read)

        reused = false

        retry
      end
    rescue SystemCallError, EOFError, IOError => e
      close

      raise ConnectionError, "cannot reach ndb at #{@path}: #{e.message}"
    rescue IO::TimeoutError
      close

      raise TimeoutError, "ndb did not answer within #{@timeout}s"
    end

    def close
      @socket&.close
    rescue IOError
      nil
    ensure
      @socket = nil
    end

    private

    def open_socket
      @socket = UNIXSocket.new(@path)
      @socket.timeout = @timeout
    end

    def send_request(method, target, body, headers)
      open_socket unless @socket

      head = +"#{method} #{target} HTTP/1.1\r\nHost: ndb\r\nAccept: application/json\r\n"

      { "Content-Type" => "application/json" }.merge(headers).each { |name, value| head << "#{name}: #{value}\r\n" }

      return send_streamed(head, body) if body.respond_to?(:read)

      head << "Content-Length: #{body.bytesize}\r\n\r\n"

      @socket.write(head, body)
    end

    def send_streamed(head, io)
      @socket.write(head, "Transfer-Encoding: chunked\r\n\r\n")

      while (chunk = io.read(STREAM_CHUNK))
        @socket.write(chunk.bytesize.to_s(16), "\r\n", chunk, "\r\n")
      end

      @socket.write("0\r\n\r\n")
    end

    def read_response
      status = @socket.gets("\r\n") or raise EOFError
      headers = read_headers
      body = read_body(headers)

      close if headers["connection"].to_s.casecmp?("close")

      Response.new(status.split(" ", 3)[1].to_i, headers, body)
    end

    def read_headers
      headers = {}

      while (line = @socket.gets("\r\n")) && line != "\r\n"
        name, value = line.split(":", 2)
        headers[name.downcase] = value.to_s.strip
      end

      headers
    end

    def read_body(headers)
      return read_chunked if headers["transfer-encoding"].to_s.casecmp?("chunked")
      return (@socket.read(headers["content-length"].to_i) || +"") if headers["content-length"]

      body = @socket.read.to_s
      close

      body
    end

    def read_chunked
      body = +""

      loop do
        size = @socket.gets("\r\n").to_s.to_i(16)

        break if size.zero?

        body << @socket.read(size)
        @socket.read(2)
      end

      @socket.gets("\r\n")

      body
    end
  end

  # A small thread-safe pool: one connection per in-flight request, idle
  # ones are kept for keep-alive. It drops its connections after a fork.
  class Pool
    attr_reader :created

    def initialize(path, timeout:, size:)
      @path = path
      @timeout = timeout
      @size = size
      @mutex = Mutex.new
      @idle = []
      @created = 0
      @pid = Process.pid
    end

    def with
      conn = checkout

      begin
        yield conn
      rescue StandardError
        conn.close

        raise
      ensure
        checkin(conn)
      end
    end

    def idle_connections
      @mutex.synchronize { @idle.dup }
    end

    private

    def checkout
      @mutex.synchronize do
        reset_after_fork

        return @idle.pop if @idle.any?

        @created += 1

        Connection.new(@path, @timeout)
      end
    end

    def checkin(conn)
      @mutex.synchronize do
        @idle << conn if @idle.size < @size
      end
    end

    def reset_after_fork
      return if @pid == Process.pid

      @idle.clear
      @pid = Process.pid
    end
  end
end

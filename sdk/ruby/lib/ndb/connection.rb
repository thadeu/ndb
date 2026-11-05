# frozen_string_literal: true

require "socket"
require "uri"

module Ndb
  Response = Struct.new(:status, :headers, :body)

  # Where the daemon listens: a unix socket path, or a TCP host and port.
  # The host name is resolved each time a connection opens, so a changed
  # DNS record is followed after a reconnect.
  class Endpoint
    attr_reader :path, :host, :port

    # "unix:///path", "tcp://host:port", or a bare socket path.
    def self.parse(address)
      address = address.to_s

      return new(path: address) if address.start_with?("/")

      uri = URI.parse(address)

      case uri.scheme
      when "unix" then new(path: uri.path)
      when "tcp" then new(host: uri.host, port: uri.port)
      else raise ArgumentError, "ndb address #{address.inspect} must be unix:///path or tcp://host:port"
      end
    rescue URI::InvalidURIError
      raise ArgumentError, "ndb address #{address.inspect} must be unix:///path or tcp://host:port"
    end

    def initialize(path: nil, host: nil, port: nil)
      if path.to_s.empty? && (host.to_s.empty? || port.nil?)
        raise ArgumentError, "ndb address needs a socket path or a host and port"
      end

      @path = path
      @host = host
      @port = port
    end

    def unix? = !@path.to_s.empty?

    def to_s = unix? ? "unix://#{@path}" : "tcp://#{@host}:#{@port}"

    def host_header = unix? ? "ndb" : "#{@host}:#{@port}"

    def connect(timeout)
      return UNIXSocket.new(@path) if unix?

      TCPSocket.new(@host, @port, connect_timeout: timeout).tap { |sock| sock.setsockopt(Socket::IPPROTO_TCP, Socket::TCP_NODELAY, 1) }
    end
  end

  # One HTTP/1.1 keep-alive connection on a unix socket or TCP. Not
  # thread-safe: the Pool hands one connection to one thread at a time.
  class Connection
    STREAM_CHUNK = 256 * 1024

    attr_reader :socket

    def initialize(endpoint, timeout)
      @endpoint = endpoint
      @timeout = timeout
      @socket = nil
    end

    # body is a String, or an IO that is sent chunked and never retried. A
    # dropped reused connection is sent again only when idempotent is true
    # (a read): a write may already have been processed.
    def request(method, target, body, headers: {}, idempotent: false)
      reused = !@socket.nil?

      begin
        send_request(method, target, body, headers)
        read_response
      rescue EOFError, Errno::EPIPE, Errno::ECONNRESET, IOError
        close

        raise unless reused && idempotent && !body.respond_to?(:read)

        reused = false

        retry
      end
    rescue SystemCallError, EOFError, IOError => e
      close

      raise ConnectionError, "cannot reach ndb at #{@endpoint}: #{e.message}"
    rescue IO::TimeoutError
      close

      raise TimeoutError, "ndb did not answer within #{@timeout}s"
    end

    # Sends the request and yields the body of a 2xx response chunk by chunk,
    # as it arrives (a watch stream). The connection is closed at the end: a
    # stream is never reused. An error response is read whole and returned.
    def stream(method, target, body, headers: {}, &)
      close

      send_request(method, target, body, headers)

      status = @socket.gets("\r\n") or raise EOFError
      response_headers = read_headers
      code = status.split(" ", 3)[1].to_i

      return Response.new(code, response_headers, read_body(response_headers)) if code >= 300

      each_body_chunk(response_headers, &)

      Response.new(code, response_headers, nil)
    rescue SystemCallError, EOFError, IOError => e
      raise ConnectionError, "cannot reach ndb at #{@endpoint}: #{e.message}"
    rescue IO::TimeoutError
      raise TimeoutError, "ndb did not answer within #{@timeout}s"
    ensure
      close
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
      @socket = @endpoint.connect(@timeout)
      @socket.timeout = @timeout
    rescue SystemCallError, SocketError => e
      raise ConnectError, "cannot reach ndb at #{@endpoint}: #{e.message}"
    end

    def send_request(method, target, body, headers)
      open_socket unless @socket

      head = +"#{method} #{target} HTTP/1.1\r\nHost: #{@endpoint.host_header}\r\nAccept: application/json\r\n"

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

    def each_body_chunk(headers)
      return yield(read_body(headers)) unless headers["transfer-encoding"].to_s.casecmp?("chunked")

      loop do
        size = @socket.gets("\r\n").to_s.to_i(16)

        break if size.zero?

        yield @socket.read(size)
        @socket.read(2)
      end
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

    def initialize(endpoint, timeout:, size:)
      @endpoint = endpoint
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

        Connection.new(@endpoint, @timeout)
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

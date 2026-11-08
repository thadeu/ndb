# frozen_string_literal: true

require "test_helper"
require "openssl"
require "tmpdir"

# ndb behind a TLS proxy (Caddy in production): the client talks https:// to
# a small TLS forwarder in front of the TCP daemon.
class HttpsTest < Minitest::Test
  def self.daemon = (@daemon ||= TcpDaemon.new)

  def setup
    @coll = NdbTestHelpers.next_name
    @dir = Dir.mktmpdir
    @saved = ENV["SSL_CERT_FILE"]

    key, cert = self_signed
    File.write(File.join(@dir, "ca.pem"), cert.to_pem)
    ENV["SSL_CERT_FILE"] = File.join(@dir, "ca.pem")

    @proxy = tls_proxy(key, cert, self.class.daemon.port)
  end

  def teardown
    @proxy.close
    ENV["SSL_CERT_FILE"] = @saved
    FileUtils.rm_rf(@dir)
  end

  def test_roundtrip_over_https
    db = Ndb::Client.new(url: "https://localhost:#{@proxy.addr[1]}")

    db.ensure_collection(@coll)
    db.bulk(@coll, [{ _id: "1", a: 1 }])

    assert db.health
    assert_equal [{ "_id" => "1", "a" => 1 }], db.search(@coll, {}).rows
  end

  def test_refuses_a_certificate_for_another_host
    db = Ndb::Client.new(url: "https://127.0.0.2:#{@proxy.addr[1]}", ready_timeout: 0)

    assert_raises(Ndb::ConnectionError) { db.collections }
  end

  def test_parses_https_and_http
    https = Ndb::Endpoint.parse("https://ndb.example.com")
    http = Ndb::Endpoint.parse("http://ndb.example.com:8080")

    assert https.tls?
    assert_equal 443, https.port
    refute http.tls?
    assert_equal 8080, http.port
    assert_raises(ArgumentError) { Ndb::Endpoint.parse("ftp://h") }
  end

  private

  def self_signed
    key = OpenSSL::PKey::RSA.new(2048)
    cert = OpenSSL::X509::Certificate.new
    cert.version = 2
    cert.serial = 1
    cert.subject = cert.issuer = OpenSSL::X509::Name.parse("/CN=localhost")
    cert.public_key = key.public_key
    cert.not_before = Time.now - 60
    cert.not_after = Time.now + 3600

    ext = OpenSSL::X509::ExtensionFactory.new(cert, cert)
    cert.add_extension(ext.create_extension("subjectAltName", "DNS:localhost,IP:127.0.0.1"))
    cert.add_extension(ext.create_extension("basicConstraints", "CA:TRUE", true))
    cert.sign(key, OpenSSL::Digest.new("SHA256"))

    [key, cert]
  end

  # Accepts TLS on a free port and copies each connection to the daemon.
  def tls_proxy(key, cert, upstream)
    ctx = OpenSSL::SSL::SSLContext.new
    ctx.key = key
    ctx.cert = cert

    tcp = TCPServer.new("127.0.0.1", 0)
    server = OpenSSL::SSL::SSLServer.new(tcp, ctx)

    Thread.new do
      loop do
        client = server.accept
        Thread.new { pipe(client, TCPSocket.new("127.0.0.1", upstream)) }
      rescue OpenSSL::SSL::SSLError, IOError, Errno::EBADF
        break if tcp.closed?
      end
    end

    tcp
  end

  def pipe(a, b)
    [[a, b], [b, a]].map do |from, to|
      Thread.new do
        loop { to.write(from.readpartial(64 * 1024)) }
      rescue EOFError, IOError, SystemCallError, OpenSSL::SSL::SSLError
        to.close rescue nil
      end
    end.each(&:join)
  end
end

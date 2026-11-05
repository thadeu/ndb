# frozen_string_literal: true

require "json"
require "uri"

module NdbRecord
  Page = Struct.new(:rows, :next_cursor, :plan, :examined, :total)

  # The HTTP API of the daemon (plan §12.1). It builds JSON and reads
  # NDJSON; it has no query semantics.
  class Client
    attr_reader :pool, :socket

    def initialize(socket:, timeout: 30, pool_size: 16)
      @socket = socket
      @pool = Pool.new(socket, timeout: timeout, size: pool_size)
    end

    def find(coll, body)
      page(post(coll, "find", body))
    end

    def aggregate(coll, body)
      page(post(coll, "aggregate", body))
    end

    def insert(coll, docs)
      json(post(coll, "insert", insert: docs)).fetch("results")
    end

    def update(coll, id, set: {}, unset: [], if_version: nil)
      change = { id: id, set: set, unset: unset }
      change[:if_version] = if_version if if_version

      json(post(coll, "update", update: change))
    end

    def delete(coll, id, if_version: nil)
      change = { id: id }
      change[:if_version] = if_version if if_version

      json(post(coll, "delete", delete: change))
    end

    def ensure_collection(coll, definition)
      json(request("PUT", path(coll), definition))
    end

    def collections
      json(request("GET", "/v1/c", nil)).fetch("collections")
    end

    # Uploads an IO (streamed) and returns the reference hash.
    def put_blob(io, content_type = nil)
      json(request("PUT", "/v1/blobs", io, headers: { "Content-Type" => content_type || "application/octet-stream" }))
    end

    # The bytes of a blob, or of its range from..to (inclusive).
    def get_blob(ref, from: nil, to: nil)
      headers = from ? { "Range" => "bytes=#{from}-#{to}" } : {}

      request("GET", "/v1/blobs/#{ref}", nil, headers: headers).body
    end

    private

    def post(coll, verb, body)
      request("POST", "#{path(coll)}/#{verb}", body)
    end

    def path(coll)
      "/v1/c/#{URI.encode_www_form_component(coll).gsub('+', '%20')}"
    end

    def request(method, target, body, headers: {})
      payload = encode(body)
      response = @pool.with { |conn| conn.request(method, target, payload, headers: headers) }

      raise_api_error(response) if response.status >= 300

      response
    end

    # JSON for a Hash, "" for nothing, an IO as it is (blob upload).
    def encode(body)
      return "" if body.nil?
      return body if body.respond_to?(:read)

      JSON.generate(body)
    end

    def json(response)
      JSON.parse(response.body)
    end

    def page(response)
      headers = response.headers

      Page.new(
        ndjson(response.body),
        headers["x-ndb-next"],
        headers["x-ndb-plan"],
        headers["x-ndb-examined"]&.to_i,
        headers["x-ndb-total"]&.to_i
      )
    end

    def ndjson(body)
      body.each_line.filter_map do |line|
        line = line.strip

        JSON.parse(line) unless line.empty?
      end
    end

    def raise_api_error(response)
      error = JSON.parse(response.body).fetch("error")

      raise NdbRecord.error_for(error["code"], error["message"], response.status)
    rescue JSON::ParserError, KeyError
      raise Error.new("HTTP #{response.status}: #{response.body.strip}", code: "http_status", status: response.status)
    end
  end
end

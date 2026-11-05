# frozen_string_literal: true

require_relative "lib/ndb/version"

Gem::Specification.new do |spec|
  spec.name = "ndb"
  spec.version = Ndb::VERSION
  spec.authors = ["thadeu"]
  spec.summary = "Plain Ruby client for the ndb NDJSON query daemon"
  spec.description = "Ndb::Client talks HTTP to ndb over a unix socket or TCP: collections, " \
                     "documents with versions, queries, aggregates, watch streams and blobs. " \
                     "Stdlib only, no runtime dependencies. The daemon decides every query semantic."
  spec.license = "MIT"
  spec.homepage = "https://github.com/thadeu/ndb"
  spec.metadata = {
    "homepage_uri" => spec.homepage,
    "source_code_uri" => "https://github.com/thadeu/ndb/tree/main/sdk/ruby",
    "changelog_uri" => "https://github.com/thadeu/ndb/blob/main/CHANGELOG.md",
    "bug_tracker_uri" => "https://github.com/thadeu/ndb/issues",
    "rubygems_mfa_required" => "true"
  }
  spec.required_ruby_version = ">= 3.2"

  spec.files = Dir["lib/**/*.rb"] + %w[LICENSE README.md].select { |f| File.exist?(f) }
  spec.require_paths = ["lib"]
end

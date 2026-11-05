# frozen_string_literal: true

require_relative "lib/ndb_record/version"

Gem::Specification.new do |spec|
  spec.name = "ndb_record"
  spec.version = NdbRecord::VERSION
  spec.authors = ["thadeu"]
  spec.summary = "ActiveModel client for the ndb NDJSON query daemon"
  spec.description = "NdbRecord::Base, a lazy Relation and a unix socket HTTP client for ndb. " \
                     "The gem only builds JSON; the daemon decides every query semantic."
  spec.license = "MIT"
  spec.homepage = "https://github.com/thadeu/ndb"
  spec.required_ruby_version = ">= 3.2"

  spec.files = Dir["lib/**/*.rb"]
  spec.require_paths = ["lib"]

  spec.add_dependency "activemodel", ">= 7.1"
  spec.add_dependency "activesupport", ">= 7.1"
end

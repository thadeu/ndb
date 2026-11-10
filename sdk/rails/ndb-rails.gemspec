# frozen_string_literal: true

require_relative "lib/ndb/rails/version"

Gem::Specification.new do |spec|
  spec.name = "ndb-rails"
  spec.version = Ndb::Rails::VERSION
  spec.authors = ["thadeu"]
  spec.summary = "ActiveModel layer for the ndb NDJSON query daemon"
  spec.description = "Ndb::Record, a lazy Relation, validations, blobs and a Rails railtie on top of " \
                     "the ndb client gem. The gem only builds JSON; the daemon decides every query semantic."
  spec.license = "MIT"
  spec.homepage = "https://ndb.clowk.in"
  spec.metadata = {
    "homepage_uri" => spec.homepage,
    "documentation_uri" => "https://ndb.clowk.in/clients/rails",
    "source_code_uri" => "https://github.com/thadeu/ndb/tree/main/sdk/rails",
    "changelog_uri" => "https://github.com/thadeu/ndb/blob/main/CHANGELOG.md",
    "bug_tracker_uri" => "https://github.com/thadeu/ndb/issues",
    "rubygems_mfa_required" => "true"
  }
  spec.required_ruby_version = ">= 3.2"

  spec.files = Dir["lib/**/*.rb"] + %w[LICENSE README.md].select { |f| File.exist?(f) }
  spec.require_paths = ["lib"]

  spec.add_dependency "activemodel", ">= 7.1"
  spec.add_dependency "activesupport", ">= 7.1"
  # The two gems have one version and release together (scripts/release.sh).
  # ndb-rails X.Y.Z needs ndb X.Y.Z or newer in the same major: the worker,
  # the client and exe/ndb change with it.
  spec.add_dependency "ndb", ">= #{Ndb::Rails::VERSION}", "< #{Ndb::Rails::VERSION.split('.').first.to_i + 1}"
end

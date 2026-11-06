# frozen_string_literal: true

# The same CRUD with ndb-rails: Ndb::Record is an ActiveModel class.
# No Rails app needed; in a Rails app, config/ndb.yml does the connect step.
require "ndb/rails"

# region connect
Ndb::Rails.configure do |c|
  c.url = ENV.fetch("NDB_URL", "unix:///tmp/ndb-examples.sock") # NDB_TOKEN is read by the SDK
end
# endregion connect

# region model
class User < Ndb::Record
  attribute :email, :string, unique: true
  attribute :plan, :string, index: true

  validates :email, presence: true
end
# endregion model

User.where(_id: %w[u1 u2]).each(&:destroy)

# region insert
User.create!([
  { _id: "u1", email: "ada@example.com", plan: "free" },
  { _id: "u2", email: "grace@example.com", plan: "free" }
])

puts "insert u1, u2 -> v#{User.find('u1').version}, v#{User.find('u2').version}"
# endregion insert

# region get
ada = User.find("u1")

puts "get u1: #{ada.email} plan=#{ada.plan} v#{ada.version}"
# endregion get

# region update
copy = User.find("u1")

ada.update!(plan: "pro")

puts "update u1 plan=pro (_v 1) -> v#{ada.version}"
# endregion update

# region conflict
begin
  copy.update!(plan: "team")
rescue Ndb::StaleObjectError
  puts "update u1 plan=team (_v 1) -> matched 0"

  fresh = User.find("u1")
  stale_version = fresh.version

  fresh.update!(plan: "team")

  puts "reload u1: v#{stale_version}, retry with _v #{stale_version} -> v#{fresh.version}"
end
# endregion conflict

# region delete
User.find("u2").destroy

puts "delete u2"
# endregion delete

# region count
puts "count: #{User.count} user(s), #{User.where(plan: 'team').count} on plan team"
# endregion count

# frozen_string_literal: true

require "securerandom"

module Ndb
  ID_LOCK = Mutex.new
  ID_STATE = { ms: 0, counter: 0 }

  # A new UUIDv7 (RFC 9562), lowercase and hyphenated: the same kind of id that
  # the daemon makes. Ids from one process sort in the order of their creation.
  def self.new_id
    ms, counter = ID_LOCK.synchronize { next_stamp((Time.now.to_f * 1000).to_i) }
    rest = SecureRandom.random_bytes(8).bytes

    rest[0] = 0x80 | (rest[0] & 0x3f)

    head = [ms >> 16, ms & 0xffff, 0x7000 | counter].pack("Nnn").unpack1("H*")
    tail = rest.pack("C*").unpack1("H*")

    "#{head[0, 8]}-#{head[8, 4]}-#{head[12, 4]}-#{tail[0, 4]}-#{tail[4, 12]}"
  end

  def self.next_stamp(now)
    if now > ID_STATE[:ms]
      ID_STATE[:ms] = now
      ID_STATE[:counter] = SecureRandom.random_number(0x800)
    elsif ID_STATE[:counter] >= 0x0fff
      ID_STATE[:ms] += 1
      ID_STATE[:counter] = SecureRandom.random_number(0x800)
    else
      ID_STATE[:counter] += 1
    end

    [ID_STATE[:ms], ID_STATE[:counter]]
  end

  private_class_method :next_stamp
end

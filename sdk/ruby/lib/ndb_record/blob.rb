# frozen_string_literal: true

require "stringio"

module NdbRecord
  # A large value in the blob store (plan §7.5). The document keeps only
  # {"$blob" => "sha256:...", "size" => N, "type" => "..."}.
  #
  # It behaves like a read-only IO. A blob built from an IO or a String is
  # pending: it is uploaded when the record is saved. A blob from a loaded
  # record reads the daemon in 1 MiB ranges, so memory stays small.
  class Blob
    CHUNK_SIZE = 1 << 20

    attr_reader :ref, :size, :content_type

    def self.from_reference(reference)
      blob = allocate

      blob.send(:stored!, reference["$blob"], reference["size"], reference["type"])

      blob
    end

    def initialize(source, content_type: nil)
      @source = source.is_a?(String) ? StringIO.new(source) : source
      @content_type = content_type || (source.respond_to?(:content_type) ? source.content_type : nil)
      @pos = 0
    end

    def uploaded?
      !@ref.nil?
    end

    # Uploads a pending blob; a stored blob does nothing.
    def upload!(client = NdbRecord.client)
      return self if uploaded?

      @source.rewind if @source.respond_to?(:rewind)

      stored = client.put_blob(@source, @content_type)

      stored!(stored["$blob"], stored["size"], stored["type"])
    end

    # The document value, uploading first if needed.
    def to_reference
      upload!

      { "$blob" => ref, "size" => size, "type" => content_type }.compact
    end

    def as_json(*)
      uploaded? ? to_reference : nil
    end

    # IO#read: all the rest, or up to length bytes (nil at the end).
    def read(length = nil)
      return @source.read(length) unless uploaded?
      return read_all if length.nil?
      return +"" if length.zero?
      return nil if eof?

      fetch([length, size - @pos].min)
    end

    def each_chunk(chunk_size = CHUNK_SIZE)
      return enum_for(:each_chunk, chunk_size) unless block_given?

      while (chunk = read(chunk_size))
        yield chunk
      end
    end

    def eof?
      uploaded? ? @pos >= size : @source.eof?
    end

    def rewind
      @pos = 0

      @source.rewind if !uploaded? && @source.respond_to?(:rewind)

      0
    end

    def ==(other)
      other.is_a?(Blob) && uploaded? && other.uploaded? && ref == other.ref
    end

    private

    def stored!(ref, size, content_type)
      @ref = ref
      @size = size
      @content_type = content_type
      @source = nil
      @pos = 0

      self
    end

    def read_all
      out = +""

      each_chunk { |chunk| out << chunk }

      out
    end

    def fetch(count)
      data = NdbRecord.client.get_blob(ref, from: @pos, to: @pos + count - 1)

      @pos += data.bytesize

      data
    end
  end

  # attribute :recording, :blob
  class BlobType < ActiveModel::Type::Value
    def type
      :blob
    end

    def cast(value)
      case value
      when nil, Blob then value
      when Hash then value.key?("$blob") ? Blob.from_reference(value) : nil
      when String then Blob.new(value)
      else value.respond_to?(:read) ? Blob.new(value) : raise(ArgumentError, "cannot make a blob from #{value.class}")
      end
    end

    def deserialize(value)
      cast(value)
    end

    def serialize(value)
      value&.to_reference
    end
  end

  ActiveModel::Type.register(:blob, BlobType)
end

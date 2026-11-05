# frozen_string_literal: true

require "date"

module Ndb
  # Ruby values -> the where JSON of the daemon (plan §12.2). No semantics:
  # every operator is evaluated by the daemon.
  module Conditions
    module_function

    # {status: "x", n: 1..3} -> {"status" => "x", "n" => {"$gte" => 1, "$lte" => 3}}
    # A key that starts with "$" ("$or", "$and", "$not") is passed as is.
    def equality(attrs)
      attrs.to_h do |field, value|
        key = field.to_s

        [key, key.start_with?("$") ? json_value(value) : condition(value)]
      end
    end

    # where.not: every field is negated on its own.
    def negation(attrs)
      attrs.each_with_object({}) do |(field, value), out|
        key = field.to_s

        case value
        when nil then out[key] = { "$exists" => true }
        when Array then out[key] = { "$nin" => json_value(value) }
        when Regexp then out[key] = { "$not" => json_value(value) }
        when Range then out["$not"] = { key => condition(value) }
        when Hash then out["$not"] = { key => condition(value) }
        else out[key] = { "$ne" => json_value(value) }
        end
      end
    end

    # AND of clauses: flat when the keys are apart, "$and" when they collide.
    def combine(clauses)
      clauses.reduce({}) do |acc, clause|
        next acc.merge(clause) if (acc.keys & clause.keys).empty?

        { "$and" => [acc, clause] }
      end
    end

    def condition(value)
      case value
      when nil then { "$exists" => false }
      when Array then { "$in" => json_value(value) }
      when Range then range(value)
      when Hash then json_value(value)
      else json_value(value)
      end
    end

    def range(range)
      out = {}

      out["$gte"] = json_value(range.begin) unless range.begin.nil?

      unless range.end.nil?
        out[range.exclude_end? ? "$lt" : "$lte"] = json_value(range.end)
      end

      out
    end

    # Times become RFC 3339 strings with milliseconds; the rest is JSON as is.
    def json_value(value)
      case value
      when Time, DateTime then value.utc.iso8601(3)
      when Date then value.iso8601
      when Symbol then value.to_s
      when Array then value.map { |v| json_value(v) }
      when Regexp then regex(value)
      when Hash then hash_value(value)
      else time_like?(value) ? value.utc.iso8601(3) : value
      end
    end

    def hash_value(hash)
      hash.each_with_object({}) do |(key, value), out|
        if key.to_s == "$regex" && value.is_a?(Regexp)
          out.merge!(regex(value))
        else
          out[key.to_s] = json_value(value)
        end
      end
    end

    # Only the Regexp objects of a request body become $regex; every other
    # value is left as it is (find and aggregate send the body as it is).
    def regexps(value)
      case value
      when Regexp then regex(value)
      when Array then value.map { |v| regexps(v) }
      when Hash
        value.each_with_object({}) do |(key, v), out|
          if key.to_s == "$regex" && v.is_a?(Regexp)
            out.merge!(regex(v))
          else
            out[key] = regexps(v)
          end
        end
      else value
      end
    end

    # A Ruby Regexp -> {"$regex" => source, "$options" => flags}. The flag m
    # is always set: in Ruby ^ and $ match at each line. IGNORECASE is i,
    # MULTILINE (dot matches a newline) is s, EXTENDED is x.
    def regex(regexp)
      reject_unsupported!(regexp.source)

      options = +"m"

      options << "i" if regexp.options.anybits?(Regexp::IGNORECASE)
      options << "s" if regexp.options.anybits?(Regexp::MULTILINE)
      options << "x" if regexp.options.anybits?(Regexp::EXTENDED)

      { "$regex" => regexp.source, "$options" => options.chars.sort_by { |c| "imsx".index(c) }.join }
    end

    # The daemon uses RE2. These Ruby features do not exist there, so they
    # fail here, before any request.
    def reject_unsupported!(source)
      feature = unsupported_feature(source)

      return unless feature

      raise InvalidQuery.new("regexp /#{source}/ uses #{feature}, which RE2 does not support",
                             code: "invalid_query")
    end

    LOOKAROUND = { "?=" => "look-ahead (?=", "?!" => "negative look-ahead (?!",
                   "?<=" => "look-behind (?<=", "?<!" => "negative look-behind (?<!",
                   "?>" => "an atomic group (?>" }.freeze
    ESCAPES = { "h" => "\\h", "G" => "\\G", "R" => "\\R", "X" => "\\X", "k" => "\\k<name>" }.freeze

    def unsupported_feature(source)
      in_class = false
      quantifier = false
      i = 0

      while i < source.length
        char = source[i]

        if char == "\\"
          nxt = source[i + 1].to_s

          return "a backreference \\#{nxt}" if nxt.match?(/[1-9]/)
          return ESCAPES[nxt] if ESCAPES.key?(nxt)

          quantifier = false
          i += 2

          next
        end

        if in_class
          in_class = false if char == "]"
        elsif char == "["
          in_class = true
          i += 1 if source[i + 1] == "^"
          i += 1 if source[i + 1] == "]"
        elsif char == "("
          LOOKAROUND.each { |prefix, name| return name if source[i + 1, prefix.length] == prefix }
        elsif char == "+" && quantifier
          return "a possessive quantifier"
        end

        quantifier = !in_class && %w[* + ?].include?(char) && !(char == "?" && source[i - 1] == "(")
        i += 1
      end

      nil
    end

    # ActiveSupport::TimeWithZone and the like, without a dependency on them.
    def time_like?(value)
      value.respond_to?(:utc) && value.respond_to?(:iso8601) && !value.is_a?(Date)
    end
  end
end

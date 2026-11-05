# frozen_string_literal: true

module NdbRecord
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
      when Time, DateTime, ActiveSupport::TimeWithZone then value.utc.iso8601(3)
      when Date then value.iso8601
      when Symbol then value.to_s
      when Array then value.map { |v| json_value(v) }
      when Hash then value.to_h { |k, v| [k.to_s, json_value(v)] }
      else value
      end
    end
  end
end

# frozen_string_literal: true

module Ndb
  # find_by_<attr>(value) and find_by_<a>_and_<b>(v1, v2), with a ! variant
  # that raises Ndb::NotFound. Only declared attribute names (and id) work;
  # any other name is a normal NoMethodError.
  #
  # The including object answers dynamic_finder_klass (the model) and
  # dynamic_finder_scope (what find_by runs on).
  module DynamicFinders
    PATTERN = /\Afind_by_(.+?)(!)?\z/
    AND = "_and_"

    def method_missing(name, *args, &)
      attrs, bang = dynamic_finder(name)

      return super unless attrs

      define_dynamic_finder(name, attrs, bang)

      run_dynamic_finder(attrs, bang, args)
    end

    def respond_to_missing?(name, include_private = false)
      dynamic_finder(name) ? true : super
    end

    private

    def run_dynamic_finder(attrs, bang, args)
      unless args.size == attrs.size
        raise ArgumentError, "wrong number of arguments (given #{args.size}, expected #{attrs.size})"
      end

      conditions = attrs.map { |name| name == "id" ? "_id" : name }.zip(args).to_h
      scope = dynamic_finder_scope

      bang ? scope.find_by!(conditions) : scope.find_by(conditions)
    end

    # The method is cached on the singleton class of a model. A relation is
    # short-lived, so it runs the finder without caching.
    def define_dynamic_finder(name, attrs, bang)
      return unless is_a?(Class)

      define_singleton_method(name) { |*args| run_dynamic_finder(attrs, bang, args) }
    end

    def dynamic_finder(name)
      match = PATTERN.match(name.to_s)

      return unless match

      attrs = segment(match[1], finder_names)

      attrs && [attrs, !match[2].nil?]
    end

    def finder_names
      dynamic_finder_klass.attribute_names | ["id"]
    end

    # Splits "a_and_b" into attribute names. A name may hold "_and_" itself.
    def segment(text, names)
      return [text] if names.include?(text)

      names.each do |name|
        next unless text.start_with?(name + AND)

        rest = segment(text[(name.length + AND.length)..], names)

        return [name, *rest] if rest
      end

      nil
    end
  end
end

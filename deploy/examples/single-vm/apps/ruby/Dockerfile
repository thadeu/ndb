# Build context: the repo root (see docker-compose.yml). The layout of the repo
# is kept, so the path in the Gemfile points to the SDK.
FROM ruby:3.4-slim

WORKDIR /repo

COPY sdk/ruby ./sdk/ruby
COPY deploy/examples/single-vm/apps/ruby ./deploy/examples/single-vm/apps/ruby

WORKDIR /repo/deploy/examples/single-vm/apps/ruby

RUN bundle install

CMD ["bundle", "exec", "ruby", "app.rb"]

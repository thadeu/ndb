.PHONY: test race lint fuzz-smoke bench bench-read bench-read-month bench-go build dist up ui-dev docs docs-build tag tg

test:
	go test ./...

race:
	go test -race ./...

lint:
	golangci-lint run ./...

# Fuzz uses one worker per core by default. Keep it light, locally and in CI.
FUZZ_PARALLEL ?= 2
FUZZTIME ?= 10s

fuzz-smoke:
	@for pkg in $$(go list ./...); do \
		for fn in $$(go test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
			go test -run=^$$ -fuzz=^$$fn$$ -fuzztime=$(FUZZTIME) -parallel=$(FUZZ_PARALLEL) $$pkg || exit 1; \
		done; \
	done

# The ESL benchmark (ndb bench): live writes with a consumer, a kill -9, a
# bulk load and reads, in about two minutes, on its own daemon. Run it
# before and after a change of impact; it is not part of CI.
bench: build
	./bin/ndb bench

# The reads on 2 million documents (286k calls), kept in .bench/2m: the
# first run loads them (about 8 min), the next ones only read (about 3 min).
bench-read: build
	./bin/ndb bench -data .bench/2m -calls 285715

# The same 2 million documents over six monthly partitions, kept in
# .bench/2m-month: reads of all of them, up to the previous month (also by
# the events-* alias) and of one month.
bench-read-month: build
	./bin/ndb bench -data .bench/2m-month -calls 285715 -partition month

# The Go micro-benchmarks of the packages.
bench-go:
	go test -run=^$$ -bench=. -benchmem ./...

# Static, CGO-free release build. VERSION defaults to the git description.
# Binaries are staged in bin/ (git-ignored); tarballs and checksums go to dist/.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
TARGETS ?= linux/amd64 linux/arm64 darwin/arm64
LDFLAGS = -s -w -X main.version=$(VERSION)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ndb ./cmd/ndb

dist:
	mkdir -p dist
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; name=ndb-v$(VERSION)-$$os-$$arch; \
		mkdir -p bin/stage/$$name; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o bin/stage/$$name/ndb ./cmd/ndb || exit 1; \
		cp LICENSE README.md bin/stage/$$name/; \
		tar -C bin/stage -czf dist/$$name.tar.gz $$name; \
	done
	cd dist && (sha256sum ndb-v$(VERSION)-*.tar.gz 2>/dev/null || shasum -a 256 ndb-v$(VERSION)-*.tar.gz) > checksums.txt

# Local daemon for development: data and socket live in .dev/ (git-ignored).
# Apps connect with NDB_URL=unix://$(CURDIR)/.dev/ndb.sock or tcp://127.0.0.1:7070.
DEV_DIR ?= .dev
DEV_TCP ?= 127.0.0.1:7070

up: build
	mkdir -p $(DEV_DIR)/data
	./bin/ndb -data-dir $(DEV_DIR)/data -unix $(DEV_DIR)/ndb.sock -tcp $(DEV_TCP)

# Console (ndb-ui) with hot reload at http://127.0.0.1:4321. Run `make up`
# in another terminal for a local daemon. Set NDB_URL to fix one connection,
# for example NDB_URL=unix://$(CURDIR)/.dev/ndb.sock.
ui-dev:
	cd console && mise exec -- bun install --frozen-lockfile && mise exec -- bun run dev

# Docs site with hot reload at http://localhost:5173.
docs:
	cd docs-site && pnpm install --frozen-lockfile && pnpm docs:dev

docs-build:
	cd docs-site && pnpm install --frozen-lockfile && pnpm docs:build

# Release: make tag patch|minor|major (or make tg patch). Bumps the SDKs and
# CHANGELOG.md, commits "Release X.Y.Z", tags vX.Y.Z, and asks before the
# push. With make's --dry-run (-n) or DRY=1, it only shows the next version.
# YES=1 pushes without asking.
# The + runs the script even under --dry-run. See scripts/release.sh.
# The short flags of make are the first word of MAKEFLAGS ("n" for -n), read
# before --silent is added: its own n must not look like a dry run.
RELEASE_DRY := $(if $(or $(DRY),$(findstring n,$(filter-out -%,$(firstword $(MAKEFLAGS))))),--dry-run)

ifneq ($(filter tag tg,$(firstword $(MAKECMDGOALS))),)
MAKEFLAGS += --silent
BUMP := $(word 2,$(MAKECMDGOALS))

ifneq ($(BUMP),)
$(eval $(BUMP): tag ;)
endif
endif

tag:
	+@scripts/release.sh $(or $(BUMP),patch) $(RELEASE_DRY) $(if $(YES),--yes)

tg: tag

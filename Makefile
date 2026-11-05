.PHONY: test race lint fuzz-smoke bench build dist up docs docs-build

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

bench:
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

# Docs site with hot reload at http://localhost:5173.
docs:
	cd docs-site && pnpm install --frozen-lockfile && pnpm docs:dev

docs-build:
	cd docs-site && pnpm install --frozen-lockfile && pnpm docs:build

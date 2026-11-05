.PHONY: test race lint fuzz-smoke bench

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

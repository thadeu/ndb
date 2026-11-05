# Run the examples against a local daemon (started when none answers).
#
#   make run-crud LANG=go        LANG is go, ruby, ts or curl (crud also has rails)
#   make run-logs                without LANG, every language runs
#   make check                   every example, every language; the output must be <example>/output.txt
#   make check UPDATE=1          write output.txt from the first language first
#   make stop | clean            stop the daemon | also delete its data
#
# Needs: Go, Ruby 3.2+ with bundler, Node 24 with pnpm, curl and jq.

EXAMPLES := crud logs reports outbox ttl-sessions
RUNS := $(addprefix run-,$(EXAMPLES))

# LANG is also a shell locale variable, so only the known values count.
SELECTED := $(if $(filter go ruby ts curl rails,$(LANG)),$(LANG),all)

unexport LANG

.PHONY: $(RUNS) check daemon stop clean deps

$(RUNS): run-%:
	@./run.sh $* $(SELECTED)

check:
	@for e in $(EXAMPLES); do CHECK=1 UPDATE="$(UPDATE)" ./run.sh $$e all || exit 1; done

daemon:
	@./run.sh daemon

deps:
	@./run.sh deps

stop:
	@./run.sh stop

clean: stop
	rm -rf .data data node_modules Gemfile.lock

#!/usr/bin/env bash
# Runs the examples against a local daemon. Use it through the Makefile:
#
#   make run-crud LANG=go        one example, one language (go ruby ts curl, and rails for crud)
#   make run-crud                one example, every language
#   make check                   every example, every language, and the output must be <example>/output.txt
#   make check UPDATE=1          write output.txt from the first language, then check
#
# The daemon is started when none answers. NDB_URL and NDB_TOKEN point it elsewhere.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(dirname "$here")"
sock="/tmp/ndb-examples.sock"

export NDB_URL="${NDB_URL:-unix://$sock}"

cd "$here"

healthy() {
	local args=(-fsS -o /dev/null)

	case "$NDB_URL" in
	unix://*) args+=(--unix-socket "${NDB_URL#unix://}" http://ndb/_health) ;;
	tcp://*) args+=(http://"${NDB_URL#tcp://}"/_health) ;;
	esac

	if [[ -n "${NDB_TOKEN:-}" ]]; then
		args+=(-H "Authorization: Bearer $NDB_TOKEN")
	fi

	curl "${args[@]}" 2>/dev/null
}

daemon() {
	if healthy; then
		return 0
	fi

	if [[ "$NDB_URL" != "unix://$sock" ]]; then
		echo "no daemon answers at $NDB_URL" >&2

		return 1
	fi

	if [[ ! -x "$root/bin/ndb" ]]; then
		(cd "$root" && go build -o bin/ndb ./cmd/ndb)
	fi

	mkdir -p .data
	logs/append.sh --seed

	nohup nice -n 10 "$root/bin/ndb" -config ndb.yml >.data/daemon.log 2>&1 &
	echo $! >.data/ndb.pid

	for _ in $(seq 1 100); do
		if healthy; then
			echo "ndb started (pid $(cat .data/ndb.pid), socket $sock)"

			return 0
		fi

		sleep 0.1
	done

	echo "ndb did not start, see examples/.data/daemon.log" >&2

	return 1
}

stop() {
	if [[ -f .data/ndb.pid ]]; then
		kill -TERM "$(cat .data/ndb.pid)" 2>/dev/null || true
		rm -f .data/ndb.pid
	fi
}

prepare() {
	case "$1" in
	ruby | rails)
		if [[ ! -f Gemfile.lock ]]; then
			bundle install --quiet
		fi
		;;
	ts)
		if [[ ! -f "$root/sdk/ts/dist/index.js" ]]; then
			(cd "$root/sdk/ts" && pnpm install --frozen-lockfile --silent && pnpm build >/dev/null)
		fi

		if [[ ! -d node_modules ]]; then
			pnpm install --silent
		fi
		;;
	esac
}

# execute EXAMPLE LANG runs one program and prints what it prints.
execute() {
	local example="$1" lang="$2"

	case "$lang" in
	go) (cd "$example/go" && nice -n 10 go run .) ;;
	ruby) BUNDLE_GEMFILE="$here/Gemfile" nice -n 10 bundle exec ruby "$example/ruby/main.rb" ;;
	rails) BUNDLE_GEMFILE="$here/Gemfile" nice -n 10 bundle exec ruby "$example/rails/main.rb" ;;
	ts) nice -n 10 node "$example/ts/main.ts" ;;
	curl) nice -n 10 bash "$example/curl.sh" ;;
	esac
}

run() {
	local example="$1" wanted="${2:-all}" langs=() out="$here/.data/out"

	if [[ "$wanted" == "all" ]]; then
		langs=(go ruby ts curl)

		if [[ -d "$example/rails" ]]; then
			langs+=(rails)
		fi
	else
		langs=("$wanted")
	fi

	mkdir -p "$out"
	logs/append.sh --seed

	for lang in "${langs[@]}"; do
		local program="$example/$lang"

		if [[ "$lang" == "curl" ]]; then
			program="$example/curl.sh"
		fi

		if [[ ! -e "$program" ]]; then
			echo "==> $example has no $lang version" >&2

			return 1
		fi

		prepare "$lang"

		echo "==> $example ($lang)"

		execute "$example" "$lang" | tee "$out/$example.$lang.txt"
	done

	if [[ -n "${CHECK:-}" ]]; then
		local golden="$example/output.txt"

		if [[ -n "${UPDATE:-}" ]]; then
			cp "$out/$example.${langs[0]}.txt" "$golden"
		fi

		for lang in "${langs[@]}"; do
			if ! diff -u "$golden" "$out/$example.$lang.txt"; then
				echo "==> $example ($lang): the output is not $golden" >&2

				return 1
			fi
		done

		echo "==> $example: the output of ${langs[*]} is $golden"
	fi
}

case "${1:-}" in
daemon) daemon ;;
stop) stop ;;
deps)
	for lang in ruby ts; do
		prepare "$lang"
	done
	;;
*)
	daemon
	run "$@"
	;;
esac

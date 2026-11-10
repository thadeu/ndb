#!/busybox/sh
# ndb-stack entrypoint: starts the daemon and the console, forwards SIGTERM
# and SIGINT to both, and exits when one of them exits. Arguments go to
# `ndb serve`, for example `-log-level debug`.

set -eu

# part: optional token for the TCP listener (the unix socket never asks for one)
if [ -n "${NDB_TOKEN:-}" ]; then
  umask 077
  printf '%s' "$NDB_TOKEN" > /run/ndb/token
  umask 022

  set -- -token-file /run/ndb/token "$@"
fi

unset NDB_TOKEN

# part: the two processes
/usr/local/bin/ndb serve -tcp "${NDB_TCP:-0.0.0.0:7070}" "$@" &
ndb_pid=$!

/usr/local/bin/ndb-ui &
ui_pid=$!

stopping=0

stop() {
  kill -TERM "$ndb_pid" "$ui_pid" 2>/dev/null || true
}

trap 'stopping=1; stop' TERM INT

# part: wait until one process ends (a short sleep in the background, so a
# signal runs the trap at once)
while kill -0 "$ndb_pid" 2>/dev/null && kill -0 "$ui_pid" 2>/dev/null; do
  sleep 1 &
  wait $! || true
done

stop

status=0

wait "$ndb_pid" || status=$?
wait "$ui_pid" || true

# part: a process that ended by itself is a failure, so that a restart
# policy starts the container again
if [ "$stopping" = 0 ] && [ "$status" = 0 ]; then
  status=1
fi

exit "$status"

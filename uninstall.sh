#!/bin/sh
# ndb uninstaller. Sibling of install.sh.
#
#   curl -fsSL https://ndb.clowk.in/uninstall.sh | bash
#   curl -fsSL https://ndb.clowk.in/uninstall.sh | bash -s -- --systemd
#   curl -fsSL https://ndb.clowk.in/uninstall.sh | bash -s -- --systemd --purge
#
# Default: remove the ndb binary.
# --systemd: also stop and disable ndb.service and remove the unit.
#            /var/lib/ndb (your data) and /etc/ndb are kept.
# --purge:   also delete /var/lib/ndb, /etc/ndb and the ndb user. This
#            destroys the data. It needs --systemd.
#
# Environment:
#   NDB_INSTALL_DIR  directory of the binary (default: look in /usr/local/bin
#                    and ~/.local/bin)

set -eu

SERVICE_USER="ndb"
DATA_DIR="/var/lib/ndb"
CONFIG_DIR="/etc/ndb"
UNIT_PATH="/etc/systemd/system/ndb.service"

SYSTEMD=0
PURGE=0

if [ -t 1 ]; then
  RED=$(printf '\033[0;31m')
  GREEN=$(printf '\033[0;32m')
  YELLOW=$(printf '\033[1;33m')
  CYAN=$(printf '\033[0;36m')
  NC=$(printf '\033[0m')
else
  RED="" GREEN="" YELLOW="" CYAN="" NC=""
fi

log()  { printf '%s[ndb/uninstall]%s %s\n' "$CYAN" "$NC" "$*"; }
ok()   { printf '%s[ndb/uninstall]%s %s\n' "$GREEN" "$NC" "$*"; }
warn() { printf '%s[ndb/uninstall]%s %s\n' "$YELLOW" "$NC" "$*" >&2; }
fail() { printf '%s[ndb/uninstall]%s %s\n' "$RED" "$NC" "$*" >&2; exit 1; }

usage() {
  cat <<USAGE
ndb uninstaller

Usage: uninstall.sh [--systemd] [--purge]

  (none)     remove the ndb binary
  --systemd  also stop and disable ndb.service and remove the unit;
             keep $DATA_DIR and $CONFIG_DIR
  --purge    also delete $DATA_DIR, $CONFIG_DIR and the $SERVICE_USER user
             (destroys the data; needs --systemd)

Environment:
  NDB_INSTALL_DIR  directory of the binary
USAGE
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --systemd) SYSTEMD=1 ;;
      --purge) PURGE=1 ;;
      -h|--help) usage; exit 0 ;;
      *) fail "unknown option: $1 (try --help)" ;;
    esac

    shift
  done

  if [ "$PURGE" -eq 1 ] && [ "$SYSTEMD" -eq 0 ]; then
    fail "--purge needs --systemd"
  fi
}

remove_binaries() {
  if [ -n "${NDB_INSTALL_DIR:-}" ]; then
    DIRS="$NDB_INSTALL_DIR"
  else
    DIRS="/usr/local/bin $HOME/.local/bin"
  fi

  FOUND=0

  for dir in $DIRS; do
    if [ -f "$dir/ndb" ]; then
      rm -f "$dir/ndb" || fail "cannot remove $dir/ndb (try sudo)"

      ok "removed $dir/ndb"

      FOUND=1
    fi
  done

  if [ "$FOUND" -eq 0 ]; then
    warn "no ndb binary found in: $DIRS"
  fi
}

remove_systemd() {
  if [ "$(id -u)" -ne 0 ]; then
    fail "--systemd needs root; run it with sudo"
  fi

  if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now ndb >/dev/null 2>&1 || warn "ndb.service was not running or not enabled"
  fi

  if [ -e "$UNIT_PATH" ]; then
    rm -f "$UNIT_PATH"

    ok "removed $UNIT_PATH"
  else
    warn "$UNIT_PATH not found"
  fi

  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload
  fi

  if [ "$PURGE" -eq 1 ]; then
    purge
  else
    log "kept $DATA_DIR and $CONFIG_DIR (use --purge to delete them)"
  fi
}

purge() {
  warn "purging $DATA_DIR and $CONFIG_DIR"

  rm -rf "$DATA_DIR" "$CONFIG_DIR"

  if id "$SERVICE_USER" >/dev/null 2>&1; then
    if command -v userdel >/dev/null 2>&1; then
      userdel "$SERVICE_USER" 2>/dev/null || warn "could not remove user $SERVICE_USER"
    else
      deluser "$SERVICE_USER" 2>/dev/null || warn "could not remove user $SERVICE_USER"
    fi
  fi

  if command -v getent >/dev/null 2>&1 && getent group "$SERVICE_USER" >/dev/null 2>&1; then
    groupdel "$SERVICE_USER" 2>/dev/null || true
  fi

  ok "purged"
}

main() {
  parse_args "$@"

  if [ "$SYSTEMD" -eq 1 ]; then
    remove_systemd
  fi

  remove_binaries

  ok "done"
}

main "$@"

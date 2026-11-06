#!/bin/sh
# ndb installer.
#
#   curl -fsSL https://ndb.clowk.in/install.sh | bash
#   curl -fsSL https://ndb.clowk.in/install.sh | bash -s -- --systemd
#   curl -fsSL https://ndb.clowk.in/install.sh | bash -s -- --ui
#
# Installs the ndb binary from GitHub Releases, after checking its sha256.
# With --systemd (Linux, root) it also creates the ndb user, /var/lib/ndb and
# /etc/ndb/ndb.yml, installs the systemd unit and starts it.
# With --ui it installs the web console (ndb-ui) instead of the daemon.
#
# Environment:
#   NDB_VERSION      version to install, with or without "v" (default: latest)
#   NDB_COMPONENT    ndb (default) or ui; the same as --ui
#   NDB_INSTALL_DIR  where to put the binary
#                    (default: /usr/local/bin when writable, else ~/.local/bin)

set -eu

REPO="thadeu/ndb"
SERVICE_USER="ndb"
DATA_DIR="/var/lib/ndb"
CONFIG_DIR="/etc/ndb"
UNIT_PATH="/etc/systemd/system/ndb.service"

SYSTEMD=0
COMPONENT="${NDB_COMPONENT:-ndb}"
TMP=""

if [ -t 1 ]; then
  RED=$(printf '\033[0;31m')
  GREEN=$(printf '\033[0;32m')
  YELLOW=$(printf '\033[1;33m')
  CYAN=$(printf '\033[0;36m')
  NC=$(printf '\033[0m')
else
  RED="" GREEN="" YELLOW="" CYAN="" NC=""
fi

log()  { printf '%s[ndb/install]%s %s\n' "$CYAN" "$NC" "$*"; }
ok()   { printf '%s[ndb/install]%s %s\n' "$GREEN" "$NC" "$*"; }
warn() { printf '%s[ndb/install]%s %s\n' "$YELLOW" "$NC" "$*" >&2; }
fail() { printf '%s[ndb/install]%s %s\n' "$RED" "$NC" "$*" >&2; exit 1; }

cleanup() {
  if [ -n "$TMP" ]; then
    rm -rf "$TMP"
  fi
}

usage() {
  cat <<USAGE
ndb installer

Usage: install.sh [--systemd | --ui]

  --systemd   Linux, as root: create the ndb user, /var/lib/ndb and
              /etc/ndb/ndb.yml (only if missing), install and start ndb.service
  --ui        install the web console (ndb-ui) instead of the daemon

Environment:
  NDB_VERSION      version to install, with or without "v" (default: latest)
  NDB_COMPONENT    ndb (default) or ui; the same as --ui
  NDB_INSTALL_DIR  directory for the binary
                   (default: /usr/local/bin when writable, else ~/.local/bin)
USAGE
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --systemd) SYSTEMD=1 ;;
      --ui) COMPONENT=ui ;;
      -h|--help) usage; exit 0 ;;
      *) fail "unknown option: $1 (try --help)" ;;
    esac

    shift
  done

  case "$COMPONENT" in
    ndb) BIN=ndb; CHECKSUMS=checksums.txt ;;
    ui)  BIN=ndb-ui; CHECKSUMS=ndb-ui-checksums.txt ;;
    *) fail "unknown component: $COMPONENT (use ndb or ui)" ;;
  esac

  if [ "$COMPONENT" = ui ] && [ "$SYSTEMD" -eq 1 ]; then
    fail "--systemd installs the daemon; it cannot go with --ui"
  fi
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "missing command: $1"
}

detect_platform() {
  case "$(uname -s)" in
    Linux)  OS=linux ;;
    Darwin) OS=darwin ;;
    *) fail "unsupported OS: $(uname -s) (ndb has linux and darwin builds)" ;;
  esac

  case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    arm64|aarch64) ARCH=arm64 ;;
    *) fail "unsupported architecture: $(uname -m) (ndb has amd64 and arm64 builds)" ;;
  esac
}

resolve_tag() {
  if [ -n "${NDB_VERSION:-}" ]; then
    TAG="v${NDB_VERSION#v}"

    return
  fi

  log "looking for the latest release"

  TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1) ||
    fail "could not read the latest release; set NDB_VERSION, for example NDB_VERSION=0.1.0"

  if [ -z "$TAG" ]; then
    fail "no release found for $REPO; set NDB_VERSION"
  fi
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d ' ' -f 1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d ' ' -f 1
  else
    fail "need sha256sum or shasum to verify the download"
  fi
}

download_and_verify() {
  NAME="$BIN-$TAG-$OS-$ARCH"
  BASE="https://github.com/$REPO/releases/download/$TAG"

  log "downloading $NAME.tar.gz"

  curl -fsSL -o "$TMP/$NAME.tar.gz" "$BASE/$NAME.tar.gz" ||
    fail "download failed: $BASE/$NAME.tar.gz (does $TAG exist for $OS/$ARCH?)"

  curl -fsSL -o "$TMP/$CHECKSUMS" "$BASE/$CHECKSUMS" ||
    fail "download failed: $BASE/$CHECKSUMS"

  WANT=$(awk -v f="$NAME.tar.gz" '$2 == f || $2 == "*" f { print $1 }' "$TMP/$CHECKSUMS")

  if [ -z "$WANT" ]; then
    fail "$NAME.tar.gz is not in $CHECKSUMS"
  fi

  GOT=$(sha256_of "$TMP/$NAME.tar.gz")

  if [ "$GOT" != "$WANT" ]; then
    fail "sha256 mismatch for $NAME.tar.gz (want $WANT, got $GOT); nothing was installed"
  fi

  ok "sha256 verified"
}

pick_install_dir() {
  if [ -n "${NDB_INSTALL_DIR:-}" ]; then
    BIN_DIR="$NDB_INSTALL_DIR"
  elif [ "$(id -u)" -eq 0 ] || [ -w /usr/local/bin ]; then
    BIN_DIR=/usr/local/bin
  else
    BIN_DIR="$HOME/.local/bin"
  fi

  mkdir -p "$BIN_DIR" || fail "cannot create $BIN_DIR"
}

install_binary() {
  tar -xzf "$TMP/$NAME.tar.gz" -C "$TMP" || fail "cannot unpack $NAME.tar.gz"

  if [ ! -f "$TMP/$NAME/$BIN" ]; then
    fail "the archive has no $NAME/$BIN"
  fi

  cp "$TMP/$NAME/$BIN" "$BIN_DIR/.$BIN.new" || fail "cannot write to $BIN_DIR"
  chmod 0755 "$BIN_DIR/.$BIN.new"
  mv -f "$BIN_DIR/.$BIN.new" "$BIN_DIR/$BIN"

  ok "installed $BIN_DIR/$BIN ($TAG)"

  case ":$PATH:" in
    *":$BIN_DIR:"*) ;;
    *) warn "$BIN_DIR is not on your PATH; add it: export PATH=\"$BIN_DIR:\$PATH\"" ;;
  esac
}

fetch_to() {
  curl -fsSL -o "$2" "https://raw.githubusercontent.com/$REPO/$TAG/deploy/systemd/$1" ||
    fail "cannot download deploy/systemd/$1 from tag $TAG"
}

ensure_user() {
  if id "$SERVICE_USER" >/dev/null 2>&1; then
    return
  fi

  log "creating system user and group $SERVICE_USER"

  if command -v useradd >/dev/null 2>&1; then
    useradd --system --user-group --no-create-home --home-dir "$DATA_DIR" \
      --shell /usr/sbin/nologin "$SERVICE_USER"
  else
    addgroup -S "$SERVICE_USER"
    adduser -S -D -H -h "$DATA_DIR" -s /sbin/nologin -G "$SERVICE_USER" "$SERVICE_USER"
  fi
}

install_systemd() {
  if [ "$OS" != linux ]; then
    fail "--systemd needs Linux"
  fi

  if [ "$(id -u)" -ne 0 ]; then
    fail "--systemd needs root; run it with sudo"
  fi

  command -v systemctl >/dev/null 2>&1 || fail "--systemd needs systemctl"

  ensure_user

  install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR"
  install -d -m 0755 "$CONFIG_DIR"

  if [ -e "$CONFIG_DIR/ndb.yml" ]; then
    log "keeping the existing $CONFIG_DIR/ndb.yml"
  else
    fetch_to ndb.yml "$TMP/ndb.yml"
    install -m 0644 "$TMP/ndb.yml" "$CONFIG_DIR/ndb.yml"

    ok "wrote $CONFIG_DIR/ndb.yml"
  fi

  fetch_to ndb.service "$TMP/ndb.service"

  if [ "$BIN_DIR" != /usr/local/bin ]; then
    sed "s#^ExecStart=/usr/local/bin/ndb#ExecStart=$BIN_DIR/ndb#" "$TMP/ndb.service" >"$TMP/ndb.service.new"
    mv "$TMP/ndb.service.new" "$TMP/ndb.service"
  fi

  install -m 0644 "$TMP/ndb.service" "$UNIT_PATH"

  ok "installed $UNIT_PATH"

  systemctl daemon-reload
  systemctl enable --now ndb || fail "systemctl enable --now ndb failed; see: journalctl -u ndb"

  report_service
}

report_service() {
  SOCK=$(sed -n 's/^[[:space:]]*unix:[[:space:]]*\([^[:space:]#]*\).*/\1/p' "$CONFIG_DIR/ndb.yml" | head -n 1)
  SOCK="${SOCK:-/run/ndb/ndb.sock}"

  i=0

  while [ "$i" -lt 20 ] && [ ! -S "$SOCK" ]; do
    sleep 0.5
    i=$((i + 1))
  done

  STATE=$(systemctl is-active ndb 2>/dev/null || true)

  ok "ndb.service is $STATE"
  log "data dir:  $DATA_DIR"
  log "config:    $CONFIG_DIR/ndb.yml"
  log "socket:    $SOCK"

  if [ ! -S "$SOCK" ]; then
    warn "the socket is not there yet; check: systemctl status ndb"
  fi
}

main() {
  parse_args "$@"

  need curl
  need tar
  need awk

  detect_platform
  resolve_tag
  pick_install_dir

  TMP=$(mktemp -d)
  trap cleanup EXIT INT TERM

  download_and_verify
  install_binary

  if [ "$COMPONENT" = ui ]; then
    log "run it: ndb-ui   (http://127.0.0.1:4321, opens unix:///run/ndb/ndb.sock; set NDB_URL for another daemon)"
  elif [ "$SYSTEMD" -eq 1 ]; then
    install_systemd
  else
    log "run it: ndb -data-dir ~/ndb-data -unix ~/ndb.sock   (or re-run with --systemd)"
  fi
}

main "$@"

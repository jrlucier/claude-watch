#!/bin/sh
# claude-watch installer / uninstaller
#
# Install:
#   curl -fsSL https://raw.githubusercontent.com/jrlucier/claude-watch/main/install.sh | sh
#
# Uninstall:
#   curl -fsSL https://raw.githubusercontent.com/jrlucier/claude-watch/main/install.sh | sh -s -- --uninstall
#
# Environment overrides:
#   INSTALL_DIR   Where the binary lives          (default: $HOME/.local/bin)
#   VERSION       Specific version to install     (default: latest release)
#   REPO          GitHub owner/repo               (default: jrlucier/claude-watch)

set -eu

REPO="${REPO:-jrlucier/claude-watch}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${VERSION:-}"
GH_BASE="${GH_BASE:-https://github.com}"   # override for testing/mirroring
ACTION="install"

for arg in "$@"; do
  case "$arg" in
    --uninstall|--remove|uninstall|remove) ACTION="uninstall" ;;
    -h|--help)
      cat <<EOF
claude-watch installer

  install (default)    Download the latest release and install it
  --uninstall          Stop the daemon, remove the binary and systemd unit

Environment:
  INSTALL_DIR=$INSTALL_DIR
  VERSION=${VERSION:-<latest>}
  REPO=$REPO
EOF
      exit 0 ;;
    *) printf 'unknown argument: %s\n' "$arg" >&2; exit 2 ;;
  esac
done

# --- pretty output ----------------------------------------------------------
if [ -t 1 ]; then
  BOLD="$(printf '\033[1m')"; DIM="$(printf '\033[2m')"
  RED="$(printf '\033[31m')"; GREEN="$(printf '\033[32m')"
  YELLOW="$(printf '\033[33m')"; RESET="$(printf '\033[0m')"
else
  BOLD=""; DIM=""; RED=""; GREEN=""; YELLOW=""; RESET=""
fi

info()  { printf '%s==>%s %s\n'      "$BOLD"   "$RESET" "$*"; }
warn()  { printf '%swarn:%s %s\n'    "$YELLOW" "$RESET" "$*" >&2; }
ok()    { printf '%s✓%s %s\n'        "$GREEN"  "$RESET" "$*"; }
die()   { printf '%serror:%s %s\n'   "$RED"    "$RESET" "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }

# --- detect platform -------------------------------------------------------
detect_platform() {
  os="$(uname -s)"
  arch="$(uname -m)"

  case "$os" in
    Linux) os="linux" ;;
    *)     die "unsupported OS: $os (claude-watch is currently Linux-only)" ;;
  esac

  case "$arch" in
    x86_64|amd64)   arch="amd64" ;;
    aarch64|arm64)  arch="arm64" ;;
    *)              die "unsupported architecture: $arch" ;;
  esac

  printf '%s-%s' "$os" "$arch"
}

# --- resolve version -------------------------------------------------------
resolve_version() {
  if [ -n "$VERSION" ]; then
    printf '%s' "$VERSION"
    return
  fi
  # Follow the /releases/latest redirect to learn the tag name without needing jq.
  url="$GH_BASE/$REPO/releases/latest"
  resolved="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$url")" \
    || die "could not reach GitHub to resolve latest release"
  tag="${resolved##*/}"
  case "$tag" in
    v*) printf '%s' "$tag" ;;
    *)  die "could not parse latest release tag from $resolved" ;;
  esac
}

# --- uninstall flow --------------------------------------------------------
do_uninstall() {
  info "Uninstalling claude-watch"
  removed_anything=0

  # Stop systemd unit if present.
  unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
  unit_path="$unit_dir/claude-watch.service"
  if command -v systemctl >/dev/null 2>&1 && [ -f "$unit_path" ]; then
    info "Stopping and disabling claude-watch.service"
    systemctl --user disable --now claude-watch.service >/dev/null 2>&1 || true
    systemctl --user reset-failed claude-watch.service  >/dev/null 2>&1 || true
    rm -f "$unit_path"
    systemctl --user daemon-reload >/dev/null 2>&1 || true
    ok "Removed $unit_path"
    removed_anything=1
  fi

  # Stop any running daemon (covers users who started it manually, without systemd).
  bin_path="$INSTALL_DIR/claude-watch"
  if [ -x "$bin_path" ]; then
    "$bin_path" quit >/dev/null 2>&1 || true
    rm -f "$bin_path"
    ok "Removed $bin_path"
    removed_anything=1
  elif command -v claude-watch >/dev/null 2>&1; then
    other="$(command -v claude-watch)"
    warn "Found another claude-watch on PATH at $other (not removed — outside INSTALL_DIR=$INSTALL_DIR)"
    printf '%s     Re-run with INSTALL_DIR=%s to remove it%s\n' "$DIM" "$(dirname "$other")" "$RESET"
  fi

  # Offer to nuke user data too.
  cfg_dir="${XDG_CONFIG_HOME:-$HOME/.config}/claude-watch"
  state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/claude-watch"
  data_present=0
  [ -d "$cfg_dir" ]   && data_present=1
  [ -d "$state_dir" ] && data_present=1

  if [ "$data_present" = "1" ]; then
    answer=""
    if [ -n "${CLAUDE_WATCH_PURGE:-}" ]; then
      answer="$CLAUDE_WATCH_PURGE"
    elif [ -r /dev/tty ]; then
      printf '\n%sAlso remove config and logs?%s\n' "$BOLD" "$RESET"
      [ -d "$cfg_dir" ]   && printf '  %s%s%s\n' "$DIM" "$cfg_dir"   "$RESET"
      [ -d "$state_dir" ] && printf '  %s%s%s\n' "$DIM" "$state_dir" "$RESET"
      printf '[y/N] '
      read -r answer </dev/tty || answer=""
    fi
    case "$answer" in
      y|Y|yes|YES|Yes)
        [ -d "$cfg_dir" ]   && rm -rf "$cfg_dir"   && ok "Removed $cfg_dir"
        [ -d "$state_dir" ] && rm -rf "$state_dir" && ok "Removed $state_dir"
        removed_anything=1
        ;;
      *)
        info "Kept config and logs ($cfg_dir, $state_dir)"
        ;;
    esac
  fi

  if [ "$removed_anything" = "0" ]; then
    warn "Nothing to remove — no claude-watch install found under INSTALL_DIR=$INSTALL_DIR"
    exit 1
  fi
  ok "Uninstall complete"
  exit 0
}

if [ "$ACTION" = "uninstall" ]; then
  do_uninstall
fi

# --- main ------------------------------------------------------------------
need curl
need tar
need uname

platform="$(detect_platform)"
info "Detected platform: ${BOLD}${platform}${RESET}"

version="$(resolve_version)"
info "Installing claude-watch ${BOLD}${version}${RESET}"

archive="claude-watch-${version}-${platform}.tar.gz"
base="$GH_BASE/$REPO/releases/download/${version}"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

info "Downloading ${archive}"
curl -fsSL -o "$tmpdir/$archive"        "$base/$archive"        || die "download failed: $base/$archive"
curl -fsSL -o "$tmpdir/$archive.sha256" "$base/$archive.sha256" || die "checksum download failed"

info "Verifying checksum"
(
  cd "$tmpdir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c "$archive.sha256" >/dev/null
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c "$archive.sha256" >/dev/null
  else
    die "neither sha256sum nor shasum found — can't verify download"
  fi
) || die "checksum mismatch — refusing to install"
ok "Checksum verified"

info "Extracting"
tar -xzf "$tmpdir/$archive" -C "$tmpdir"
[ -f "$tmpdir/claude-watch" ] || die "archive did not contain a claude-watch binary"

mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmpdir/claude-watch" "$INSTALL_DIR/claude-watch" \
  || die "failed to install to $INSTALL_DIR (try a different INSTALL_DIR or use sudo)"
ok "Installed to ${BOLD}${INSTALL_DIR}/claude-watch${RESET}"

# --- post-install hints ----------------------------------------------------
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    warn "$INSTALL_DIR is not on your \$PATH"
    printf '%s     Add this to your shell config:%s\n' "$DIM" "$RESET"
    printf '       export PATH="%s:$PATH"\n' "$INSTALL_DIR"
    ;;
esac

# --- optional: install systemd user unit for auto-start --------------------
# Only offered on Linux with systemd. Reads from /dev/tty so it works under
# `curl | sh` (where stdin is the pipe, not the terminal).
maybe_install_autostart() {
  [ "${platform%-*}" = "linux" ] || return 0
  command -v systemctl >/dev/null 2>&1 || return 0

  unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
  unit_path="$unit_dir/claude-watch.service"

  if [ -f "$unit_path" ]; then
    info "systemd unit already exists at $unit_path — skipping"
    return 0
  fi

  answer=""
  if [ -n "${CLAUDE_WATCH_AUTOSTART:-}" ]; then
    answer="$CLAUDE_WATCH_AUTOSTART"
  elif [ -r /dev/tty ]; then
    printf '\n%sStart claude-watch automatically on login?%s [Y/n] ' "$BOLD" "$RESET"
    read -r answer </dev/tty || answer=""
  else
    info "Non-interactive shell — skipping auto-start prompt"
    info "  Run with CLAUDE_WATCH_AUTOSTART=yes to enable it"
    return 0
  fi

  case "$answer" in
    ""|y|Y|yes|YES|Yes) ;;
    *) info "Skipping auto-start setup"; return 0 ;;
  esac

  mkdir -p "$unit_dir"
  cat > "$unit_path" <<EOF
[Unit]
Description=Claude Code usage tray indicator
PartOf=graphical-session.target
After=graphical-session.target

[Service]
ExecStart=$INSTALL_DIR/claude-watch start --foreground
Restart=on-failure

[Install]
WantedBy=graphical-session.target
EOF
  ok "Wrote $unit_path"

  systemctl --user daemon-reload || warn "systemctl --user daemon-reload failed"
  if systemctl --user enable --now claude-watch.service >/dev/null 2>&1; then
    ok "Enabled and started claude-watch.service"
  else
    warn "Could not start claude-watch.service automatically"
    printf '%s     Try manually: systemctl --user enable --now claude-watch%s\n' "$DIM" "$RESET"
  fi
}

maybe_install_autostart

printf '\n%sNext:%s claude-watch start  %s(or check the tray icon if auto-start is on)%s\n' \
  "$BOLD" "$RESET" "$DIM" "$RESET"

# claude-watch

A system-tray indicator for Claude Code usage. Linux (GNOME) and macOS.

Shows current **5-hour** and **7-day** subscription usage at a glance — the tray icon itself is a pair of stacked progress bars (5h on top, 7d below), colored green / yellow / orange / red as you approach each limit. Click for a per-block cost breakdown, burn rate, and time-to-exhaustion forecast.

Replaces the unmaintained [Haletran/claude-usage-extension](https://github.com/Haletran/claude-usage-extension) (GNOME extension #9231) and goes further:

- **Hybrid data** — Anthropic's OAuth usage API for the authoritative 5h/7d %, plus local JSONL parsing of `~/.claude/projects/**/*.jsonl` for burn rate, per-model cost, and exhaustion forecast.
- **Token-refresh aware** — fsnotify on `~/.claude/.credentials.json` so the daemon re-polls within a second when `claude` CLI refreshes the OAuth token.
- **Stale-data resilient** — if the API hiccups, the icon stays at last-known values with a `!` indicator on the tooltip rather than going blank.
- **Threshold notifications** — opt-in desktop notification when crossing 80 % or 95 % (configurable).
- **CLI surface** — `claude-watch status --json` for scripting.

## Build

```
make build
```

Drop `bin/claude-watch` on your `$PATH`.

## Usage

```
claude-watch start          # spawn detached daemon, show tray icon
claude-watch status         # one-line snapshot
claude-watch status --json  # JSON for scripting
claude-watch refresh        # force an immediate API + JSONL refresh
claude-watch set-label 5h   # panel label shows only 5h%  (default)
claude-watch set-label both # panel label shows "5h XX% / 7d YY%"
claude-watch quit           # stop the daemon
```

Any non-`start` command auto-spawns the daemon if it isn't running.

## Config

`$XDG_CONFIG_HOME/claude-watch/config.toml` (default `~/.config/claude-watch/config.toml`):

```toml
api_refresh_seconds   = 300      # minimum 300 — Anthropic rate-limits faster polling
jsonl_refresh_seconds = 30
label_mode            = "5h"     # "5h" | "both"
proxy_url             = ""
notify_thresholds     = [80, 95]
```

## Logs

`$XDG_STATE_HOME/claude-watch/daemon.log` (default `~/.local/state/claude-watch/daemon.log`).

## Auto-start on login

Add a user systemd unit `~/.config/systemd/user/claude-watch.service`:

```ini
[Unit]
Description=Claude Code usage tray indicator
After=graphical-session.target

[Service]
ExecStart=%h/.local/bin/claude-watch start --foreground
Restart=on-failure

[Install]
WantedBy=default.target
```

Then `systemctl --user enable --now claude-watch`.

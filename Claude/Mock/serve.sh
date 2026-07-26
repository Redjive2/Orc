#!/usr/bin/env bash
#
# Brings the mock data up in cq: sets a password and a sync token, starts the
# server, and syncs the seeded stores into it once.
#
# Run Claude/Mock/seed.sh first. The server binds loopback only — this is a
# demo of somebody's private window into Orc, and it has no business being
# reachable from the network.
#
# Usage:
#   Claude/Mock/serve.sh [root]        default: /tmp/orc-mock
#   kill $(cat <root>/serve.pid)       to stop it

set -euo pipefail

ROOT="${1:-/tmp/orc-mock}"
ADDR="127.0.0.1:8080"
PASSWORD="mockmockmock"

if [ ! -f "$ROOT/env" ]; then
  echo "no mock data at $ROOT — run Claude/Mock/seed.sh first" >&2
  exit 1
fi

set -a; . "$ROOT/env"; set +a
export CQ_SERVER="http://$ADDR"

say() { printf '\n\033[1m%s\033[0m\n' "$*"; }

say "server state"
rm -rf "$CQ_STATE" "$CQ_HOME"
# Piped in rather than typed: cq does not disable terminal echo, and says so.
printf '%s' "$PASSWORD" | cq admin operator
# The token goes to stdout on its own and the human-facing report to stderr,
# which is what makes `export CQ_TOKEN=$(cq admin token studio)` work as its
# help says. Printed once, stored only as a digest: caught here or not at all.
CQ_TOKEN="$(cq admin token studio)"
if [ -z "$CQ_TOKEN" ]; then
  echo "could not capture a sync token" >&2
  exit 1
fi
export CQ_TOKEN
# Replaced rather than appended: rerunning this mints a new token and voids the
# old one, so a stale line left behind would be a trap for whoever sources it.
grep -v '^export CQ_TOKEN=' "$ROOT/env" > "$ROOT/env.tmp" && mv "$ROOT/env.tmp" "$ROOT/env"
printf 'export CQ_TOKEN=%s\n' "$CQ_TOKEN" >> "$ROOT/env"

say "serving"
# nohup and setsid so the server outlives the shell that started it — otherwise
# it dies with the terminal, which for a demo you come back to is no use.
if command -v setsid >/dev/null 2>&1; then
  setsid nohup cq serve --addr "$ADDR" > "$ROOT/serve.log" 2>&1 &
else
  nohup cq serve --addr "$ADDR" > "$ROOT/serve.log" 2>&1 &
fi
disown 2>/dev/null || true

# $! is the wrapper's pid, not the server's — setsid forks before it execs — so
# the pid is looked up rather than assumed. A stop command that kills the wrong
# process is worse than none.
for _ in $(seq 30); do
  SERVER_PID="$(pgrep -f "cq serve --addr $ADDR" | head -1)"
  [ -n "$SERVER_PID" ] && break
  sleep 0.1
done
if [ -z "${SERVER_PID:-}" ]; then
  echo "the server did not start; see $ROOT/serve.log" >&2
  exit 1
fi
echo "$SERVER_PID" > "$ROOT/serve.pid"

# Wait for the port rather than sleeping a guessed interval.
for _ in $(seq 50); do
  if curl -sf -o /dev/null "http://$ADDR/" 2>/dev/null; then break; fi
  sleep 0.1
done

say "syncing"
cq sync

say "up"
echo "  site       http://$ADDR"
echo "  password   $PASSWORD"
echo "  log        $ROOT/serve.log"
echo "  stop       kill \$(cat $ROOT/serve.pid)"
echo
echo "  re-sync after changing the stores:  . $ROOT/env && cq sync"

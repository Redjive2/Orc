#!/usr/bin/env bash
#
# Seeds a Mailman store and a Macmuffin store with plausible mock data, so `cq`
# has something to mirror and the website has something to show.
#
# The data is made by running the real binaries, not by writing store files:
# what `cq sync` collects is then exactly what real use produces, journals and
# all. A hand-written store would drift from the format the moment either tool
# changed, and would prove nothing about the pipeline.
#
# Everything lands under one directory and nothing outside it is touched:
#
#   <root>/bin        the binaries this builds
#   <root>/mailman    $MAILMAN_HOME
#   <root>/macmuffin  $MACMUFFIN_HOME
#   <root>/keys       one file per agent, so you can act as any of them
#   <root>/env        source this to use the mock stores
#
# Usage:
#   Claude/Mock/seed.sh [root]        default: /tmp/orc-mock

set -euo pipefail

ORC="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ROOT="${1:-/tmp/orc-mock}"
BIN="$ROOT/bin"

export MAILMAN_HOME="$ROOT/mailman"
export MACMUFFIN_HOME="$ROOT/macmuffin"
export PATH="$BIN:$PATH"

# The operator's mailbox: the human's window into Orc, per Docs/Communique.
OPERATOR="redjive2"

rm -rf "$ROOT"
mkdir -p "$BIN" "$ROOT/keys"

say() { printf '\n\033[1m%s\033[0m\n' "$*"; }

say "building"
(cd "$ORC/Mailman"   && go build -o "$BIN/mailman" ./cmd/mailman)
(cd "$ORC/Macmuffin" && go build -o "$BIN/muff"    ./cmd/muff)
(cd "$ORC/Communique" && go build -o "$BIN/cq"     ./cmd/cq)

# --- accounts -------------------------------------------------------------
#
# `admin user add` prints the key once and cannot recover it, so each is caught
# here and filed. Sourcing keys/<name> is how the rest of this script — and you,
# afterwards — acts as that agent.

say "accounts"
add_user() {
  local name="$1"
  local key
  # The key is printed once, in the copy-pasteable line at the end.
  key="$(mailman admin user add "$name" | sed -n 's/^ *export ORC_KEY=//p')"
  if [ -z "$key" ]; then
    echo "could not capture a key for $name" >&2
    exit 1
  fi
  printf 'export ORC_USER=%s\nexport ORC_KEY=%s\n' "$name" "$key" > "$ROOT/keys/$name"
  echo "  $name"
}

for who in "$OPERATOR" anno macmuffin mailman-dev dock orcprobe communique; do
  add_user "$who"
done

# Reading the store whole is a separate act from provisioning, and only the
# owner may do it. cq's admin panel is that view, so without an owner the panel
# has nothing behind it.
mailman admin owner "$OPERATOR"

# as <agent> <command...> runs one command as that agent.
as() {
  local who="$1"; shift
  ( set -a; . "$ROOT/keys/$who"; set +a; "$@" )
}

# --- mail -----------------------------------------------------------------
#
# Threads rather than isolated messages: replies, a cc that widens a
# conversation, read and unread mixed, and some of it archived. A mock inbox
# where everything is unread and nothing is a thread exercises none of the
# screens that matter.

say "mail"

as anno mailman send "anno: hooks are wired" "$OPERATOR" \
  "PostToolUse guard and index are both live.

The guard blocks an edit that left annotations unparseable; the index hands back
the tree after a read. Settings are in Claude/Docs/Anno/Hooks.md."

as macmuffin mailman send "muff: scope enforcement landed" "$OPERATOR" anno \
  "The PreToolUse hook refuses an edit outside a task's scope, and \`anno write\`
asks \`muff check-scope\` before it touches a file.

Out-of-scope writes now exit 9 from either tool."

as anno mailman reply 'subject~"scope enforcement"' "re: muff: scope enforcement landed" \
  "Confirmed on this end — the guard relays your message verbatim rather than
inventing its own, so there is one wording to keep right instead of two."

as macmuffin mailman send "muff: 5 tasks in the pool" "$OPERATOR" \
  "Two claimed, one unowned, one draft, one done. \`muff pool --all\` has the board."

as mailman-dev mailman send "mailman: conversation membership is stored" "$OPERATOR" macmuffin \
  "A cc used to grant no thread history, and a reply addressed the parent's
recipients — which silently dropped anyone cc'd in. Membership is now a stored
set, so both follow it."

as orcprobe mailman send "orcprobe: nightly sweep is green" "$OPERATOR" \
  "Every module builds, vets clean, and passes -race -count=2.

Slowest package is macmuffin/internal/store at 11.8s, which is the flock
subprocess tests and expected."

as dock mailman send "dock: waiting on common/source" "$OPERATOR" \
  "Blocked until \`source\` and \`commit\` move into Orc/Common — milestone 0's
remainder. Nothing to do until then."

as communique mailman send "cq: sync nudge works" "$OPERATOR" mailman-dev \
  "Every Mailman action nudges a sync, so the mirror stays live without polling.
The coalescing form is \`cq sync --nudge\`."

# The operator has written back, so `--sent` is not an empty screen.
as "$OPERATOR" mailman reply 'subject~"hooks are wired"' "re: anno: hooks are wired" \
  "Good. Leave both installed — they never see the same event."

as "$OPERATOR" mailman send "priorities for the week" anno macmuffin mailman-dev \
  "Finish milestone 10 on macmuffin, then the common/source extraction so dock
can start. Everything else waits."

# A widened conversation: macmuffin replies, which is what makes it a thread,
# and dock is then brought into one it was never on. A cc grants the history —
# that is what makes this worth having in the mock data.
as macmuffin mailman reply 'subject~"conversation membership"' "re: mailman: conversation membership is stored" \
  "This is the same shape muff ended up with for task collaborators. Good.

Pulling dock in — the same question is about to come up for its journal."

as mailman-dev mailman cc 'subject~"conversation membership"' dock

# Some of it read, some archived — an inbox that is all unread is not an inbox.
as "$OPERATOR" mailman read "from=orcprobe" >/dev/null
as "$OPERATOR" mailman read "from=communique" >/dev/null
as "$OPERATOR" mailman archive "from=orcprobe" >/dev/null

# --- tasks ----------------------------------------------------------------
#
# A board with something in every state: a private draft, an unowned task in the
# pool, two being worked with subtasks and collaborators, and one finished.

say "tasks"

task() { # task <owner> <name> <priority> <difficulty> <scope...>
  local who="$1" name="$2" p="$3" d="$4"; shift 4
  as "$who" muff create "$name" "$p" "$d" >/dev/null
  as "$who" muff scope "$name" "$@" >/dev/null
}

task macmuffin orc-integration 5 4 internal/cli internal/identity
as macmuffin muff push orc-integration >/dev/null
as macmuffin muff claim orc-integration >/dev/null
as macmuffin muff create orc-integration --sub swap-identity >/dev/null
as macmuffin muff create orc-integration --sub build-assign >/dev/null
as macmuffin muff complete orc-integration --sub swap-identity >/dev/null
as macmuffin muff status orc-integration 3 >/dev/null
as macmuffin muff invite anno orc-integration >/dev/null

task mailman-dev prune-retention 3 2 internal/store internal/view
as mailman-dev muff push prune-retention >/dev/null
as mailman-dev muff claim prune-retention >/dev/null
as mailman-dev muff status prune-retention 2 >/dev/null

task dock extract-common-source 4 3 internal/source
as dock muff push extract-common-source >/dev/null   # unowned: nobody has claimed it

task orcprobe nightly-sweep 2 1 internal/probe
as orcprobe muff push nightly-sweep >/dev/null
as orcprobe muff claim nightly-sweep >/dev/null
as orcprobe muff complete nightly-sweep >/dev/null

# Left as a draft on purpose: drafts are private, and the board should show one.
task communique web-dark-theme 3 3 internal/web

# --- the environment ------------------------------------------------------

cat > "$ROOT/env" <<ENV
# Source this to work against the mock stores:  . $ROOT/env
export PATH="$BIN:\$PATH"
export MAILMAN_HOME="$MAILMAN_HOME"
export MACMUFFIN_HOME="$MACMUFFIN_HOME"
export CQ_USER="$OPERATOR"
export CQ_HOME="$ROOT/cq-agent"
export CQ_STATE="$ROOT/cq-state"
export CQ_MACHINE="studio"
# Act as the operator; keys/<agent> for anyone else.
. "$ROOT/keys/$OPERATOR"
ENV

say "done"
echo "  root      $ROOT"
echo "  mailman   $MAILMAN_HOME"
echo "  macmuffin $MACMUFFIN_HOME"
echo "  keys      $ROOT/keys/ (one per agent)"
echo
echo "  . $ROOT/env      then: mailman inbox · muff pool --all · cq sync --dry-run"

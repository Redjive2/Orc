# Login — a credential the fleet can see

A plan for authenticating agent sessions without anybody attaching to a pty, and for
making the state of that credential visible in cq.

This says what to build, in what order, and — where a choice was not obvious — why
that one. Nothing here is built yet.

## §1 The failure this is about

A session with no credential does not fail. It opens a **login prompt**, on a pty
nobody is attached to, and sits there. `orc status` calls it live, because it is:
supervisor up, child up, pty open, nothing happening. The fleet looks healthy and
does no work.

That is the whole problem, and it has three shapes:

1. **Never authenticated.** A new machine, or a fleet whose operator authenticated
   as themselves and never thought about what the agents inherit.
2. **Authenticated and expired.** The commonest one, and the worst, because it
   arrives on a fleet that has been working for weeks. Claude warns three days out —
   at *its* prompt, which on an agent machine nobody reads.
3. **Authenticated for the wrong thing.** An `ANTHROPIC_API_KEY` in the operator's
   profile belonging to a disabled organisation outranks the subscription login they
   thought was in use.

Sessions inherit the real `HOME`, so a keychain login reaches them, and
`CLAUDE_CODE_OAUTH_TOKEN` now reaches them too. What is missing is not a mechanism.
It is that **nothing knows whether the mechanism is working** until agents stop.

## §2 What Orc must not do

Stated first, because it is the constraint the rest is designed around and it was
the reason the obvious version of this was refused.

**Orc never clicks "Authorize".** The consent screen is where a person grants a
program access to their account. A fleet that clicked it on a schedule would be
minting credentials with nobody watching, and it would do so by driving a page it
does not own — which breaks the first time that page changes, silently, at the
moment a fleet stops working.

**Orc never handles a password.** Nothing in this plan types into a login form.

What Orc *may* do is the mechanical half either side of the human's decision: open
the flow, hold the result, report on it, and hand it to sessions. The approval in
the middle stays a person's, in their own browser, once.

## §3 `orc login`

One command, run by the operator, on the machine that runs agents.

```
orc login                  start the flow; print the URL; wait for the code
orc login --code <code>    finish a flow started elsewhere
orc login --status         which credential sessions use, and how long it has left
orc login --forget         remove the stored token
```

It wraps `claude setup-token`, which exists for exactly this — "CI pipelines,
scripts, or other environments where interactive browser login isn't available" —
and which produces a token that authenticates against the subscription and lasts a
year. Wrapping rather than reimplementing: the OAuth flow belongs to Claude, and a
second implementation of it in Orc would be a second thing to keep in step with a
login page.

**Where the token goes.** `$ORC_HOME/credential`, 0600, the same shape and the same
permissions as an identity's key. Not the operator's shell profile, for three
reasons: Orc can then report on it, `orc doctor` can check it without reading the
operator's dotfiles, and a token that lives where the fleet lives is one that
`sh/nuke` removes with the fleet rather than leaving behind.

Sessions get it as `CLAUDE_CODE_OAUTH_TOKEN`, injected by `session/environment.go`
alongside the rest — *unless* the environment already carries a credential, which
stays ahead of it. An operator who has set `ANTHROPIC_API_KEY` on purpose has said
something, and Orc quietly overriding it would be Orc deciding whose account the
fleet spends.

**What is recorded beside it.** A small JSON file, not the token: when it was
minted, when it expires if the flow says, which account it belongs to if the flow
says, and when it was last seen to work. That is what every screen below reads. The
token itself is read only when a session is started.

## §4 The URL is the interesting part

The flow prints an authorization URL and waits for a code. On a laptop that is a
browser opening by itself. On a headless agent machine — a server in a cupboard, a
box reached over ssh — it is a URL nobody can click.

So `orc login` **always prints the URL** rather than only opening a browser, and
waits for the code on stdin. That alone makes the flow work over ssh, which is most
of the problem.

It also makes the flow *device-independent*, and that is what cq is for: the
operator can authorize on a phone. The agent machine never needs a browser at all.

## §5 cq: what the server may and may not do

**The server cannot reach the agent machine.** Everything cq shows is a mirror, and
everything it does is queued for the next sync. That decides the whole shape:

- cq **can** show the state of the credential, because that travels in the snapshot.
- cq **cannot** run the flow, because the flow is a process on the agent machine
  waiting on stdin, and the queue delivers actions minutes later.

So cq's job is **noticing and handing off**, not doing.

### §5.1 What the snapshot carries

Never the token. The fleet snapshot gains a small block:

| Field | Why it is there |
|---|---|
| `source` | which credential a session would use — keychain, `$CLAUDE_CODE_OAUTH_TOKEN`, an API key, a cloud provider — in Claude's own precedence order, so it names what would *actually* be used |
| `expires` | when it stops working, where that is knowable |
| `minted` | when `orc login` last ran |
| `account` | the account it belongs to, where the flow says — so "authenticated for the wrong thing" is visible |
| `blocked` | identities employed and running whose sessions cannot authenticate |

`blocked` is the field that earns the feature. Everything else is diagnosis; that
one is the symptom, and it is what turns "the fleet is quiet" into "these four
agents cannot log in".

### §5.2 The screen

A row on `manage → fleet`, not a tab of its own: it is one fact about the machine,
and a tab nobody visits is a tab nobody reads when it matters. It says one of:

- **fine** — the source and how long it has left, muted, one line;
- **expiring** — inside three days, warned, with what to run;
- **expired, or nothing reaches sessions** — alarmed, above the agent list,
  naming the agents that are stopped by it.

The warning appears on the *fleet* screen because that is the screen somebody opens
when agents are not working, which is exactly when this is the answer.

### §5.3 The hand-off

The useful thing cq can do is carry the **URL**, not the code.

When `orc login` is waiting, it records the authorization URL beside the credential
file. The next sync mirrors it, and cq shows it as a link: the operator taps it on
their phone, authorizes in their own browser, and is given a code.

**The code goes back to the agent machine directly, not through cq.** This is the
one place this plan refuses the convenient answer, and the reason is worth writing
down: the cq server is the internet-facing box, and it is a different trust domain
from the machine that runs agents. An authorization code that passed through it
would let anyone who had compromised the server mint a credential for the
operator's Claude account. A queue that carries somebody's tasks is not a queue that
should carry their credentials.

So the flow is:

1. `orc login` on the agent machine prints the URL and waits.
2. cq shows the URL — the operator authorizes from wherever they are.
3. The operator pastes the code into the terminal where step 1 is waiting, or runs
   `orc login --code <code>` there.

cq has removed the need for a browser on the agent machine, which was the actual
obstacle, without becoming a credential path.

**If that proves too awkward in practice** — an operator who is genuinely away from
the agent machine and wants to finish from a phone — the escape hatch is a queued
`orc.login.code` action, and it must be built with that trade-off stated out loud:
short expiry, single use, cleared from the queue the moment it applies, and never
written to the queue's log. It is deliberately not in the first version.

## §6 Watching it, so nobody has to

The point of §1 is that this fails silently. Three places catch it, and they are
already the three places a fleet is watched from:

- **`orc doctor`** grows the credential guard it now has, plus expiry: a token
  inside its last three days reads as a warning rather than as in-force.
- **The wake cycle**, which is the thing that runs on a timer, checks the credential
  once per pass and says so *once* when it is expiring — not once per agent, which
  would be a fleet's worth of identical lines.
- **cq**, as §5.2.

And one more, which is the honest one: **a session that stops at a login prompt
should be recognisable**. The supervisor cannot read the screen, but it can notice
that a session has produced no events at all since it started while the credential
is known to be bad, and say that rather than leaving `orc status` calling it live.

## §7 What it costs to get wrong

- **A token in a snapshot.** The snapshot goes to a server the operator may not
  control. Nothing about the credential except its *shape* may travel: no token, no
  code, no account password. The test for this is a test that greps a snapshot for
  the token and fails if it is there.
- **Overriding a deliberate credential.** An operator with `ANTHROPIC_API_KEY` set
  has chosen an account. Orc's stored token sits *behind* the environment, never in
  front of it.
- **A stale "it works".** `last seen to work` must be written when a session
  actually authenticates, not when `orc login` finished. A field that means "it was
  fine a month ago" printed as "fine" is worse than no field.
- **Clicking Authorize.** §2.

## §8 Milestones

| # | Delivers | Why this order |
|---|---|---|
| 1 | `orc login` and the stored credential; sessions inherit it | The mechanism, on its own, testable without cq |
| 2 | `orc login --status`, and `orc doctor` growing expiry | Diagnosis on the machine, before any of it is remote |
| 3 | The credential block in the fleet snapshot, with the no-token test | The contract cq reads; the test is the point |
| 4 | cq's row on `manage → fleet`, including `blocked` | Visibility, which is what was asked for |
| 5 | The URL hand-off in cq | Removes the browser requirement from the agent machine |
| 6 | The wake cycle's once-per-pass check | The timer that notices without anybody looking |

1 and 2 are worth having alone: they solve a headless machine over ssh. 3 and 4 are
the visibility. 5 is the convenience that makes a phone enough. 6 is what makes it
not need anybody at all.

## §9 Deliberately not in this

- **Automating the consent screen.** §2.
- **Per-session credentials.** One machine, one credential. An agent is not a
  separate Anthropic account, and pretending otherwise would mean a fleet's worth of
  logins to keep alive.
- **Refreshing a token by itself.** A token that can be renewed without a person is
  a credential with no human in the loop, which is the thing §2 is about. Orc warns;
  the operator runs one command.
- **cq starting the flow.** §5.

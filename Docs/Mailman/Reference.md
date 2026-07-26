# §1 Mailman — CLI

Mailman exposes the following CLI:

```
mailman <command> <args...>
```

| Command                             | Does                                                                       |
|-------------------------------------|----------------------------------------------------------------------------|
| `inbox [--all\|--sent]`              | Show unread messages, marked with `*` — *all* messages with `--all`, or your own outgoing mail with `--sent` |
| `open <query>`                      | Open the most recent message matching `<query>`                            |
| `convo <convo UID> [--all]`         | The same screen as `inbox`, for the messages in one conversation           |
| `send <subject> <to...> <content>`  | Send a message to all `to`s                                                |
| `reply <query> <subject> <content>` | Reply within the matched message's conversation                            |
| `archive [query]`                   | Archive everything matching `<query>`, or show the archive inbox           |
| `prune <query>`                     | Delete everything in the archive matching `<query>`                        |
| `read <query>`                      | Mark everything matching `<query>` as read in inbox                        |
| `check <query>`                     | Check who has and has not read the messages matching `<query>`             |
| `cc <query> <user>`                 | Add `<user>` to the conversation matched by `<query>`                      |
| `verify`                            | Check the store for damage, reporting without changing anything           |
| `admin user add\|remove\|list`       | Provisioning stand-in until Orc remote auth lands                         |

`inbox` lists by persistent unique identifier (puid), then timestamp, then
sender, then subject, then conversation id — convo title, then convo UID, then
message index, all optionally.

`reply` starts a conversation rooted on the given message, if need be.

`read` is visible to all recipients of that message.

`cc` works via a special email, so `mailman check` works on it. It also adds the
user to the conversation itself, which means two things: they can read the whole
thread with `convo`, including messages sent before they joined, and later
replies reach them even when the message being replied to predates them.

## §1.1 Conversations

A conversation has a stored participant list, separate from any one message's
recipients. `reply` addresses that list; `cc` extends it; `convo` requires
membership in it. A non-member asking for a conversation is told it does not
exist rather than that they may not see it, so the command cannot be used to
discover what conversations are going on.

A thread shows every message in it, and marks the ones the reader was not
personally sent with `·` and an id of `—`: those have no puid, because nothing
was ever delivered to that reader.

`prune` is the only irreversible command. It refuses a query that reaches
anything outside the archive, prints what it will delete, and needs `--yes`
when standard input is not a terminal — which, for an agent, is always.

## §1.2 Identity

Accounts are controlled via Orc remote auth. Mailman issues no identity and
holds no session: it reads a credential from the environment and verifies it on
every command.

| Variable    | Holds                      |
|-------------|----------------------------|
| `$ORC_USER` | the mailbox to act as      |
| `$ORC_KEY`  | the key that proves it     |

Both must be set together. `help` is the only command that does not
authenticate.

## §1.3 Beyond the core

Two commands sit outside the set above. Both are additive: nothing else depends
on them, and neither changes how mail is sent or read.

`verify` walks the store, replays every journal, and reconciles read receipts,
reporting what is wrong without repairing anything. A store several unsupervised
agents write to needs some way to answer "is this healthy?".

`admin user` creates and removes mailboxes, printing a fresh key once on
creation. It exists because Orc remote auth does not yet, and it writes exactly
the records Orc will write — so it can be deleted once Orc writes them itself.

## §1.4 Queries

A query selects messages by any of the fields `inbox` lists, and subqueries
combine with `&` and `|`:

```
mailman open from="boss"                        -> most recent message from boss
mailman open 'from="boss" & subject="RE: work"' -> most recent from boss with that subject
mailman open id="0"                             -> message with puid 0
```

Fields: `id`, `mid`, `kind`, `from`, `to`, `cc`, `any`, `subject`, `body`,
`convo`, `title`, `index`, `unread`, `archived`, `before`, `after`.

| Operator | Means                                              |
|----------|----------------------------------------------------|
| `=`      | equal — for `to` and `cc`, "is a recipient"        |
| `!=`     | not equal — for `to` and `cc`, "is not a recipient"|
| `~`      | contains, ignoring case                            |

Subqueries group with `()` and negate with `!`. `|` binds loosest, then `&`,
then `!`. An unknown field is always an error, never a term that quietly
matches nothing.

`open` takes the most recent match, as above; every other query command acts on
the *whole* match set.

## §1.5 JSON

`--json` prints the same information as a JSON array, for another program to
read. It is the contract Communiqué mirrors through, so it is a stable shape,
not a rendering.

| Command           | Gives                                   |
|-------------------|-----------------------------------------|
| `inbox`           | mail I was sent — `--all`, `--sent` too |
| `archive`         | mail I filed                            |
| `convo`           | one thread                              |
| `check`           | who has read what I sent                |
| `admin user list` | the accounts that exist                 |
| `admin mail`      | every message, and whose mailbox holds it |

Colour is off under `--json`: output meant for another program should not carry
escape sequences. `--no-bodies` withholds message bodies.

## §1.6 The whole store

`mailman admin mail` shows every message in the store, with the mailboxes that
hold it and who has read it. It is what Communiqué's admin panel is built on.

It is the one command that shows an account mail it was never sent, so it is the
one command with an owner:

```
mailman admin owner redjive   -> name who may read the store whole
mailman admin owner           -> show who that is
```

Anyone may name the first owner — a store nobody owns has nothing to check a
claim against, the same reason `admin user add` does not authenticate. After
that, only the owner may read the store whole or hand ownership on, and the
owner cannot be removed while they hold it.

Unowned fails closed: with no owner named, nobody may read it.

Provisioning is unauthenticated and stays that way; agents share an operating
system user, so file permissions separate nothing and a key is the only thing
that can.

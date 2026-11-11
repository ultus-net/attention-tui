# attention-tui design

Status: proposed, awaiting approval.
Date: 2026-11-12.

## Intent

A terminal pane that answers one question at a glance: does anything need me
right now?

The immediate cause is the Microsoft 365 web apps. Outlook on the web moved to
a public-client MSAL model, so with third-party cookies blocked the silent SSO
to Entra fails and the user is prompted to sign in again after 24 hours
(MC871011). Teams PWA has the same failure. A native process never performs
browser silent SSO, so it is structurally immune to that class of logout.

The second goal is attention cost. The pane sits in one tiling-window-manager
column for a whole working day, stays authenticated without intervention, and
stays quiet when nothing needs a human.

`rss-tui` at `~/Projects/rss-tui` already solves the same shape of problem for
feeds: a passive ticker plus an interactive view over one SQLite cache, one
YAML config, XDG paths, `notify-send`. This tool reuses that architecture and
its conventions rather than inventing new ones.

## Goals

- One process that stays signed in indefinitely with no user action after
  first login.
- Show only what the user has decided is worth seeing, with that decision fully
  configurable.
- Zero CPU between polls, near-zero network per poll.
- Never exit or block on an auth or network failure. Degrade and say so.
- Work in a narrow pane (40 columns) and in a wide one.

## Non-goals

- Composing new mail. Reply and forward stay in the browser client.
- Attachments, calendar, channels, or file shares.
- Marking items read on the server.
- Mobile, Windows, macOS packaging.

## Anti-goals

Eye-catching is a failure. A pane that animates, redraws constantly, or lights
up for routine traffic has made the user's day worse. Empty means empty.

## Constraints

Graph v1.0 facts this design depends on, verified against the v1.0 reference:

- `GET /me/chats?$expand=lastMessagePreview&$orderby=lastMessagePreview/createdDateTime desc`
  returns, per chat, `lastMessagePreview.createdDateTime` and
  `viewpoint.lastMessageReadDateTime`. Comparing the two tells us whether the
  user has unread messages in that chat, with no per-chat message call.
  `chatViewpoint` is populated only for the list-chats operation, in delegated
  context.
- `$top` caps at 50 for chats and for chat messages. `$orderby` is
  descending-only. `$filter` on chat messages is unreliable, so filtering stays
  client-side.
- `lastMessagePreview` carries no `mentions` array, so mention detection needs
  a `GET /me/chats/{id}/messages?$top=10` call. It is only made for chats that
  are already unread.
- Mail has a real delta query: `GET /me/mailFolders/inbox/messages/delta` with
  a stored `deltaLink`. `importance`, `flag.flagStatus`,
  `inferenceClassification`, `isRead`, `from`, `subject`, and `webLink` are all
  v1.0 properties.
- Chat messages cannot be sent with an app-only token. Sending needs a
  delegated token, which is what a per-user client holds anyway.
- Graph rate limits: roughly 10 messages per 10 seconds per conversation for
  chat, plus per-app throttling with `Retry-After`. A 60-second tick is orders
  of magnitude below both.
- `ChatMessage.Send` may require admin consent in some tenants. The intended
  operator is the tenant admin.

## Approaches considered

| | What it is | Trade | Verdict |
|---|---|---|---|
| A | One Go binary, one delegated token, one cache, polling loop, Bubble Tea UI plus passive renderer | One process to keep alive; auth failures surface in the pane | Chosen |
| B | Poller daemon owning auth, with a disposable renderer over a socket | Survives renderer crashes | Rejected: two moving parts and a protocol to debug, to fix a crash problem a supervisor already fixes |
| C | Startup script printing counts into a tmux status segment | Cheapest | Rejected: cannot show what is waiting, only how many |

A was picked by the operator. The tie-break against B is that state lives in
SQLite, so a crashed renderer costs nothing: the next instance is already
correct. The tie-break against C is that "what needs me" is the whole point.

## Design

### Modules

Mirrors `rss-tui`:

```
cmd/attention-tui/main.go     flags, wiring, --login, --once, --passive
internal/config               YAML load/save, XDG paths, sample config, validation
internal/auth                 auth code + PKCE, token cache, refresh
internal/graph                typed REST client, paging, retry, throttling
internal/rules                match rules, assign tier
internal/store                SQLite cache and watermarks
internal/ui                   Bubble Tea model, view, passive renderer
internal/notify               notify-send / osascript, urgency by tier
internal/theme                shared palettes, same names as rss-tui
```

`internal/config` copies the `rss-tui` patterns: `KnownFields(true)` so a typo
fails loudly, `Duration` decoding from `10m`, atomic save with a `.bak`, sample
config written on first run, and `Validate` rejecting values the rest of the
program cannot honour.

### Auth

One flow, two modes. Authorization code with PKCE against
`http://localhost:53147/callback`, opened by `attention-tui --login`.

- `auth.mode: public` (default) sends no secret. A native app cannot keep one.
- `auth.mode: confidential` sends a client secret read from the OS keyring.
  This is a deliberate choice for a single-user tool on the operator's own
  machine, not a pattern to distribute. It buys deferred sign-in-frequency
  enforcement on silent renewals, which is the whole point of the exercise.

Scopes: `offline_access Chat.Read Chat.ReadWrite ChatMessage.Send Mail.Read
User.Read`.

Token storage: OS keyring first, `$XDG_DATA_HOME/attention-tui/token.json` at
0600 as fallback for machines without a session bus. The rotated refresh token
is written before the new access token is used, so a crash mid-refresh cannot
lose the session.

Refresh is single-flight behind a mutex and an `flock` on the token file, so two
panes cannot race the same refresh.

The TUI itself never starts a login flow. Unauthenticated state renders one
line: `auth: no token - run attention-tui --login`. The process keeps running.

### Poll loop

One tick every `poll_interval` (default 60s, floor 30s, 10 percent jitter).

1. Mail: redeem the stored delta link. New and changed messages upsert into
   the cache. On `410 Gone` or an expired delta token, restart the delta from
   the inbox root and set a backfilling flag.
2. Chats: one list call with `$expand=lastMessagePreview`. A chat is unread
   when `lastMessagePreview.createdDateTime > viewpoint.lastMessageReadDateTime`.
   For unread chats only, fetch the newest 10 messages and upsert them.
3. Rules run over new items, assigning a tier. Notifications fire once per
   tick, coalesced, and only for tier `alert`.
4. Sleep until the next tick.

Failures never terminate the loop.

- `401` once: refresh, retry once. Still `401`: mark auth degraded, keep the
  last rendered state, surface the banner.
- `429` or `5xx`: honour `Retry-After`, back off 30s, doubling to 5m.
- Network and DNS errors: same backoff, no banner until the third consecutive
  failure.

Wall clock is re-read every tick, so suspend and resume costs one delayed tick
rather than a burst of catch-up polls.

### Rules and tiers

Three tiers, because two cannot express "show it but stay silent":

```yaml
tiers:
  alert: { notify: true,  show: true  }
  watch: { notify: false, show: true  }
  quiet: { notify: false, show: false }

defaults:
  mail: watch
  chat: quiet

rules:
  - name: mentions
    match: { mention: true }
    tier: alert

  - name: vip-senders
    match: { from: ["cfo@example.com", "*@legal.example.com"] }
    tier: alert

  - name: high-importance
    match: { importance: high }
    tier: alert

  - name: bots
    match: { chat: ["Deploy Bot", "#build-*"] }
    tier: quiet
```

Match fields: `source` (`mail` or `chat`), `from` (glob over address or display
name), `subject` (regular expression), `chat` (glob over chat name), `mention`,
`importance`, `focused`, `flagged`. An absent field matches everything. Glob is
case-insensitive and supports `*` only.

Evaluation order is file order, first match wins, and a rule with no `tier`
inherits the default for that source. First-match-wins keeps the config
readable: later rules are never silent overrides.

Tiers are evaluated at render time over cached items, not stored. Editing the
rules and pressing `R` reclassifies the existing list with no refetch.

Regexes are compiled at load. A bad pattern is a startup error naming the rule.

### Store

SQLite through `modernc.org/sqlite`, same as `rss-tui`, no cgo.

- `items(source, id, chat_id, sender, subject, preview, received_at, url,
  importance, focused, flagged, mention, raw_json, first_seen)`, primary key
  `(source, id)`.
- `chats(chat_id, name, last_message_at, last_read_at, web_url)`.
- `kv(key, value)` for the mail delta link, schema version, and backfill flag.

Retention: 7 days and at most 500 rows, pruned at the end of each tick.
`webUrl` and `webLink` are stored opaquely and never parsed.

### UI

Interactive model, one list, grouped by source:

```
 attention  3
 TEAMS
 * Ana Ruiz        @you can you look at the
   eng-platform    deploy before standup?
 MAIL
 ! CFO             Q4 numbers - need your signoff
   high            09:42
 j/k move   enter open   r reply   a all   / filter   R refresh   q quit
```

- `enter` opens the item's stored URL with the configured browser and marks it
  seen locally only.
- `r` on a chat item opens a one-line composer and posts with
  `POST /chats/{id}/messages`. Mail has no reply.
- `a` toggles `quiet` items into the list.
- `R` forces a poll and reclassifies.
- The header carries the count and an auth or network banner when degraded.

`--passive` renders a fixed-height block, no input, redrawn in place, for a
tmux or status pane. It is the mode this tool is actually for.

The theme list matches `rss-tui` (`catppuccin-mocha`, `dracula`, `nord`,
`gruvbox-dark`, `tokyo-night`, `solarized-dark`) so two panes in one window do
not clash. `NO_COLOR` disables colour.

Notification urgency maps to tier: `alert` is `normal`, everything else is
silent.

### Process behaviour

- `--once` performs one tick, prints the passive block, exits non-zero on
  failure. For cron and scripts.
- `--login` runs the auth flow and exits.
- `--passive` runs the loop in the ticker view.
- Default runs the interactive view.
- `SIGHUP` reloads config and rules without restarting.

## Testing

- `internal/rules`: table tests over every match field, first-match ordering,
  defaults, and inheritance. This is the requirement the operator called out, so
  it carries the most cases.
- `internal/config`: sample config parses, unknown field rejected, bad duration
  rejected, bad regex rejected naming the rule, atomic save leaves `.bak`.
- `internal/graph`: `httptest` server against fixtures, covering paging,
  `Retry-After`, 401 then refresh then retry, and delta `410` restart. Fixtures
  are trimmed real payloads with their source noted beside the test.
- `internal/store`: upsert idempotence, retention pruning, delta-link
  round-trip.
- `internal/ui`: passive renderer golden test at 40 and 100 columns; interactive
  model transitions without a terminal.

## Left to the builder

- App registration steps and the exact `--login` prompts.
- Whether `ChatMessage.Send` needs consent in the target tenant; the error path
  must say so plainly if it does.
- Keyring library choice (`zalando/go-keyring` is the smallest fit).
- Whether `--passive` should reserve a fixed height or shrink to content.
- Packaging: `go install`, a PKGBUILD, or a `Makefile` target, deferred until
  the tool earns it.

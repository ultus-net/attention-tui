# attention-tui

A terminal pane that answers one question: does anything need me right now?

attention-tui stays signed in to Microsoft Teams chats and Outlook mail, keeps
one SQLite cache, and renders only the rows your rules call attention-worthy.
It is read-only on the server: reading a row in the pane never writes read
state back to Teams or Outlook. The single exception is a reply you type into a
chat.

Status: 0.1.0, usable, single-instance, Linux and macOS. The design is in
`docs/superpowers/specs/2026-11-12-attention-tui-design.md`.

## Requirements

- Go 1.27.3 or newer to build. No cgo: SQLite is `modernc.org/sqlite`.
- A Microsoft Entra app registration in your tenant (below).
- `notify-send` for desktop notifications on Linux. macOS uses `osascript`.
- `xdg-open` (Linux) or `open` (macOS) to open a row.

## App registration

1. In the Entra admin center, open **App registrations** and create a new
   registration. Supported account types: **Accounts in any organizational
   directory**. Attention-tui refuses personal Microsoft accounts because Teams
   chat permissions reject them.
2. Under **Authentication**, add a platform of type **Mobile and desktop
   applications** and set the redirect URI to
   `http://localhost:53147/callback`. Enable **Allow public client flows**
   (`allowPublicClient`). A public client sends no secret.
3. Under **API permissions**, add these delegated Microsoft Graph permissions:
   `User.Read`, `Mail.Read`, `Chat.Read`, `ChatMessage.Send`, and
   `offline_access`. Then grant admin consent. `Chat.Read` and
   `ChatMessage.Send` need it in most tenants.
4. Copy the **Application (client) ID** into `auth.client_id` in the config.

Tenant admin consent is a one-time step. The default `auth.tenant` is
`organizations`, never `common`, because `common` admits personal accounts.

## Build and run

```bash
make build            # binary at ./attention-tui
./attention-tui --login
./attention-tui       # interactive pane
```

The first run writes a commented sample config to
`$XDG_CONFIG_HOME/attention-tui/config.yaml` and state to
`$XDG_DATA_HOME/attention-tui/state.db`.

Flags:

| Flag | Meaning |
| --- | --- |
| `--config PATH` | Use a specific config file |
| `--login` | Run the browser sign-in flow and exit |
| `--once` | Poll once, render one block, exit |
| `--passive` | Render a fixed-height block on a loop |
| `--version` | Print the version |

`SIGHUP` reloads the config and rebuilds the rule engine. A second instance
opens the same cache read-only and shows `secondary: cache only`; it never
polls and never writes.

## Configuration

Durations use Go syntax (`30s`, `10m`, `2h`). Polling is floored at 30s and
capped at 24h.

```yaml
poll_interval: 60s
passive_items: 20        # rows rendered by --passive and --once
backfill_days: 7         # cold-start history window
backfill_max: 200        # cold-start message cap

theme: catppuccin-mocha  # dracula, nord, gruvbox-dark, tokyo-night, solarized-dark
browser: ""              # empty uses the system opener
notifications: false     # desktop notification for alert-tier rows

auth:
  client_id: ""
  tenant: organizations
  mode: public           # confidential reads a secret from the OS keyring
  redirect_port: 53147
```

### Tiers

Three tiers ship by default. `notify` sends a desktop notification. `show`
renders the row.

| Tier | notify | show |
| --- | --- | --- |
| `alert` | true | true |
| `watch` | false | true |
| `quiet` | false | false |

### Rules

Rules are evaluated top to bottom at render time. The first match wins, so a
config edit takes effect on the next render without a refetch. A rule with no
`tier` inherits the default for its source. Press `a` in the pane to reveal
quiet rows.

```yaml
defaults:
  mail: watch
  chat: quiet

rules:
  - name: mentions
    match:
      mention: true
    tier: alert

  - name: vip-senders
    match:
      from:
        - cfo@example.com
        - "*@legal.example.com"
    tier: alert

  - name: high-importance
    match:
      importance: high
    tier: alert

  - name: bots
    match:
      chat:
        - Deploy Bot
        - "#build-*"
    tier: quiet
```

Match fields:

| Field | Applies to | Semantics |
| --- | --- | --- |
| `source` | both | `mail` or `chat` |
| `from` | both | Glob, case-insensitive; matches the whole header or the address |
| `subject` | mail | Case-insensitive regular expression |
| `chat` | chat | Glob on the chat display name |
| `mention` | chat | Boolean; `false` excludes mentions |
| `importance` | mail | `high`, `normal`, or `low` |
| `focused` | mail | Boolean |
| `flagged` | mail | Boolean |

A glob supports `*` only and matches the whole string. An absent field matches
everything.

### Keys

| Key | Action |
| --- | --- |
| `j` / `k` | Move the cursor |
| `enter` | Open the row and mark it seen locally |
| `a` | Show or hide quiet rows |
| `r` | Reply to the selected chat |
| `/` | Filter by name, sender, subject, or preview |
| `R` | Reload the cache |
| `q` | Quit |

### Passive pane

`--passive` renders a fixed-height block sized from `COLUMNS` and `LINES`, so
it can sit in a tmux pane:

```tmux
# ~/.tmux.conf
set -g status-interval 5
bind-key a split-window -h -l 44 'attention-tui --passive'
```

`--once` performs one poll and prints one block, which suits status scripts. On
a secondary instance it skips the poll: it prints `secondary: cache only` and
renders whatever the primary last cached.

## Behaviour worth knowing

- One row per chat, built from the newest unread message. One row per mail
  message.
- Cold start and delta restarts backfill at most 7 days and 200 messages, and
  never notify for that history.
- A notification fires only for an alert-tier row first seen within two poll
  intervals that has never notified.
- Graph is polled: `$top` is capped at 50 for chats and mail, 10 for messages.
- Tokens live in `$XDG_DATA_HOME/attention-tui/token.json` with mode 0600. A
  rotated refresh token is persisted before the new access token is used.
- A network or auth failure prints a one-line banner and keeps the process
  alive. `auth: no token; run attention-tui --login` means the refresh token
  was revoked or never issued.

## Known limits

Deliberately absent: channels and channel mentions, calendar, attachments,
mail compose, Teams file access, webhooks, and any server-side read marking.
Notifications are one coalesced message per poll, not one per item.

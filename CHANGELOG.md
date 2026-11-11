# Changelog

All notable changes to this project are recorded here. The format follows
Keep a Changelog, and the project adheres to Semantic Versioning.

## [0.1.0] - 2026-11-12

### Added

- One Go binary with one delegated Graph token and one SQLite cache, polling
  Teams chats and Outlook inbox mail. No webhooks, no Graph SDK, no cgo.
- Authorization-code + PKCE sign-in on `http://localhost:53147/callback`, with
  file or OS-keyring token storage, single-flight refresh, and refresh-token
  rotation persisted before use.
- Mail delta walk with `410` restart, bounded 7-day / 200-message backfill, and
  HTML preview flattening.
- Chat polling with `lastMessagePreview`, a local watermark, clock-skew
  clamping, one row per chat, and mention detection.
- Render-time rule engine: three tiers, first match wins, glob senders and chat
  names, subject regex, importance, focused and flagged matching.
- Bubble Tea pane with cursor, tier reveal, filter, local seen marking, and
  Teams chat replies.
- Fixed-height passive renderer for `--passive` and `--once`, sized from the
  terminal, falling back to `COLUMNS` and `LINES`.
- The passive block redraws in place with a cursor-up clear instead of
  clearing the whole screen.
- A piped `--passive` prints one plain block and exits, with no escape
  sequences.
- The configured palette colours the pane header, tier tags, alert rows, and
  passive footer. `NO_COLOR`, `TERM=dumb`, and a non-terminal writer turn
  colour off.
- Desktop notifications for alert-tier rows, coalesced to one message per poll.
- Single-instance `flock`; a second instance runs cache-only and says so.
- `SIGHUP` reloads the config and rebuilds the rules engine.
- Network and auth failures degrade to a one-line banner; the process never
  exits on them.
- Conditional Access claims challenges from a `401` are answered with a
  claims-aware token refresh. When the tenant demands interactive re-auth, the
  challenge is persisted and the banner points at `--login`, which carries the
  challenge into the sign-in URL.
- Sample config written on first run, six shared palettes, README with app
  registration steps, and a Makefile.
- The SQLite cache records `PRAGMA user_version`. A database written by a newer
  build is refused with an explanation instead of being read as if it were this
  schema.
- The pane filter matches characters in order, so `anl` finds `Ana Lima`. It
  still ignores case and does not match a rune out of order.
- `--theme NAME` overrides the configured palette, accepts the same aliases as
  the config, and is reapplied after a `SIGHUP` reload.
- A full chat walk asks for `$expand=lastMessagePreview,members`, so most chats
  are named without a per-chat member call. The per-chat call stays as the
  fallback when the expansion is absent.
- A chat with no last-message preview is skipped rather than treated as read.
  The read state is unknown, not false.

### Fixed

Review-round corrections before the first release:

- Notification stamping applies only to alert-tier rows; rows the rules keep
  quiet stay unstamped.
- The mail delta link advances only after a tick commits, so a failed tick
  replays the delta instead of skipping mail.
- A newer message on a chat re-arms its row, clearing seen and notified state.
- The filter and reply prompts accept every backspace key a terminal may send.
- The Graph transport refuses an absolute `nextLink` or `deltaLink` on another
  origin, and refuses cross-origin redirects, so the bearer token cannot leave
  the configured Graph origin.
- Mail delta `@removed` entries delete the cached row immediately, so a message
  read, moved, or deleted out of the inbox disappears now rather than at the
  retention point.
- The mail delta asks for pages with `Prefer: odata.maxpagesize=1000` and
  immutable IDs. The endpoint ignores `$top`, so the old `$top=50` did nothing.
- A 1:1 chat is named after the other participant; the signed-in user is no
  longer listed in the chat name.
- A chat row is flagged as a mention only when the mentioning message is unread.

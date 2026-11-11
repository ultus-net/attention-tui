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
- Fixed-height passive renderer for `--passive` and `--once`, sized from
  `COLUMNS` and `LINES`.
- Desktop notifications for alert-tier rows, coalesced to one message per poll.
- Single-instance `flock`; a second instance runs cache-only and says so.
- `SIGHUP` reloads the config and rebuilds the rules engine.
- Network and auth failures degrade to a one-line banner; the process never
  exits on them.
- Sample config written on first run, six shared palettes, README with app
  registration steps, and a Makefile.

### Fixed

Review-round corrections before the first release:

- Notification stamping applies only to alert-tier rows; rows the rules keep
  quiet stay unstamped.
- The mail delta link advances only after a tick commits, so a failed tick
  replays the delta instead of skipping mail.
- A newer message on a chat re-arms its row, clearing seen and notified state.
- The filter and reply prompts accept every backspace key a terminal may send.

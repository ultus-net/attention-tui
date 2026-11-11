# attention-tui Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A single Go binary that keeps a delegated Microsoft Graph session alive and renders an always-on, rule-filtered attention pane over Teams chats and Outlook mail.

**Architecture:** One process, one delegated token, one SQLite cache. A poll loop redeems a mail delta link and lists chats with `lastMessagePreview`; a rules engine assigns each cached row a tier at render time; a passive renderer and a Bubble Tea model both read that same cache. No Graph SDK, no webhooks, no server-side read writes.

**Tech Stack:** Go 1.27.3, `modernc.org/sqlite` (pure Go, no cgo), `charmbracelet/bubbletea` and `lipgloss`, `gopkg.in/yaml.v3`, `zalando/go-keyring`.

**Spec:** `docs/superpowers/specs/2026-11-12-attention-tui-design.md`

## Global Constraints

- Go 1.27.3 in `go.mod`; module path `github.com/ultus-net/attention-tui`.
- No Graph SDK. Hand-rolled REST over `net/http`.
- No cgo. SQLite is `modernc.org/sqlite`.
- All configuration is read through `internal/config`. No bare `os.Getenv` outside that package.
- Tokens are never logged. `token.json` is written 0600. A rotated refresh token is persisted before the new access token is used.
- No write to any Graph read state. `seen` is local only.
- Errors are wrapped with `%w` or typed. No swallowed errors, no log-and-continue.
- Graph caps: `$top` 50 for chats and mail, 10 for per-chat messages. Never request more.
- Poll floor 30s. Retention 7 days and 500 rows. Backfill window 7 days and 200 messages.
- Notification eligibility: notify-tier row, `notified_at` null, `first_seen` within two poll intervals.
- Default tenant is `organizations`, never `common`.
- Every commit covers one logical change and leaves `go test ./...` and `go vet ./...` clean.

## Review Focus

Failure modes the spec implies but no single task's happy path exercises. Each gets a test in the task named here.

1. **Cold start, empty cache, Graph unreachable.** The pane must render `attention 0` plus the network banner and stay alive. Tested in Task 10.
2. **A 40-column pane with long accented previews.** Truncation must happen on a rune boundary and the block must keep its exact height. Tested in Task 11.
3. **A message edited in place.** Same `(source, id)`, new preview: the row updates and `notified_at` is not cleared, so it cannot notify twice. Tested in Task 3.
4. **A chat whose `lastMessagePreview` timestamp is in the future.** Clock skew must not pin the chat to unread forever. Tested in Task 6.
5. **Mail `bodyPreview` carrying HTML entities and tags.** Must flatten to one plain line before it reaches the pane. Tested in Task 5.

## File Structure

```
go.mod, .gitignore, LICENSE, README.md, CHANGELOG.md, Makefile
cmd/attention-tui/main.go        flags, wiring, process loop, signal handling
internal/config/config.go        YAML, XDG paths, sample config, validation
internal/rules/rules.go          tiers, match, first-match-wins, render-time eval
internal/store/store.go          SQLite schema, upsert, seen, notify eligibility, retention
internal/graph/client.go         typed REST client, paging, retry, 401 refresh hook
internal/graph/mail.go           inbox delta, 410 restart, bounded backfill
internal/graph/chats.go          chat list, unread compare, messages, member names
internal/auth/auth.go            auth code + PKCE, token store, refresh
internal/notify/notify.go        notify-send / osascript, notification format
internal/theme/theme.go          palettes shared with rss-tui
internal/ui/model.go             Bubble Tea model, keys, banner
internal/ui/passive.go           fixed-height renderer for --passive and --once
```

---

### Task 1: Project skeleton and config

**Files:**
- Create: `go.mod`, `.gitignore`, `LICENSE`, `CHANGELOG.md`, `README.md`
- Create: `internal/config/config.go`, `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config`, `config.Auth`, `config.Tier`, `config.Rule`, `config.Match`, `config.Duration`, `config.Load(path string) (*Config, bool, error)`, `config.Save(path string, cfg *Config) error`, `config.DefaultPath() (string, error)`, `config.DataDir() (string, error)`.

- [ ] **Step 1: Write the failing test**

`internal/config/config_test.go`, table-driven over `parse`, plus path and validation cases:

```go
func TestSampleConfigParses(t *testing.T)      // Load(tempPath) creates file, returns created=true, Validate passes
func TestUnknownFieldRejected(t *testing.T)    // yaml with "pasive_items" fails, error names the field
func TestDurationParsing(t *testing.T)         // "90s" -> 90s, 45 -> 45s, "banana" -> error
func TestPollIntervalFloor(t *testing.T)       // 10s rejected, 30s accepted, 25h rejected
func TestTenantDefault(t *testing.T)           // empty tenant becomes "organizations"
func TestSaveLeavesBackup(t *testing.T)        // second Save leaves path+".bak" with the first content
func TestPaths(t *testing.T)                   // DefaultPath ends config.yaml; DataDir ends attention-tui
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestSampleConfigParses -v`
Expected: FAIL, `package github.com/ultus-net/attention-tui/internal/config is not in std` or `undefined: Load`.

- [ ] **Step 3: Implement `internal/config/config.go`**

Mirror `~/Projects/rss-tui/internal/config/config.go`: `Duration` with YAML marshal/unmarshal, `KnownFields(true)`, atomic save with `.bak`, sample config written on first run, `Validate` rejecting the spec's out-of-range values. Types:

```go
type Auth struct { ClientID, Tenant, Mode string; RedirectPort int }
type Tier struct { Notify bool; Show bool }
type Match struct { Source string; From []string; Subject string; Chat []string; Mention *bool; Importance string; Focused *bool; Flagged *bool }
type Rule struct { Name string; Match Match; Tier string }
type Config struct {
    PollInterval Duration
    PassiveItems int
    BackfillDays int
    BackfillMax int
    Theme string
    Browser string
    Notifications bool
    Auth Auth
    Tiers map[string]Tier
    Defaults map[string]string
    Rules []Rule
}
```

Sample config carries the three tiers, `defaults: {mail: watch, chat: quiet}`, and the four example rules from the spec. Defaults applied in `applyDefaults`: poll 60s, passive items 20, theme `catppuccin-mocha`, backfill 7 days and 200 messages, `auth.mode` `public`, `auth.redirect_port` 53147, `auth.tenant` `organizations`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -v`
Expected: PASS, all seven tests.

- [ ] **Step 5: Commit**

```bash
git add go.mod .gitignore LICENSE CHANGELOG.md README.md internal/config
git commit -m "feat: config package and project skeleton"
```

---

### Task 2: Rules engine

**Files:**
- Create: `internal/rules/rules.go`, `internal/rules/rules_test.go`

**Interfaces:**
- Consumes: `config.Config`, `config.Tier`, `config.Rule`, `config.Match`.
- Produces: `rules.Item` (Source, ID, ChatID, ChatName, From, Subject, Preview, Importance, Focused, Flagged, Mention, Received, URL, Seen), `rules.Verdict{Name string; Show bool; Notify bool}`, `rules.New(cfg *config.Config) (*Engine, error)`, `(*Engine) Eval(it Item) Verdict`.

- [ ] **Step 1: Write the failing test**

```go
func TestFirstMatchWins(t *testing.T)          // mention rule before bot rule: a mention in a bot chat is alert
func TestDefaultsWhenNoRuleMatches(t *testing.T)// mail -> watch, chat -> quiet
func TestGlobFrom(t *testing.T)                // "*@legal.example.com" matches Ana <ana@legal.example.com>
func TestGlobIsAnchored(t *testing.T)          // "cfo@example.com" does not match "cfo@example.com.evil"
func TestSubjectRegexCaseInsensitive(t *testing.T) // "invoice" matches "Q4 INVOICE"
func TestAbsentFieldMatchesEverything(t *testing.T) // empty Match matches
func TestMentionFalseIsNotWildcard(t *testing.T)// mention: false excludes mentions
func TestBadTierNameRejected(t *testing.T)     // rule tier "urgent" not in tiers -> error names rule
func TestBadRegexRejected(t *testing.T)        // subject "(" -> error names rule
func TestImportanceAndFlags(t *testing.T)      // importance high, flagged true, focused true
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/rules/ -run TestFirstMatchWins -v`
Expected: FAIL, `undefined: New`.

- [ ] **Step 3: Implement `internal/rules/rules.go`**

`New` compiles every `subject` regex once and validates every rule tier against `cfg.Tiers`. `Eval` walks rules in file order, returns the first verdict where every present field matches, else the default for `Source`. Glob: case-insensitive, `*` is the only metacharacter, full-string match. Allocates nothing per call beyond the verdict.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/rules/ -v`
Expected: PASS, ten tests.

- [ ] **Step 5: Commit**

```bash
git add internal/rules
git commit -m "feat: rule engine with tiers and first-match-wins"
```

---

### Task 3: Store

**Files:**
- Create: `internal/store/store.go`, `internal/store/store_test.go`

**Interfaces:**
- Consumes: `rules.Item`.
- Produces: `store.Open(path string) (*Store, error)`, `(*Store) Close() error`, `UpsertItems(ctx, []rules.Item) (int, error)`, `Items(ctx) ([]rules.Item, error)`, `SetSeen(ctx, source, id string) error`, `PendingNotifications(ctx, window time.Duration) ([]rules.Item, error)`, `KV(ctx, key string) (string, bool, error)`, `SetKV(ctx, key, value string) error`, `Prune(ctx, olderThan time.Time, max int) (int, error)`, `ChatName(ctx, chatID string) (string, bool, error)`, `SetChatName(ctx, chatID, name string) error`, `ChatWatermark(ctx, chatID string) (time.Time, error)`, `SetChatWatermark(ctx, chatID string, t time.Time) error`.

- [ ] **Step 1: Write the failing test**

```go
func TestUpsertIsIdempotent(t *testing.T)      // same (source,id) twice -> 1 inserted, 1 row
func TestEditKeepsSeenAndNotified(t *testing.T) // REVIEW FOCUS 3: change preview only -> seen and notified_at survive
func TestSeenFilters(t *testing.T)             // Items returns unseen first; SetSeen removes it from the count
func TestPendingNotificationsOnce(t *testing.T) // two calls, second returns empty
func TestPendingNotificationsWindow(t *testing.T)// first_seen 3 intervals old -> not returned
func TestPrune(t *testing.T)                   // 600 rows -> 500 kept, oldest dropped
func TestKVRoundTrip(t *testing.T)             // delta link survives reopen
func TestChatWatermark(t *testing.T)           // set, reopen, read back
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestUpsertIsIdempotent -v`
Expected: FAIL, `undefined: Open`.

- [ ] **Step 3: Implement `internal/store/store.go`**

Single file, `database/sql` over `modernc.org/sqlite`, schema created in `Open` with `CREATE TABLE IF NOT EXISTS`. `items` primary key `(source, id)`; the conflict clause updates content columns and leaves `seen`, `notified_at`, `first_seen` untouched. `PendingNotifications` selects notify-eligible rows in one statement and sets `notified_at` inside the same transaction. Enable `PRAGMA journal_mode=WAL` and `PRAGMA busy_timeout=5000`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -v`
Expected: PASS, eight tests.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat: sqlite cache with notify eligibility and retention"
```

---

### Task 4: Graph client core

**Files:**
- Create: `internal/graph/client.go`, `internal/graph/client_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `graph.TokenSource` (`Token(ctx context.Context) (string, error)`), `graph.New(baseURL string, ts TokenSource) *Client`, `(*Client) get(ctx, url string, out any) error`, and the shared error values `graph.ErrUnauthorized`, `graph.ErrGone`. `New` with an empty `baseURL` means `https://graph.microsoft.com/v1.0`.

- [ ] **Step 1: Write the failing test**

```go
func TestRetriesOn429WithRetryAfter(t *testing.T) // first 429 Retry-After: 0, then 200 -> one body, two requests
func TestRetriesOn503(t *testing.T)               // 503 then 200 -> success
func TestUnauthorizedRefreshesOnce(t *testing.T)  // 401, Token() called again, retry 200
func TestUnauthorizedTwiceReturnsErrUnauthorized(t *testing.T)
func TestGoneMapsToErrGone(t *testing.T)          // 410 -> errors.Is(err, ErrGone)
func TestErrorBodyIsSurfaced(t *testing.T)        // 403 with graph error JSON -> error contains "AccessDenied"
func TestContextCancelledStopsRetry(t *testing.T) // cancelled ctx -> returns ctx.Err()
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/graph/ -run TestRetriesOn429 -v`
Expected: FAIL, `undefined: New`.

- [ ] **Step 3: Implement `internal/graph/client.go`**

One `do` function owns the loop: `Authorization: Bearer` header, `Retry-After` honoured on 429 and 5xx (seconds or HTTP date, cap 300s), at most 2 retries, one forced token refresh on a lone 401. Wrap every non-2xx as a typed `graphError{Status, Code, Message}` so callers can `errors.Is` against `ErrUnauthorized` and `ErrGone`. Decode with `json.Unmarshal` into the caller's struct.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/graph/ -v`
Expected: PASS, seven tests.

- [ ] **Step 5: Commit**

```bash
git add internal/graph
git commit -m "feat: graph client with retry and one-shot refresh"
```

---

### Task 5: Mail delta

**Files:**
- Create: `internal/graph/mail.go`, `internal/graph/mail_test.go`

**Interfaces:**
- Consumes: `(*Client).get`.
- Produces: `graph.MailMessage{ID, Sender, Subject, BodyPreview, WebLink, Importance string; Focused, Flagged, IsRead, HasAttachments bool; Received time.Time}`, `graph.MailDeltaResult{Messages []MailMessage; NextLink, DeltaLink string; Restart bool}`, `(*Client) MailDelta(ctx, url string) (MailDeltaResult, error)`, `graph.FlattenPreview(s string) string`.

- [ ] **Step 1: Write the failing test**

```go
func TestMailDeltaReturnsDeltaLink(t *testing.T)   // body with @odata.deltaLink -> DeltaLink set, Restart false
func TestMailDeltaFollowsNextLink(t *testing.T)    // NextLink set, DeltaLink empty
func TestMailDeltaGoneRestarts(t *testing.T)       // 410 -> Restart true, nil error
func TestFlattenStripsTagsAndEntities(t *testing.T)// REVIEW FOCUS 5: "<p>Q4 &amp; Q3&nbsp;numbers</p>" -> "Q4 & Q3 numbers"
func TestFlattenCollapsesWhitespace(t *testing.T)  // tabs and newlines become single spaces
func TestMailDeltaSelectsOnlyNeededFields(t *testing.T) // request URL contains $select with webLink and importance
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/graph/ -run TestMailDeltaReturnsDeltaLink -v`
Expected: FAIL, `undefined: MailDelta`.

- [ ] **Step 3: Implement `internal/graph/mail.go`**

Request `me/mailFolders/inbox/messages/delta` with `$top=50` and a `$select` of `id,subject,from,bodyPreview,webLink,importance,receivedDateTime,isRead,inferenceClassification,flag,hasAttachments`. Map `flag.flagStatus == "flagged"` and `inferenceClassification == "focused"`. `Restart` comes from `errors.Is(err, ErrGone)`, which the caller turns into a root delta walk. `FlattenPreview` runs `html.UnescapeString`, strips tags with a small scanner (no regex dependency), collapses whitespace, and trims. Never parse `webLink`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/graph/ -v`
Expected: PASS, six new tests.

- [ ] **Step 5: Commit**

```bash
git add internal/graph/mail.go internal/graph/mail_test.go
git commit -m "feat: inbox delta fetch with preview flattening"
```

---

### Task 6: Chat polling

**Files:**
- Create: `internal/graph/chats.go`, `internal/graph/chats_test.go`

**Interfaces:**
- Consumes: `(*Client).get`.
- Produces: `graph.Chat{ID, Topic, ChatType, WebURL string; LastMessageAt, LastReadAt time.Time}`, `(*Client) Chats(ctx, pageURL string) ([]Chat, string, error)`, `graph.Message{ID, ChatID, Sender, Body, WebURL string; Created time.Time; Mention bool}`, `(*Client) ChatMessages(ctx, chatID string, top int) ([]Message, error)`, `(*Client) ChatMemberNames(ctx, chatID string) ([]string, error)`, `graph.ChatName(ch Chat, names []string) string`, `graph.IsUnread(ch Chat, watermark, now time.Time) bool`.

- [ ] **Step 1: Write the failing test**

```go
func TestChatsParsesPreviewAndReadTime(t *testing.T) // lastMessagePreview.createdDateTime and viewpoint.lastMessageReadDateTime
func TestChatsReturnsNextLink(t *testing.T)
func TestIsUnreadComparesPreviewToRead(t *testing.T) // preview 10:00, read 09:00 -> true; read 10:05 -> false
func TestLocalWatermarkSuppressesFuturePreview(t *testing.T) // REVIEW FOCUS 4: preview now+2h, watermark now, read 09:00 -> false
func TestChatMessagesMentionFlag(t *testing.T)       // a message with a mentions array -> Mention true
func TestChatMessagesDefaultTop(t *testing.T)        // top 0 becomes 10, never above 10
func TestMemberNamesSkipEmpty(t *testing.T)          // two members, one blank displayName -> one name
func TestChatNameFromTopicElseMembers(t *testing.T)  // topic wins; empty topic falls back to joined member names
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/graph/ -run TestIsUnreadComparesPreviewToRead -v`
Expected: FAIL, `undefined: IsUnread`.

- [ ] **Step 3: Implement `internal/graph/chats.go`**

`Chats` requests `me/chats` with `$top=50`, `$expand=lastMessagePreview`, and `$orderby=lastMessagePreview/createdDateTime desc`. `IsUnread` is `preview.After(effectiveRead)` where `effectiveRead` is the later of `ch.LastReadAt` and the local `watermark`, and a preview more than five minutes in the future is clamped to `now`. `ChatMessages` requests `me/chats/{id}/messages` with `$top=10` and sets `Mention` when `mentions` is non-empty. `ChatMemberNames` requests `me/chats/{id}/members` and skips blank `displayName`. `ChatName(ch, names)` returns `Topic` when non-blank, else the names joined with `, `.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/graph/ -v`
Expected: PASS, eight new tests.

- [ ] **Step 5: Commit**

```bash
git add internal/graph/chats.go internal/graph/chats_test.go
git commit -m "feat: chat polling with unread compare and name derivation"
```

---

### Task 7: Auth

**Files:**
- Create: `internal/auth/auth.go`, `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: `config.Auth`.
- Produces: `auth.Tokens{AccessToken, RefreshToken string; Expiry time.Time}`, `auth.Store` (`Load() (Tokens, bool, error)`, `Save(Tokens) error`), `auth.NewFileStore(path string) Store`, `auth.NewKeyringStore(service, user string) Store`, `auth.Manager` with `NewManager(cfg config.Auth, scopes []string, st Store) *Manager`, `(*Manager) Token(ctx) (string, error)`, `(*Manager) Login(ctx, open func(string) error) error`, and `auth.ErrNoToken`.

- [ ] **Step 1: Write the failing test**

```go
func TestPKCEChallengeIsS256(t *testing.T)        // challenge equals base64url(sha256(verifier)) without padding
func TestFileStoreRoundTrip(t *testing.T)         // save, load, equal fields
func TestFileStorePermissions(t *testing.T)       // file mode 0600 after Save
func TestExpiredTokenRefreshes(t *testing.T)      // Expiry in the past -> one POST to token endpoint
func TestRefreshRotationPersistedFirst(t *testing.T) // new refresh token written before the access token is returned
func TestRefreshIsSingleFlight(t *testing.T)      // ten concurrent Token() calls -> one token-endpoint hit
func TestRevokedRefreshReturnsErrNoToken(t *testing.T) // invalid_grant -> errors.Is(err, ErrNoToken)
func TestMissingTokenReturnsErrNoToken(t *testing.T)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/auth/ -run TestPKCEChallengeIsS256 -v`
Expected: FAIL, `undefined: NewManager`.

- [ ] **Step 3: Implement `internal/auth/auth.go`**

`Login` builds a verifier and S256 challenge, starts `http.Server` on `127.0.0.1:redirect_port`, opens the authorize URL through the injected `open`, waits for `code`, exchanges it at `/{tenant}/oauth2/v2.0/token`, and shuts the server down. `Token` returns the cached access token with 60 seconds of slack, else refreshes under one `sync.Mutex` and one `flock` on `<path>.lock`. `Save` writes to `path+".tmp"` at 0600 then renames. `invalid_grant` maps to `ErrNoToken` so the UI can print the one-line banner. `mode: confidential` adds `client_secret` from the keyring to token requests.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/auth/ -v`
Expected: PASS, eight tests.

- [ ] **Step 5: Commit**

```bash
git add internal/auth
git commit -m "feat: pkce login, token store, single-flight refresh"
```

---

### Task 8: Notifications and theme

**Files:**
- Create: `internal/notify/notify.go`, `internal/notify/notify_test.go`
- Create: `internal/theme/theme.go`, `internal/theme/theme_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `notify.Alert{Count int; Titles []string}`, `notify.New(enabled bool) *Notifier`, `(*Notifier) Send(ctx context.Context, a Alert) error`, `notify.Format(a Alert) (title, body string)`, `notify.ErrUnavailable`; `theme.Palette` with fields `Accent, Muted, Alert, Mail, Chat, Border lipgloss.Color`, `theme.Palette(name string) Palette`, `theme.Names() []string`.

- [ ] **Step 1: Write the failing test**

```go
func TestFormatSingularAndPlural(t *testing.T)  // 1 -> "1 item needs you", 3 -> "3 items need you"
func TestFormatCapsTitlesAtThree(t *testing.T)  // 5 titles -> three plus "and 2 more"
func TestDisabledIsSilent(t *testing.T)         // Send with enabled=false returns nil and runs nothing
func TestMissingNotifierIsQuiet(t *testing.T)   // LookPath fails -> ErrUnavailable, no panic
func TestPaletteNames(t *testing.T)             // six names, catppuccin-mocha first
func TestUnknownPaletteFallsBack(t *testing.T)  // "nope" -> catppuccin-mocha
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/notify/ ./internal/theme/ -run TestFormat -v`
Expected: FAIL, `undefined: Format`.

- [ ] **Step 3: Implement both packages**

Copy the structure of `~/Projects/rss-tui/internal/notify/notify.go`: `Runner func`, injectable `look` and `run`, `notify-send` on Linux, `osascript` on macOS, 5-second timeout, `Format` pure and table-testable. Urgency is `normal` for alerts, nothing else is sent. Theme mirrors `~/Projects/rss-tui/internal/theme/theme.go` with the same six palette names so both panes match.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/notify/ ./internal/theme/ -v`
Expected: PASS, six tests.

- [ ] **Step 5: Commit**

```bash
git add internal/notify internal/theme
git commit -m "feat: desktop notifications and shared palettes"
```

---

### Task 9: Tick runner

**Files:**
- Create: `internal/app/tick.go`, `internal/app/tick_test.go`

**Interfaces:**
- Consumes: `graph.*`, `store.*`, `rules.*`, `config.Config`.
- Produces: `app.Source` interface (`Chats`, `ChatMessages`, `ChatMemberNames`, `MailDelta`), `app.Runner` with `NewRunner(cfg *config.Config, st *store.Store, src Source) *Runner`, `(*Runner) Tick(ctx context.Context) (TickResult, error)`, `app.TickResult{NewMail, NewChats int; Alerts []rules.Item}`, `(*Runner) Backoff() time.Duration`.

- [ ] **Step 1: Write the failing test**

```go
func TestTickFetchesMailAndChats(t *testing.T)        // one delta message and one unread chat -> both in the store
func TestChatCollapsesToOneItem(t *testing.T)         // ten messages in one chat -> one row, mention true if any mentions
func TestMailReadMarksSeen(t *testing.T)              // isRead true in delta -> row is seen
func TestBackfillFiresNoAlerts(t *testing.T)          // Restart true with 50 messages -> zero alerts
func TestBackfillIsBounded(t *testing.T)              // 400 messages -> 200 inserted
func TestFirstTickPagesChats(t *testing.T)            // nextLink returned -> second Chats call, third tick does not page
func TestNetworkErrorDoesNotStopRunner(t *testing.T)  // error -> Tick returns error, next Tick still calls source
func TestBackoffGrows(t *testing.T)                   // 30s, 60s, 120s, capped at 5m
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/ -run TestChatCollapsesToOneItem -v`
Expected: FAIL, `undefined: NewRunner`.

- [ ] **Step 3: Implement `internal/app/tick.go`**

`Tick` performs the four steps from the spec in order: mail delta (root walk when `Restart` or when no stored link, bounded by 7 days and 200 messages, `backfill` suppresses alerts), chat list (full page walk on the first tick and every 30th, first page otherwise), per-chat message fetch for chats `graph.IsUnread` reports, then `UpsertItems` in one transaction. Chat rows use the newest unread message and OR the mention flags across the fetched ten. `nonnil` helpers keep `Rules` empty-safe. `Backoff` doubles from 30s on each consecutive failure and resets on success.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/app/ -v`
Expected: PASS, eight tests.

- [ ] **Step 5: Commit**

```bash
git add internal/app
git commit -m "feat: tick runner with bounded backfill and chat collapsing"
```

---

### Task 10: Interactive UI

**Files:**
- Create: `internal/ui/model.go`, `internal/ui/view.go`, `internal/ui/ui_test.go`

**Interfaces:**
- Consumes: `store.Store`, `rules.Engine`, `config.Config`.
- Produces: `ui.Deps{Store *store.Store; Engine *rules.Engine; Cfg *config.Config; Open func(string) error; Send func(ctx context.Context, chatID, text string) error; Banner string}`, `ui.New(d Deps) Model` implementing `tea.Model`, and `(*Model) SetBanner(string)`.

- [ ] **Step 1: Write the failing test**

```go
func TestCursorMovesWithJK(t *testing.T)          // j then k returns to the first row
func TestEnterOpensAndMarksSeen(t *testing.T)     // Open receives webLink, row disappears from the list
func TestARevealsQuietRows(t *testing.T)          // quiet row hidden, then visible after "a"
func TestBannerRendersWhenSet(t *testing.T)       // REVIEW FOCUS 1: banner "auth: no token" appears in View()
func TestEmptyCacheRendersZero(t *testing.T)      // no rows -> header contains "attention 0"
func TestRComposesOnlyForChat(t *testing.T)       // "r" on a mail row does nothing; on a chat row it opens the composer
func TestFilterNarrowsRows(t *testing.T)          // "/" then "ana" leaves one row
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/ -run TestCursorMovesWithJK -v`
Expected: FAIL, `undefined: New`.

- [ ] **Step 3: Implement `internal/ui/model.go` and `view.go`**

Model holds the row slice, cursor, filter string, composer state, and banner. `Init` returns a tick command that reloads rows from the store every poll interval. `Update` handles `j`, `k`, `enter`, `a`, `r`, `/`, `R`, `q`, `ctrl+c`. `View` renders the header, the grouped list, and the key hint line; every row is one string built with `lipgloss` and truncated by the passive package's truncation helper so both views cut identically. Banner text is never wrapped. Colour goes through `lipgloss`, which honours `NO_COLOR` without any code of ours.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ui/ -v`
Expected: PASS, seven tests.

- [ ] **Step 5: Commit**

```bash
git add internal/ui
git commit -m "feat: interactive attention pane"
```

---

### Task 11: Passive renderer and `--once`

**Files:**
- Create: `internal/ui/passive.go`, `internal/ui/passive_test.go`

**Interfaces:**
- Consumes: `rules.Item`, `rules.Verdict`, `theme.Palette`.
- Produces: `ui.Row{Item rules.Item; Verdict rules.Verdict; ChatName string}`, `ui.RenderPassive(rows []Row, width, height int, p theme.Palette) string`, `ui.Truncate(s string, width int) string`, `ui.RunOnce(ctx, w io.Writer, d Deps) error`.

- [ ] **Step 1: Write the failing test**

```go
func TestPassiveHasExactHeight(t *testing.T)      // height 8 -> 8 lines
func TestPassiveEmptyIsZero(t *testing.T)         // no rows -> "attention 0"
func TestTruncateIsRuneSafe(t *testing.T)         // REVIEW FOCUS 2: accented long string at width 40 -> exactly 40 runes, valid UTF-8
func TestTruncateAddsEllipsis(t *testing.T)       // width 10 -> 9 runes plus one ellipsis
func TestPassiveNarrowWidthDoesNotPanic(t *testing.T) // width 20, long sender -> no panic, one line per row
func TestRunOnceWritesBlockAndReturnsError(t *testing.T) // writer receives the block; source error is returned
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/ -run TestPassiveHasExactHeight -v`
Expected: FAIL, `undefined: RenderPassive`.

- [ ] **Step 3: Implement `internal/ui/passive.go`**

Group by source, chat rows before mail rows, notify-tier rows first within a group. Reserve one line for the header and one for the hint. `Truncate` walks runes with `runewidth`, never splits a cluster, and pads rather than wraps. `RunOnce` runs one tick through the same deps, writes the block, and returns the tick error so `main` can exit non-zero. `main` cuts the row list to `cfg.PassiveItems` before calling the renderer, so the renderer never needs the config.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ui/ -v`
Expected: PASS, six new tests.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/passive.go internal/ui/passive_test.go
git commit -m "feat: passive pane renderer and once mode"
```

---

### Task 12: Wiring, process loop, single instance

**Files:**
- Create: `cmd/attention-tui/main.go`
- Create: `cmd/attention-tui/main_test.go`

**Interfaces:**
- Consumes: every package above.
- Produces: the `attention-tui` binary with `--config`, `--login`, `--once`, `--passive`, `--version`; `run(args []string, stdout, stderr io.Writer) int` for tests.

- [ ] **Step 1: Write the failing test**

```go
func TestVersionFlag(t *testing.T)              // "--version" prints one line and returns 0
func TestMissingConfigCreatesSample(t *testing.T)// first run writes the file and keeps going
func TestSecondInstanceIsReadOnly(t *testing.T) // lock held -> banner "secondary: cache only", no polling
func TestOnceWithUnauthenticatedReturnsBanner(t *testing.T) // no token -> exit 1, stderr names --login
func TestSIGHUPReloadsRules(t *testing.T)       // edited rule file changes the verdict after SIGHUP
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/attention-tui/ -run TestVersionFlag -v`
Expected: FAIL, `undefined: run`.

- [ ] **Step 3: Implement `cmd/attention-tui/main.go`**

Parse flags, load config, open the store, take the `flock` on `<data>/state.db.lock`, then either run `--login`, `--once`, the passive loop, or `tea.NewProgram`. The loop re-reads wall clock each iteration, adds 10 percent jitter, and prints the banner instead of exiting on `auth.ErrNoToken`. `SIGHUP` reloads the config and rebuilds the rules engine. Log one line per failure at the boundary, never the token.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/attention-tui/ -v && go vet ./... && gofmt -l .`
Expected: PASS, five tests; `go vet` silent; `gofmt -l` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add cmd
git commit -m "feat: binary wiring, single instance lock, signal reload"
```

---

### Task 13: Docs and packaging

**Files:**
- Modify: `README.md`, `CHANGELOG.md`
- Create: `Makefile`

**Interfaces:**
- Consumes: nothing.
- Produces: nothing imported by code.

- [ ] **Step 1: Write the failing check**

Run: `grep -q '^## App registration' README.md`
Expected: exit 1, the section does not exist yet.

- [ ] **Step 2: Write the README**

Overview, status, requirements, the exact app-registration steps (`allowPublicClient`, redirect `http://localhost:53147/callback`, delegated scopes, admin consent for `ChatMessage.Send`), build and run, the config reference with the tiers and rules, the `--passive` tmux snippet, and a known-limits section naming what is deliberately absent.

- [ ] **Step 3: Update the changelog and add the Makefile**

`CHANGELOG.md` gets a single `0.1.0` entry naming the shipped capabilities. `Makefile` has `build`, `test`, `vet`, and `install` targets, with `test` running `go test ./...` and `vet` running `go vet ./...`.

- [ ] **Step 4: Verify**

Run: `make vet test`
Expected: no vet output, all packages PASS.

- [ ] **Step 5: Commit**

```bash
git add README.md CHANGELOG.md Makefile
git commit -m "docs: registration, config reference and packaging"
```

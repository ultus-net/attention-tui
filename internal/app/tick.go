// Package app runs one polling tick: redeem the mail delta, list chats,
// collapse each unread chat to one row, and write the cache.
package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ultus-net/attention-tui/internal/config"
	"github.com/ultus-net/attention-tui/internal/graph"
	"github.com/ultus-net/attention-tui/internal/rules"
	"github.com/ultus-net/attention-tui/internal/store"
)

const (
	mailDeltaKey   = "mail_delta"
	mailMaxPages   = 10
	chatsMaxPages  = 20
	chatWalkEvery  = 30
	chatMessageTop = 10
	retentionDays  = 7
	retentionMax   = 500
	baseBackoff    = 30 * time.Second
	maxBackoff     = 5 * time.Minute
	notifyWindow   = 2
)

// Source is the slice of the Graph client the runner needs. Tests fake it.
type Source interface {
	Chats(ctx context.Context, pageURL string) ([]graph.Chat, string, error)
	ChatMessages(ctx context.Context, chatID string, top int) ([]graph.Message, error)
	ChatMemberNames(ctx context.Context, chatID string) ([]string, error)
	MailDelta(ctx context.Context, pageURL string) (graph.MailDeltaResult, error)
}

// TickResult reports what one poll produced.
type TickResult struct {
	NewMail  int
	NewChats int
	Alerts   []rules.Item
}

// Runner owns the polling state: cached delta link, tick count, backoff.
type Runner struct {
	cfg    *config.Config
	store  *store.Store
	source Source
	engine *rules.Engine
	err    error

	ticks    int
	failures int
	backoff  time.Duration
}

// NewRunner builds a runner. A rule set that fails to compile is reported by
// the first Tick rather than dropped.
func NewRunner(cfg *config.Config, st *store.Store, src Source) *Runner {
	engine, err := rules.New(cfg)
	return &Runner{cfg: cfg, store: st, source: src, engine: engine, err: err}
}

// Backoff is how long the process should wait before the next tick.
func (r *Runner) Backoff() time.Duration { return r.backoff }

// Tick performs one poll. An error leaves the cache intact and the runner
// usable, so the process loop can wait and retry.
func (r *Runner) Tick(ctx context.Context) (TickResult, error) {
	res, err := r.tick(ctx)
	if err != nil {
		r.failed()
		return res, err
	}
	r.failures = 0
	r.backoff = 0
	return res, nil
}

func (r *Runner) failed() {
	r.failures++
	if r.failures == 1 {
		r.backoff = baseBackoff
		return
	}
	r.backoff *= 2
	if r.backoff > maxBackoff {
		r.backoff = maxBackoff
	}
}

func (r *Runner) tick(ctx context.Context) (TickResult, error) {
	if r.err != nil {
		return TickResult{}, fmt.Errorf("app: rules: %w", r.err)
	}
	r.ticks++
	now := time.Now()
	res := TickResult{}

	mails, removed, deltaLink, backfill, err := r.fetchMail(ctx)
	if err != nil {
		return res, err
	}
	mailItems := make([]rules.Item, 0, len(mails))
	seen := make([]rules.Item, 0)
	for _, m := range mails {
		it := rules.Item{
			Source:     "mail",
			ID:         m.ID,
			From:       m.Sender,
			Subject:    m.Subject,
			Preview:    m.BodyPreview,
			Importance: m.Importance,
			URL:        m.WebLink,
			Received:   m.Received,
			Focused:    m.Focused,
			Flagged:    m.Flagged,
		}
		mailItems = append(mailItems, it)
		if m.IsRead {
			seen = append(seen, it)
		}
	}
	inserted, err := r.store.UpsertItems(ctx, mailItems)
	if err != nil {
		return res, err
	}
	res.NewMail = inserted
	for _, it := range seen {
		if err := r.store.SetSeen(ctx, it.Source, it.ID); err != nil {
			return res, err
		}
	}
	// A message that left the inbox is gone from the pane now, not when the
	// retention window expires.
	if _, err := r.store.DeleteItems(ctx, "mail", removed); err != nil {
		return res, err
	}

	chats, err := r.fetchChats(ctx)
	if err != nil {
		return res, err
	}
	chatItems := make([]rules.Item, 0, len(chats))
	for _, ch := range chats {
		watermark, err := r.store.ChatWatermark(ctx, ch.ID)
		if err != nil {
			return res, err
		}
		if !graph.IsUnread(ch, watermark, now) {
			continue
		}
		msgs, err := r.source.ChatMessages(ctx, ch.ID, chatMessageTop)
		if err != nil {
			return res, err
		}
		it, err := r.chatItem(ctx, ch, msgs, watermark)
		if err != nil {
			return res, err
		}
		chatItems = append(chatItems, it)
	}
	inserted, err = r.store.UpsertItems(ctx, chatItems)
	if err != nil {
		return res, err
	}
	res.NewChats = inserted

	// The delta link advances only after every row from this walk is cached,
	// so a later failure replays the same delta instead of skipping pages.
	if deltaLink != "" {
		if err := r.store.SetKV(ctx, mailDeltaKey, deltaLink); err != nil {
			return res, err
		}
	}

	if _, err := r.store.Prune(ctx, now.AddDate(0, 0, -retentionDays), retentionMax); err != nil {
		return res, err
	}

	// Backfill is history, not news: it must never announce itself. Skipping
	// the eligibility pass also leaves those rows unstamped.
	if !backfill {
		alerts, err := r.pendingAlerts(ctx)
		if err != nil {
			return res, err
		}
		res.Alerts = alerts
	}
	return res, nil
}

func (r *Runner) pendingAlerts(ctx context.Context) ([]rules.Item, error) {
	window := time.Duration(notifyWindow) * r.cfg.PollInterval.Std()
	pending, err := r.store.PendingNotifications(ctx, window)
	if err != nil {
		return nil, err
	}
	out := make([]rules.Item, 0, len(pending))
	for _, it := range pending {
		if r.engine.Eval(it).Notify {
			out = append(out, it)
		}
	}
	if err := r.store.MarkNotified(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Runner) fetchMail(ctx context.Context) ([]graph.MailMessage, []string, string, bool, error) {
	link, ok, err := r.store.KV(ctx, mailDeltaKey)
	if err != nil {
		return nil, nil, "", false, err
	}
	backfill := !ok || strings.TrimSpace(link) == ""
	pageURL := link
	if backfill {
		pageURL = ""
	}

	var collected []graph.MailMessage
	var removed []string
	deltaLink := ""
	restarted := false
	for page := 0; page < mailMaxPages; page++ {
		res, err := r.source.MailDelta(ctx, pageURL)
		if err != nil {
			return nil, nil, "", backfill, fmt.Errorf("app: mail delta: %w", err)
		}
		if res.Restart {
			if restarted {
				return nil, nil, "", backfill, fmt.Errorf("app: mail delta restarted twice in one tick")
			}
			restarted = true
			backfill = true
			collected = nil
			pageURL = ""
			continue
		}
		collected = append(collected, res.Messages...)
		removed = append(removed, res.Deleted...)
		if res.DeltaLink != "" {
			deltaLink = res.DeltaLink
		}
		if strings.TrimSpace(res.NextLink) == "" {
			break
		}
		pageURL = res.NextLink
	}
	return r.boundMail(collected, backfill), removed, deltaLink, backfill, nil
}

func (r *Runner) boundMail(msgs []graph.MailMessage, backfill bool) []graph.MailMessage {
	max := r.cfg.BackfillMax
	if max <= 0 {
		max = 200
	}
	if backfill {
		days := r.cfg.BackfillDays
		if days <= 0 {
			days = 7
		}
		cutoff := time.Now().AddDate(0, 0, -days)
		filtered := msgs[:0]
		for _, m := range msgs {
			if m.Received.IsZero() || m.Received.After(cutoff) {
				filtered = append(filtered, m)
			}
		}
		msgs = filtered
	}
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].Received.After(msgs[j].Received) })
	if len(msgs) > max {
		msgs = msgs[:max]
	}
	return msgs
}

func (r *Runner) fetchChats(ctx context.Context) ([]graph.Chat, error) {
	full := r.ticks == 1 || r.ticks%chatWalkEvery == 0
	pageURL := ""
	var chats []graph.Chat
	for page := 0; page < chatsMaxPages; page++ {
		batch, next, err := r.source.Chats(ctx, pageURL)
		if err != nil {
			return nil, fmt.Errorf("app: list chats: %w", err)
		}
		chats = append(chats, batch...)
		if !full || strings.TrimSpace(next) == "" {
			break
		}
		pageURL = next
	}
	return chats, nil
}

func (r *Runner) chatItem(ctx context.Context, ch graph.Chat, msgs []graph.Message, watermark time.Time) (rules.Item, error) {
	read := ch.LastReadAt
	if watermark.After(read) {
		read = watermark
	}

	var newest *graph.Message
	mention := false
	for i := range msgs {
		// Only unread messages may flag the row. A mention in a message the
		// user already read must not keep the chat in the alert tier.
		if !msgs[i].Created.After(read) {
			continue
		}
		if newest == nil {
			newest = &msgs[i]
		}
		if msgs[i].Mention {
			mention = true
		}
	}
	if newest == nil && len(msgs) > 0 {
		newest = &msgs[0]
	}

	name, err := r.chatName(ctx, ch)
	if err != nil {
		return rules.Item{}, err
	}

	it := rules.Item{
		Source:   "chat",
		ID:       ch.ID,
		ChatID:   ch.ID,
		ChatName: name,
		URL:      ch.WebURL,
		Received: ch.LastMessageAt,
		Mention:  mention,
	}
	if newest != nil {
		it.From = newest.Sender
		it.Preview = newest.Body
		it.URL = firstNonEmpty(newest.WebURL, ch.WebURL)
		it.Received = newest.Created
	}
	return it, nil
}

func (r *Runner) chatName(ctx context.Context, ch graph.Chat) (string, error) {
	if topic := strings.TrimSpace(ch.Topic); topic != "" {
		return topic, nil
	}
	cached, ok, err := r.store.ChatName(ctx, ch.ID)
	if err != nil {
		return "", err
	}
	if ok {
		return cached, nil
	}
	names, err := r.source.ChatMemberNames(ctx, ch.ID)
	if err != nil {
		return "", fmt.Errorf("app: chat members %s: %w", ch.ID, err)
	}
	name := graph.ChatName(ch, names)
	if name != "" {
		if err := r.store.SetChatName(ctx, ch.ID, name); err != nil {
			return "", err
		}
	}
	return name, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

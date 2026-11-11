package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ultus-net/attention-tui/internal/config"
	"github.com/ultus-net/attention-tui/internal/graph"
	"github.com/ultus-net/attention-tui/internal/rules"
	"github.com/ultus-net/attention-tui/internal/store"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		PollInterval: config.Duration(60 * time.Second),
		BackfillDays: 7,
		BackfillMax:  200,
		Tiers: map[string]config.Tier{
			"alert": {Notify: true, Show: true},
			"watch": {Notify: false, Show: true},
			"quiet": {Notify: false, Show: false},
		},
		Defaults: map[string]string{"mail": "watch", "chat": "quiet"},
		Rules: []config.Rule{
			{Name: "mentions", Match: config.Match{Mention: boolPtr(true)}, Tier: "alert"},
		},
	}
}

func boolPtr(b bool) *bool { return &b }

type fakeSource struct {
	mailFn    func(ctx context.Context, url string) (graph.MailDeltaResult, error)
	chatsFn   func(ctx context.Context, url string) ([]graph.Chat, string, error)
	messages  map[string][]graph.Message
	members   map[string][]string
	mailCalls int
	chatCalls int
	page2Hits int
}

func (f *fakeSource) MailDelta(ctx context.Context, url string) (graph.MailDeltaResult, error) {
	f.mailCalls++
	if f.mailFn != nil {
		return f.mailFn(ctx, url)
	}
	if url == "" {
		return graph.MailDeltaResult{DeltaLink: "delta-1"}, nil
	}
	return graph.MailDeltaResult{DeltaLink: url}, nil
}

func (f *fakeSource) Chats(ctx context.Context, url string) ([]graph.Chat, string, error) {
	f.chatCalls++
	if url == "page2" {
		f.page2Hits++
	}
	if f.chatsFn != nil {
		return f.chatsFn(ctx, url)
	}
	return nil, "", nil
}

func (f *fakeSource) ChatMessages(ctx context.Context, chatID string, top int) ([]graph.Message, error) {
	return f.messages[chatID], nil
}

func (f *fakeSource) ChatMemberNames(ctx context.Context, chatID string) ([]string, error) {
	return f.members[chatID], nil
}

func newRunner(t *testing.T, cfg *config.Config, src Source) (*Runner, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return NewRunner(cfg, st, src), st
}

func unreadChat() graph.Chat {
	now := time.Now()
	return graph.Chat{
		ID:            "19:abc",
		Topic:         "Deploy Bot",
		WebURL:        "https://teams.example/19:abc",
		LastMessageAt: now,
		LastReadAt:    now.Add(-time.Hour),
	}
}

func TestTickFetchesMailAndChats(t *testing.T) {
	now := time.Now()
	src := &fakeSource{
		mailFn: func(context.Context, string) (graph.MailDeltaResult, error) {
			return graph.MailDeltaResult{
				Messages: []graph.MailMessage{{
					ID: "m1", Sender: "Ana <ana@example.com>", Subject: "Budget",
					Received: now, IsRead: false,
				}},
				DeltaLink: "delta-1",
			}, nil
		},
		chatsFn: func(context.Context, string) ([]graph.Chat, string, error) {
			return []graph.Chat{unreadChat()}, "", nil
		},
		messages: map[string][]graph.Message{
			"19:abc": {{ID: "msg1", ChatID: "19:abc", Sender: "Ana", Body: "build failed", Created: now}},
		},
	}
	r, st := newRunner(t, testConfig(t), src)

	res, err := r.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.NewMail != 1 || res.NewChats != 1 {
		t.Fatalf("tick = %+v, want one new mail and one new chat", res)
	}
	items, err := st.Items(context.Background())
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
}

func TestChatCollapsesToOneItem(t *testing.T) {
	now := time.Now()
	msgs := make([]graph.Message, 0, 10)
	for i := 0; i < 10; i++ {
		msgs = append(msgs, graph.Message{
			ID: "msg" + time.Duration(i).String(), ChatID: "19:abc",
			Sender: "Ana", Body: "message", Created: now.Add(time.Duration(i) * time.Second),
			Mention: i == 7,
		})
	}
	src := &fakeSource{
		chatsFn: func(context.Context, string) ([]graph.Chat, string, error) {
			return []graph.Chat{unreadChat()}, "", nil
		},
		messages: map[string][]graph.Message{"19:abc": msgs},
	}
	r, st := newRunner(t, testConfig(t), src)

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	items, err := st.Items(context.Background())
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want one row per chat", len(items))
	}
	if !items[0].Mention {
		t.Fatal("a mention anywhere in the fetched messages must flag the chat row")
	}
	if items[0].Source != "chat" || items[0].ChatName != "Deploy Bot" {
		t.Fatalf("item = %+v", items[0])
	}
}

func TestMailReadMarksSeen(t *testing.T) {
	src := &fakeSource{
		mailFn: func(context.Context, string) (graph.MailDeltaResult, error) {
			return graph.MailDeltaResult{
				Messages:  []graph.MailMessage{{ID: "m1", IsRead: true, Received: time.Now()}},
				DeltaLink: "delta-1",
			}, nil
		},
	}
	r, st := newRunner(t, testConfig(t), src)

	res, err := r.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.NewMail != 1 {
		t.Fatalf("NewMail = %d, want 1", res.NewMail)
	}
	items, err := st.Items(context.Background())
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %d, want the read message already seen", len(items))
	}
}

func TestBackfillFiresNoAlerts(t *testing.T) {
	now := time.Now()
	msgs := make([]graph.MailMessage, 0, 50)
	for i := 0; i < 50; i++ {
		msgs = append(msgs, graph.MailMessage{ID: "m" + time.Duration(i).String(), Subject: "old", Received: now})
	}
	src := &fakeSource{
		mailFn: func(_ context.Context, url string) (graph.MailDeltaResult, error) {
			if url != "" {
				return graph.MailDeltaResult{Restart: true}, nil
			}
			return graph.MailDeltaResult{Messages: msgs}, nil
		},
	}
	cfg := testConfig(t)
	cfg.Rules = []config.Rule{{Name: "everything", Tier: "alert"}}
	r, _ := newRunner(t, cfg, src)

	res, err := r.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(res.Alerts) != 0 {
		t.Fatalf("alerts = %d, want 0 on a backfill", len(res.Alerts))
	}
}

func TestBackfillIsBounded(t *testing.T) {
	now := time.Now()
	msgs := make([]graph.MailMessage, 0, 400)
	for i := 0; i < 400; i++ {
		msgs = append(msgs, graph.MailMessage{
			ID: "m" + time.Duration(i).String(), Received: now.Add(-time.Duration(i) * time.Second),
		})
	}
	src := &fakeSource{
		mailFn: func(context.Context, string) (graph.MailDeltaResult, error) {
			return graph.MailDeltaResult{Messages: msgs, DeltaLink: "delta-1"}, nil
		},
	}
	r, st := newRunner(t, testConfig(t), src)

	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	items, err := st.Items(context.Background())
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 200 {
		t.Fatalf("items = %d, want the 200-item backfill cap", len(items))
	}
}

func TestFirstTickPagesChats(t *testing.T) {
	src := &fakeSource{
		chatsFn: func(_ context.Context, url string) ([]graph.Chat, string, error) {
			if url == "" {
				return nil, "page2", nil
			}
			return nil, "", nil
		},
	}
	r, _ := newRunner(t, testConfig(t), src)

	for i := 0; i < 3; i++ {
		if _, err := r.Tick(context.Background()); err != nil {
			t.Fatalf("Tick %d: %v", i, err)
		}
	}
	if src.page2Hits != 1 {
		t.Fatalf("page 2 fetched %d times, want once on the first tick", src.page2Hits)
	}
	if src.chatCalls != 4 {
		t.Fatalf("chat calls = %d, want 4", src.chatCalls)
	}
}

func TestNetworkErrorDoesNotStopRunner(t *testing.T) {
	failing := true
	src := &fakeSource{
		mailFn: func(context.Context, string) (graph.MailDeltaResult, error) {
			if failing {
				return graph.MailDeltaResult{}, errors.New("network down")
			}
			return graph.MailDeltaResult{DeltaLink: "delta-1"}, nil
		},
	}
	r, _ := newRunner(t, testConfig(t), src)

	if _, err := r.Tick(context.Background()); err == nil {
		t.Fatal("expected the first tick to report the network error")
	}
	failing = false
	if _, err := r.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if src.mailCalls != 2 {
		t.Fatalf("mail calls = %d, want 2", src.mailCalls)
	}
}

func TestBackoffGrows(t *testing.T) {
	src := &fakeSource{
		mailFn: func(context.Context, string) (graph.MailDeltaResult, error) {
			return graph.MailDeltaResult{}, errors.New("down")
		},
	}
	r, _ := newRunner(t, testConfig(t), src)

	want := []time.Duration{30 * time.Second, 60 * time.Second, 120 * time.Second, 240 * time.Second, 300 * time.Second}
	for i, w := range want {
		if _, err := r.Tick(context.Background()); err == nil {
			t.Fatalf("tick %d should fail", i)
		}
		if got := r.Backoff(); got != w {
			t.Fatalf("backoff after %d failures = %s, want %s", i+1, got, w)
		}
	}
}

var _ = rules.Item{}

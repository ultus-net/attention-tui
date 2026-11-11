package rules

import (
	"strings"
	"testing"
	"time"

	"github.com/ultus-net/attention-tui/internal/config"
)

func testConfig(t *testing.T, rules ...config.Rule) *config.Config {
	t.Helper()
	return &config.Config{
		Tiers: map[string]config.Tier{
			"alert": {Notify: true, Show: true},
			"watch": {Notify: false, Show: true},
			"quiet": {Notify: false, Show: false},
		},
		Defaults: map[string]string{"mail": "watch", "chat": "quiet"},
		Rules:    rules,
	}
}

func mailItem() Item {
	return Item{
		Source:     "mail",
		ID:         "m1",
		From:       "Ana Lima <ana@legal.example.com>",
		Subject:    "Q4 INVOICE",
		Preview:    "please review",
		Importance: "high",
		Received:   time.Now(),
	}
}

func chatItem() Item {
	return Item{
		Source:   "chat",
		ID:       "c1",
		ChatID:   "19:abc",
		ChatName: "Deploy Bot",
		From:     "Deploy Bot",
		Preview:  "build failed",
	}
}

func TestFirstMatchWins(t *testing.T) {
	mention := true
	cfg := testConfig(t,
		config.Rule{Name: "mentions", Match: config.Match{Mention: &mention}, Tier: "alert"},
		config.Rule{Name: "bots", Match: config.Match{Chat: []string{"Deploy Bot"}}, Tier: "quiet"},
	)
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	it := chatItem()
	it.Mention = true
	v := e.Eval(it)
	if !v.Notify || !v.Show {
		t.Fatalf("verdict = %+v, want alert", v)
	}
	if v.Name != "mentions" {
		t.Fatalf("verdict name = %q, want mentions", v.Name)
	}
}

func TestDefaultsWhenNoRuleMatches(t *testing.T) {
	e, err := New(testConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if v := e.Eval(mailItem()); !v.Show || v.Notify {
		t.Fatalf("mail verdict = %+v, want watch", v)
	}
	if v := e.Eval(chatItem()); v.Show || v.Notify {
		t.Fatalf("chat verdict = %+v, want quiet", v)
	}
}

func TestGlobFrom(t *testing.T) {
	cfg := testConfig(t, config.Rule{
		Name: "legal",
		Match: config.Match{
			From: []string{"*@legal.example.com"},
		},
		Tier: "alert",
	})
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if v := e.Eval(mailItem()); v.Name != "legal" {
		t.Fatalf("verdict = %+v, want the legal rule", v)
	}
}

func TestGlobIsAnchored(t *testing.T) {
	cfg := testConfig(t, config.Rule{
		Name:  "cfo",
		Match: config.Match{From: []string{"cfo@example.com"}},
		Tier:  "alert",
	})
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	it := mailItem()
	it.From = "cfo@example.com.evil"
	if v := e.Eval(it); v.Name != "default" {
		t.Fatalf("verdict = %+v, anchored glob must not match a suffix", v)
	}
}

func TestSubjectRegexCaseInsensitive(t *testing.T) {
	cfg := testConfig(t, config.Rule{
		Name:  "invoice",
		Match: config.Match{Subject: "invoice"},
		Tier:  "alert",
	})
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if v := e.Eval(mailItem()); v.Name != "invoice" {
		t.Fatalf("verdict = %+v, want the invoice rule", v)
	}
}

func TestAbsentFieldMatchesEverything(t *testing.T) {
	cfg := testConfig(t, config.Rule{Name: "all", Tier: "alert"})
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if v := e.Eval(mailItem()); v.Name != "all" {
		t.Fatalf("mail verdict = %+v", v)
	}
	if v := e.Eval(chatItem()); v.Name != "all" {
		t.Fatalf("chat verdict = %+v", v)
	}
}

func TestMentionFalseIsNotWildcard(t *testing.T) {
	notMention := false
	cfg := testConfig(t, config.Rule{
		Name:  "not-mentioned",
		Match: config.Match{Mention: &notMention},
		Tier:  "watch",
	})
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	it := chatItem()
	it.Mention = true
	if v := e.Eval(it); v.Name != "default" {
		t.Fatalf("verdict = %+v, mention:false must exclude mentions", v)
	}
	it.Mention = false
	if v := e.Eval(it); v.Name != "not-mentioned" {
		t.Fatalf("verdict = %+v, want the rule to match a non-mention", v)
	}
}

func TestBadTierNameRejected(t *testing.T) {
	cfg := testConfig(t, config.Rule{Name: "urgent-mail", Match: config.Match{Source: "mail"}, Tier: "urgent"})
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected an unknown tier to be rejected")
	}
	if !strings.Contains(err.Error(), "urgent-mail") {
		t.Fatalf("error should name the rule, got %v", err)
	}
}

func TestBadRegexRejected(t *testing.T) {
	cfg := testConfig(t, config.Rule{Name: "bad-subject", Match: config.Match{Subject: "("}, Tier: "alert"})
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected a bad regex to be rejected")
	}
	if !strings.Contains(err.Error(), "bad-subject") {
		t.Fatalf("error should name the rule, got %v", err)
	}
}

func TestImportanceAndFlags(t *testing.T) {
	yes := true
	cfg := testConfig(t, config.Rule{
		Name: "flagged-focus",
		Match: config.Match{
			Importance: "high",
			Flagged:    &yes,
			Focused:    &yes,
		},
		Tier: "alert",
	})
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	it := mailItem()
	it.Flagged = true
	it.Focused = true
	if v := e.Eval(it); v.Name != "flagged-focus" {
		t.Fatalf("verdict = %+v, want the flagged-focus rule", v)
	}
	it.Flagged = false
	if v := e.Eval(it); v.Name != "default" {
		t.Fatalf("verdict = %+v, flagged:true must not match an unflagged item", v)
	}
}

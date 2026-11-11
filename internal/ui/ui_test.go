package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/ultus-net/attention-tui/internal/config"
	"github.com/ultus-net/attention-tui/internal/rules"
	"github.com/ultus-net/attention-tui/internal/store"
	"github.com/ultus-net/attention-tui/internal/theme"
)

// TestMain forces a colour profile so escape gating is observable under
// `go test`, where stdout is a pipe and lipgloss degrades to no colour.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	os.Exit(m.Run())
}

type recorder struct {
	opened   string
	chatID   string
	sentText string
}

func testDeps(t *testing.T, items ...rules.Item) (Deps, *store.Store, *recorder) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if len(items) > 0 {
		if _, err := st.UpsertItems(context.Background(), items); err != nil {
			t.Fatalf("UpsertItems: %v", err)
		}
	}
	cfg := &config.Config{
		PollInterval: config.Duration(time.Minute),
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
	engine, err := rules.New(cfg)
	if err != nil {
		t.Fatalf("rules.New: %v", err)
	}
	rec := &recorder{}
	d := Deps{
		Store:  st,
		Engine: engine,
		Cfg:    cfg,
		Open: func(url string) error {
			rec.opened = url
			return nil
		},
		Send: func(ctx context.Context, chatID, text string) error {
			rec.chatID = chatID
			rec.sentText = text
			return nil
		},
	}
	return d, st, rec
}

func boolPtr(b bool) *bool { return &b }

func mailItem(id, from, subject string) rules.Item {
	return rules.Item{
		Source:   "mail",
		ID:       id,
		From:     from,
		Subject:  subject,
		Preview:  "preview",
		URL:      "https://outlook.example/" + id,
		Received: time.Now(),
	}
}

func chatItem(id string) rules.Item {
	return rules.Item{
		Source:   "chat",
		ID:       id,
		ChatID:   "19:" + id,
		ChatName: "Deploy Bot",
		From:     "Deploy Bot",
		Preview:  "build failed",
		URL:      "https://teams.example/" + id,
		Received: time.Now(),
		Mention:  true,
	}
}

func press(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return got
}

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestCursorMovesWithJK(t *testing.T) {
	d, _, _ := testDeps(t, mailItem("m1", "Ana", "one"), mailItem("m2", "Ben", "two"))
	m := New(d)
	if len(m.visibleRows()) != 2 {
		t.Fatalf("rows = %d, want 2", len(m.visibleRows()))
	}
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
	m = press(t, m, runeKey('j'))
	if m.cursor != 1 {
		t.Fatalf("cursor after j = %d, want 1", m.cursor)
	}
	m = press(t, m, runeKey('k'))
	if m.cursor != 0 {
		t.Fatalf("cursor after k = %d, want 0", m.cursor)
	}
}

func TestEnterOpensAndMarksSeen(t *testing.T) {
	d, _, rec := testDeps(t, mailItem("m1", "Ana", "one"))
	m := New(d)
	m = press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if rec.opened != "https://outlook.example/m1" {
		t.Fatalf("opened = %q", rec.opened)
	}
	if rows := m.visibleRows(); len(rows) != 0 {
		t.Fatalf("rows after enter = %d, want the row gone", len(rows))
	}
}

func TestARevealsQuietRows(t *testing.T) {
	it := chatItem("c1")
	it.Mention = false
	d, _, _ := testDeps(t, it)
	m := New(d)
	if rows := m.visibleRows(); len(rows) != 0 {
		t.Fatalf("rows = %d, want quiet rows hidden", len(rows))
	}
	m = press(t, m, runeKey('a'))
	if rows := m.visibleRows(); len(rows) != 1 {
		t.Fatalf("rows after a = %d, want the quiet row visible", len(rows))
	}
}

func TestBannerRendersWhenSet(t *testing.T) {
	d, _, _ := testDeps(t)
	m := New(d)
	m.SetBanner("auth: no token")
	if !strings.Contains(m.View(), "auth: no token") {
		t.Fatalf("view = %q, want the banner", m.View())
	}
}

func TestEmptyCacheRendersZero(t *testing.T) {
	d, _, _ := testDeps(t)
	m := New(d)
	if !strings.Contains(m.View(), "attention 0") {
		t.Fatalf("view = %q, want attention 0", m.View())
	}
}

func TestRComposesOnlyForChat(t *testing.T) {
	mailDeps, _, _ := testDeps(t, mailItem("m1", "Ana", "one"))
	mailModel := New(mailDeps)
	mailModel = press(t, mailModel, runeKey('r'))
	if mailModel.composer {
		t.Fatal("r on a mail row must not open the composer")
	}

	chatDeps, _, _ := testDeps(t, chatItem("c1"))
	chatModel := New(chatDeps)
	chatModel = press(t, chatModel, runeKey('r'))
	if !chatModel.composer {
		t.Fatal("r on a chat row must open the composer")
	}
}

func TestFilterNarrowsRows(t *testing.T) {
	d, _, _ := testDeps(t, mailItem("m1", "Ana Lima", "budget"), mailItem("m2", "Ben", "invoice"))
	m := New(d)
	m = press(t, m, runeKey('/'))
	m = press(t, m, runeKey('a'))
	m = press(t, m, runeKey('n'))
	m = press(t, m, runeKey('a'))
	rows := m.visibleRows()
	if len(rows) != 1 {
		t.Fatalf("filtered rows = %d, want 1", len(rows))
	}
}

func TestBenchFilterUsesFuzzyOrder(t *testing.T) {
	row := Row{Item: rules.Item{From: "Ana Lima", Subject: "budget"}}
	for _, filter := range []string{"anl", "ana bud", "alima"} {
		if !rowMatches(row, filter) {
			t.Fatalf("filter %q did not match in order", filter)
		}
	}
	for _, filter := range []string{"lima ana", "zzz"} {
		if rowMatches(row, filter) {
			t.Fatalf("filter %q matched out of order or absent", filter)
		}
	}
}

func TestFilterNarrowsRowsFuzzy(t *testing.T) {
	d, _, _ := testDeps(t, mailItem("m1", "Ana Lima", "budget"), mailItem("m2", "Ben", "invoice"))
	m := New(d)
	m.filter = "anl"
	if rows := m.visibleRows(); len(rows) != 1 {
		t.Fatalf("filtered rows = %d, want Anna's in-order match", len(rows))
	}
}

// Terminals disagree on what they send for the backspace key: 127, ctrl+h, or
// the delete escape sequence. All three must edit the prompt.
var backspaceVariants = []tea.KeyType{tea.KeyBackspace, tea.KeyCtrlH, tea.KeyDelete}

func TestFilterDeletesOnEveryBackspaceVariant(t *testing.T) {
	for _, variant := range backspaceVariants {
		d, _, _ := testDeps(t, mailItem("m1", "Ana", "one"))
		m := New(d)
		m = press(t, m, runeKey('/'))
		for _, r := range "ana" {
			m = press(t, m, runeKey(r))
		}
		m = press(t, m, tea.KeyMsg{Type: variant})
		if m.filter != "an" {
			t.Fatalf("%v: filter = %q, want %q", variant, m.filter, "an")
		}
	}
}

func TestComposerDeletesOnEveryBackspaceVariant(t *testing.T) {
	for _, variant := range backspaceVariants {
		d, _, _ := testDeps(t, chatItem("c1"))
		m := New(d)
		m = press(t, m, runeKey('r'))
		for _, r := range "hi" {
			m = press(t, m, runeKey(r))
		}
		m = press(t, m, tea.KeyMsg{Type: variant})
		if m.composerText != "h" {
			t.Fatalf("%v: composer text = %q, want %q", variant, m.composerText, "h")
		}
	}
}

func TestFilterKeepsSpaces(t *testing.T) {
	d, _, _ := testDeps(t, mailItem("m1", "Ana Lima", "one"))
	m := New(d)
	m = press(t, m, runeKey('/'))
	m = press(t, m, runeKey('a'))
	m = press(t, m, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = press(t, m, runeKey('l'))
	if m.filter != "a l" {
		t.Fatalf("filter = %q, want %q", m.filter, "a l")
	}
}

func TestPromptIgnoresEmptyRuneKeys(t *testing.T) {
	d, _, _ := testDeps(t, mailItem("m1", "Ana", "one"))
	m := New(d)
	m = press(t, m, runeKey('/'))
	m = press(t, m, runeKey('a'))
	m = press(t, m, tea.KeyMsg{Type: tea.KeyLeft, Runes: nil})
	m = press(t, m, tea.KeyMsg{Type: tea.KeyCtrlA})
	if m.filter != "a" {
		t.Fatalf("filter = %q, want %q", m.filter, "a")
	}
}

func TestColourGateControlsEscapes(t *testing.T) {
	d, _, _ := testDeps(t, chatItem("c1"))
	d.Palette = theme.Get(theme.DefaultName)

	if view := New(d).View(); strings.Contains(view, "\x1b[") {
		t.Fatalf("color off emitted escape codes: %q", view)
	}

	d.Color = true
	if view := New(d).View(); !strings.Contains(view, "\x1b[") {
		t.Fatal("color on emitted no escape codes for an alert row")
	}
}

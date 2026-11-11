package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/ultus-net/attention-tui/internal/rules"
	"github.com/ultus-net/attention-tui/internal/theme"
)

func row(id, source string, notify bool) Row {
	return Row{
		Item:     rules.Item{Source: source, ID: id, From: "Ana Lima <ana@example.com>", Preview: "preview " + id},
		Verdict:  rules.Verdict{Show: true, Notify: notify},
		ChatName: "Ana Lima",
	}
}

func TestPassiveHasExactHeight(t *testing.T) {
	rows := []Row{row("1", "mail", false), row("2", "chat", true), row("3", "mail", false)}
	out := RenderPassive(rows, 60, 8, theme.Get("catppuccin-mocha"))
	if got := len(strings.Split(out, "\n")); got != 8 {
		t.Fatalf("lines = %d, want 8\n%s", got, out)
	}
}

func TestPassiveEmptyIsZero(t *testing.T) {
	out := RenderPassive(nil, 60, 6, theme.Get("catppuccin-mocha"))
	if !strings.Contains(out, "attention 0") {
		t.Fatalf("out = %q, want attention 0", out)
	}
}

func TestTruncateIsRuneSafe(t *testing.T) {
	s := strings.Repeat("café ", 30)
	got := Truncate(s, 40)
	if !utf8.ValidString(got) {
		t.Fatalf("Truncate broke UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 40 {
		t.Fatalf("runes = %d, want 40", n)
	}
	if w := runewidth.StringWidth(got); w != 40 {
		t.Fatalf("width = %d, want 40", w)
	}
}

func TestTruncateAddsEllipsis(t *testing.T) {
	got := Truncate("abcdefghijklmnop", 10)
	if n := utf8.RuneCountInString(got); n != 10 {
		t.Fatalf("runes = %d, want 10", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("got = %q, want an ellipsis", got)
	}
	if got != "abcdefghi…" {
		t.Fatalf("got = %q", got)
	}
}

func TestPassiveNarrowWidthDoesNotPanic(t *testing.T) {
	src := Row{
		Item:     rules.Item{Source: "mail", ID: "m1", From: strings.Repeat("A very long sender ", 10), Preview: strings.Repeat("and a long preview ", 10)},
		Verdict:  rules.Verdict{Show: true, Notify: true},
		ChatName: strings.Repeat("Very Long Name ", 10),
	}
	out := RenderPassive([]Row{src}, 20, 6, theme.Get("catppuccin-mocha"))
	lines := strings.Split(out, "\n")
	if len(lines) != 6 {
		t.Fatalf("lines = %d, want 6", len(lines))
	}
	for _, line := range lines {
		if w := runewidth.StringWidth(line); w > 20 {
			t.Fatalf("line %q is %d cells wide, want <= 20", line, w)
		}
	}
}

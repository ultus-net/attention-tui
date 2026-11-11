package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// Truncate cuts s to at most width display cells, never splitting a rune, and
// marks a cut with an ellipsis.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if used+w > width-1 {
			break
		}
		b.WriteRune(r)
		used += w
	}
	b.WriteString("…")
	return b.String()
}

// paint colours an already-truncated line. Colouring after truncation keeps
// escape bytes out of the width calculation. enabled=false leaves s untouched,
// which is what NO_COLOR and a non-terminal writer want.
func paint(s string, c lipgloss.Color, enabled bool) string {
	if !enabled || s == "" || c == "" {
		return s
	}
	return lipgloss.NewStyle().Foreground(c).Render(s)
}

package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ultus-net/attention-tui/internal/theme"
)

// RenderPassive renders a fixed block: exactly height lines, each at most width
// cells. The caller decides which rows are eligible, and whether the writer can
// take colour. Painting happens after truncation so escapes never affect width.
func RenderPassive(rows []Row, width, height int, p theme.Palette, color bool) string {
	if height <= 0 {
		return ""
	}
	if width < 1 {
		width = 1
	}
	ordered := orderRows(rows)

	lines := make([]string, 0, height)
	header := Truncate(fmt.Sprintf("attention %d", len(rows)), width)
	lines = append(lines, paint(header, p.Accent, color))
	if height == 1 {
		return lines[0]
	}

	body := height - 2
	for i := 0; i < body; i++ {
		if i < len(ordered) {
			row := passiveRow(ordered[i], width)
			if ordered[i].Verdict.Notify {
				row = paint(row, p.Alert, color)
			}
			lines = append(lines, row)
			continue
		}
		lines = append(lines, "")
	}
	if body > 0 && len(ordered) > body {
		hidden := len(ordered) - body + 1
		lines[len(lines)-1] = Truncate(fmt.Sprintf("… and %d more", hidden), width)
	}

	lines = append(lines, paint(Truncate("run attention-tui for keys", width), p.Muted, color))
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

// orderRows puts chats before mail and notify-tier rows first inside a group,
// then newest first.
func orderRows(rows []Row) []Row {
	out := make([]Row, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Item.Source != b.Item.Source {
			return a.Item.Source == "chat"
		}
		if a.Verdict.Notify != b.Verdict.Notify {
			return a.Verdict.Notify
		}
		return a.Item.Received.After(b.Item.Received)
	})
	return out
}

func passiveRow(row Row, width int) string {
	tag := "[mail]"
	if row.Item.Source == "chat" {
		tag = "[chat]"
	}
	name := row.ChatName
	if name == "" {
		name = row.Item.From
	}
	if name == "" {
		name = "(unknown)"
	}
	return Truncate(fmt.Sprintf("%s %s  %s", tag, name, row.Item.Preview), width)
}

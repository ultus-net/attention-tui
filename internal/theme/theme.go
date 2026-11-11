// Package theme holds the colour palettes shared with rss-tui. Every palette
// fills the same semantic roles, so adding a theme is one struct entry.
package theme

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette is one colour scheme.
type Palette struct {
	Name   string
	Accent lipgloss.Color // pane title, active border
	Muted  lipgloss.Color // timestamps, hints
	Alert  lipgloss.Color // alert-tier rows
	Mail   lipgloss.Color // mail source tag
	Chat   lipgloss.Color // chat source tag
	Border lipgloss.Color
}

// DefaultName is the palette used when the config names an unknown theme.
const DefaultName = "catppuccin-mocha"

var order = []string{
	"catppuccin-mocha",
	"dracula",
	"nord",
	"gruvbox-dark",
	"tokyo-night",
	"solarized-dark",
}

var palettes = map[string]Palette{
	"catppuccin-mocha": {
		Name: "catppuccin-mocha", Accent: "#cba6f7", Muted: "#a6adc8",
		Alert: "#f38ba8", Mail: "#89b4fa", Chat: "#a6e3a1", Border: "#45475a",
	},
	"dracula": {
		Name: "dracula", Accent: "#bd93f9", Muted: "#a3a9c9",
		Alert: "#ff5555", Mail: "#8be9fd", Chat: "#50fa7b", Border: "#44475a",
	},
	"nord": {
		Name: "nord", Accent: "#88c0d0", Muted: "#a5b1c9",
		Alert: "#bf616a", Mail: "#81a1c1", Chat: "#a3be8c", Border: "#434c5e",
	},
	"gruvbox-dark": {
		Name: "gruvbox-dark", Accent: "#fabd2f", Muted: "#bdae93",
		Alert: "#fb4934", Mail: "#83a598", Chat: "#b8bb26", Border: "#504945",
	},
	"tokyo-night": {
		Name: "tokyo-night", Accent: "#7aa2f7", Muted: "#9aa5ce",
		Alert: "#f7768e", Mail: "#bb9af7", Chat: "#9ece6a", Border: "#2f334d",
	},
	"solarized-dark": {
		Name: "solarized-dark", Accent: "#268bd2", Muted: "#839496",
		Alert: "#dc322f", Mail: "#2aa198", Chat: "#859900", Border: "#073642",
	},
}

var aliases = map[string]string{
	"catppuccin":       "catppuccin-mocha",
	"catppuccin-mocha": "catppuccin-mocha",
	"mocha":            "catppuccin-mocha",
	"dracula":          "dracula",
	"nord":             "nord",
	"gruvbox":          "gruvbox-dark",
	"gruvbox-dark":     "gruvbox-dark",
	"tokyo":            "tokyo-night",
	"tokyo-night":      "tokyo-night",
	"solarized":        "solarized-dark",
	"solarized-dark":   "solarized-dark",
}

// Resolve returns the canonical palette name for name. The second result is
// false when name is not a known palette or alias.
func Resolve(name string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	canonical, ok := aliases[key]
	return canonical, ok
}

// Get resolves name, falling back to the default for unknown values.
func Get(name string) Palette {
	if canonical, ok := Resolve(name); ok {
		return palettes[canonical]
	}
	return palettes[DefaultName]
}

// Names lists palette names in cycle order.
func Names() []string {
	out := make([]string, len(order))
	copy(out, order)
	return out
}

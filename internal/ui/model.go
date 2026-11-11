// Package ui renders the attention pane, interactively or as a fixed block.
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ultus-net/attention-tui/internal/config"
	"github.com/ultus-net/attention-tui/internal/rules"
	"github.com/ultus-net/attention-tui/internal/store"
	"github.com/ultus-net/attention-tui/internal/theme"
)

// Row is one rendered item with the tier it landed in.
type Row struct {
	Item     rules.Item
	Verdict  rules.Verdict
	ChatName string
}

// Deps is everything the model needs. Store and Engine are shared with the
// poll loop; nothing here writes to Graph read state.
type Deps struct {
	Store  *store.Store
	Engine *rules.Engine
	Cfg    *config.Config
	Open   func(string) error
	Send   func(ctx context.Context, chatID, text string) error
	Banner string
	// Palette colours rows when Color is true. Color is false for a
	// non-terminal writer or when NO_COLOR is set.
	Palette theme.Palette
	Color   bool
	// EngineFunc lets the process swap in a rebuilt rules engine on SIGHUP.
	// When nil, Engine is used.
	EngineFunc func() *rules.Engine
}

// Model is the Bubble Tea model for the interactive pane.
type Model struct {
	deps Deps

	rows      []Row
	cursor    int
	banner    string
	err       string
	showQuiet bool

	filtering bool
	filter    string

	composer     bool
	composerText string

	width   int
	palette theme.Palette
	color   bool
}

type tickMsg struct{}
type reloadMsg struct{}
type sendErrMsg struct{ err error }

// BannerMsg replaces the banner line from outside the model.
type BannerMsg struct{ Text string }

// RefreshMsg asks the model to reload rows from the cache.
type RefreshMsg struct{}

// New builds a model and loads the current cache.
func New(d Deps) Model {
	m := Model{deps: d, banner: d.Banner, width: 80, palette: d.Palette, color: d.Color}
	m.reload()
	return m
}

// SetBanner replaces the banner line, e.g. "auth: no token".
func (m *Model) SetBanner(text string) {
	m.banner = text
}

// Init starts the reload ticker.
func (m Model) Init() tea.Cmd {
	return m.tickCmd()
}

func (m Model) tickCmd() tea.Cmd {
	interval := 60 * time.Second
	if m.deps.Cfg != nil && m.deps.Cfg.PollInterval.Std() > 0 {
		interval = m.deps.Cfg.PollInterval.Std()
	}
	return tea.Tick(interval, func(time.Time) tea.Msg { return tickMsg{} })
}

// Update handles keys, resize and reloads.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		return m, nil
	case tickMsg:
		m.reload()
		return m, m.tickCmd()
	case reloadMsg:
		m.reload()
		return m, nil
	case sendErrMsg:
		m.err = msg.err.Error()
		return m, nil
	case BannerMsg:
		m.banner = msg.Text
		return m, nil
	case RefreshMsg:
		m.reload()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.composer {
		return m.handleComposer(msg)
	}
	if m.filtering {
		return m.handleFilter(msg)
	}

	switch msg.String() {
	case "j", "down":
		if m.cursor < len(m.visibleRows())-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter":
		m.openCurrent()
	case "a":
		m.showQuiet = !m.showQuiet
		m.clampCursor()
	case "r":
		if row, ok := m.current(); ok && row.Item.Source == "chat" && m.deps.Send != nil {
			m.composer = true
			m.composerText = ""
		}
	case "/":
		m.filtering = true
		m.filter = ""
		m.cursor = 0
	case "R":
		m.reload()
	case "q", "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) handleFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyEsc:
		m.filtering = false
		m.filter = ""
	case msg.Type == tea.KeyEnter:
		m.filtering = false
	case isBackspace(msg):
		m.filter = dropLastRune(m.filter)
	case len(msg.Runes) > 0:
		m.filter += string(msg.Runes)
	}
	m.clampCursor()
	return m, nil
}

func (m Model) handleComposer(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyEsc:
		m.composer = false
		m.composerText = ""
	case isBackspace(msg):
		m.composerText = dropLastRune(m.composerText)
	case msg.Type == tea.KeyEnter:
		row, ok := m.current()
		if !ok || m.deps.Send == nil {
			m.composer = false
			return m, nil
		}
		text := strings.TrimSpace(m.composerText)
		if text == "" {
			m.composer = false
			return m, nil
		}
		chatID := row.Item.ChatID
		send := m.deps.Send
		m.composer = false
		m.composerText = ""
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := send(ctx, chatID, text); err != nil {
				return sendErrMsg{err: err}
			}
			return reloadMsg{}
		}
	case len(msg.Runes) > 0:
		m.composerText += string(msg.Runes)
	}
	return m, nil
}

// isBackspace covers every key a terminal may send for a backspace: 127,
// ctrl+h, and the delete escape sequence. The prompts are one line, so delete
// and backspace make the same edit.
func isBackspace(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyBackspace, tea.KeyCtrlH, tea.KeyDelete:
		return true
	}
	return false
}

func dropLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

func (m *Model) openCurrent() {
	row, ok := m.current()
	if !ok {
		return
	}
	if m.deps.Open != nil {
		if err := m.deps.Open(row.Item.URL); err != nil {
			m.err = err.Error()
		}
	}
	if m.deps.Store != nil {
		if err := m.deps.Store.SetSeen(context.Background(), row.Item.Source, row.Item.ID); err != nil {
			m.err = err.Error()
		}
	}
	m.reload()
}

func (m *Model) reload() {
	m.err = ""
	engine := m.deps.Engine
	if m.deps.EngineFunc != nil {
		engine = m.deps.EngineFunc()
	}
	if m.deps.Store == nil || engine == nil {
		m.rows = nil
		return
	}
	items, err := m.deps.Store.Items(context.Background())
	if err != nil {
		m.err = err.Error()
		return
	}
	rows := make([]Row, 0, len(items))
	for _, it := range items {
		name := it.ChatName
		if name == "" {
			name = it.From
		}
		rows = append(rows, Row{Item: it, Verdict: engine.Eval(it), ChatName: name})
	}
	m.rows = rows
	m.clampCursor()
}

func (m *Model) clampCursor() {
	n := len(m.visibleRows())
	if n == 0 {
		m.cursor = 0
		return
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// visibleRows applies the tier gate and the filter, in list order.
func (m Model) visibleRows() []Row {
	if m.deps.Cfg == nil {
		return nil
	}
	needle := strings.ToLower(strings.TrimSpace(m.filter))
	out := make([]Row, 0, len(m.rows))
	for _, row := range m.rows {
		if !row.Verdict.Show && !m.showQuiet {
			continue
		}
		if needle != "" && !rowMatches(row, needle) {
			continue
		}
		out = append(out, row)
	}
	return out
}

func rowMatches(row Row, needle string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		row.ChatName, row.Item.From, row.Item.Subject, row.Item.Preview,
	}, " "))
	return strings.Contains(haystack, needle)
}

func (m Model) current() (Row, bool) {
	rows := m.visibleRows()
	if m.cursor < 0 || m.cursor >= len(rows) {
		return Row{}, false
	}
	return rows[m.cursor], true
}

// View renders the whole pane.
func (m Model) View() string {
	rows := m.visibleRows()
	var b strings.Builder

	header := fmt.Sprintf("attention %d", len(rows))
	if m.filtering || m.filter != "" {
		header += "  /" + m.filter
	}
	b.WriteString(paint(header, m.palette.Accent, m.color))
	b.WriteString("\n")

	if m.banner != "" {
		b.WriteString(m.banner)
		b.WriteString("\n")
	}
	if m.err != "" {
		b.WriteString(m.err)
		b.WriteString("\n")
	}

	if len(rows) == 0 {
		b.WriteString("nothing needs you\n")
	}
	for i, row := range rows {
		marker := "  "
		if i == m.cursor {
			marker = "> "
		}
		b.WriteString(marker)
		b.WriteString(m.renderRow(row))
		b.WriteString("\n")
	}

	if m.composer {
		fmt.Fprintf(&b, "reply to %s: %s\n", m.currentName(), m.composerText)
	}
	b.WriteString("j/k move  enter open  a all  r reply  / filter  R reload  q quit\n")
	return b.String()
}

func (m Model) renderRow(row Row) string {
	tag := "[mail]"
	tagColor := m.palette.Mail
	if row.Item.Source == "chat" {
		tag = "[chat]"
		tagColor = m.palette.Chat
	}
	name := row.ChatName
	if name == "" {
		name = "(unknown)"
	}
	line := Truncate(fmt.Sprintf("%s %s  %s", tag, name, row.Item.Preview), max(10, m.width-2))
	if !m.color {
		return line
	}
	if row.Verdict.Notify {
		return paint(line, m.palette.Alert, true)
	}
	return paint(tag, tagColor, true) + strings.TrimPrefix(line, tag)
}

func (m Model) currentName() string {
	row, ok := m.current()
	if !ok {
		return ""
	}
	return row.ChatName
}

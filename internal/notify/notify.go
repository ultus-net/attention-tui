// Package notify sends one desktop notification per poll cycle, and only for
// alert-tier items.
package notify

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ErrUnavailable means no supported notifier was found on this system.
var ErrUnavailable = errors.New("notify: no desktop notifier available")

// Runner executes a notifier binary.
type Runner func(ctx context.Context, name string, args ...string) error

// Alert is one coalesced notification.
type Alert struct {
	Count  int
	Titles []string
}

// Notifier shells out to notify-send (Linux) or osascript (macOS). It is off by
// default and never blocks the UI: callers run it in a goroutine.
type Notifier struct {
	enabled bool
	goos    string
	look    func(string) (string, error)
	run     Runner
}

// New builds a notifier. enabled=false makes Send a no-op.
func New(enabled bool) *Notifier {
	return &Notifier{
		enabled: enabled,
		goos:    runtime.GOOS,
		look:    exec.LookPath,
		run: func(ctx context.Context, name string, args ...string) error {
			return exec.CommandContext(ctx, name, args...).Run()
		},
	}
}

// Enabled reports whether notifications are turned on.
func (n *Notifier) Enabled() bool { return n != nil && n.enabled }

// Send fires one notification. A missing notifier returns ErrUnavailable so
// callers stay quiet instead of nagging.
func (n *Notifier) Send(ctx context.Context, a Alert) error {
	if !n.Enabled() || a.Count <= 0 {
		return nil
	}
	title, body := Format(a)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	switch n.goos {
	case "linux":
		if _, err := n.look("notify-send"); err != nil {
			return ErrUnavailable
		}
		return n.run(ctx, "notify-send",
			"--app-name=attention-tui", "--urgency=normal", "--expire-time=8000", title, body)
	case "darwin":
		if _, err := n.look("osascript"); err != nil {
			return ErrUnavailable
		}
		script := fmt.Sprintf("display notification %s with title %s",
			osascriptQuote(body), osascriptQuote(title))
		return n.run(ctx, "osascript", "-e", script)
	default:
		return ErrUnavailable
	}
}

// Format builds the notification title and body. Kept pure for tests.
func Format(a Alert) (title, body string) {
	title = fmt.Sprintf("%d items need you", a.Count)
	if a.Count == 1 {
		title = "1 item needs you"
	}

	lines := make([]string, 0, 4)
	for _, t := range a.Titles {
		if strings.TrimSpace(t) == "" {
			continue
		}
		lines = append(lines, strings.TrimSpace(t))
	}
	if len(lines) > 3 {
		overflow := len(lines) - 3
		lines = lines[:3]
		lines = append(lines, fmt.Sprintf("and %d more", overflow))
	}
	body = strings.Join(lines, "\n")
	if body == "" {
		body = "Open attention-tui."
	}
	return title, body
}

func osascriptQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return `"` + s + `"`
}

package notify

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestFormatSingularAndPlural(t *testing.T) {
	title, body := Format(Alert{Count: 1, Titles: []string{"Ana: budget"}})
	if title != "1 item needs you" {
		t.Fatalf("title = %q", title)
	}
	if !strings.Contains(body, "Ana: budget") {
		t.Fatalf("body = %q", body)
	}
	title, _ = Format(Alert{Count: 3, Titles: []string{"a", "b", "c"}})
	if title != "3 items need you" {
		t.Fatalf("title = %q", title)
	}
}

func TestFormatCapsTitlesAtThree(t *testing.T) {
	_, body := Format(Alert{Count: 5, Titles: []string{"one", "two", "three", "four", "five"}})
	if !strings.Contains(body, "and 2 more") {
		t.Fatalf("body = %q, want an overflow line", body)
	}
	if strings.Contains(body, "four") || strings.Contains(body, "five") {
		t.Fatalf("body = %q, want only the first three titles", body)
	}
}

func TestDisabledIsSilent(t *testing.T) {
	n := New(false)
	var ran bool
	n.look = func(string) (string, error) { return "/usr/bin/notify-send", nil }
	n.run = func(context.Context, string, ...string) error {
		ran = true
		return nil
	}
	if err := n.Send(context.Background(), Alert{Count: 2, Titles: []string{"a", "b"}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ran {
		t.Fatal("a disabled notifier must run nothing")
	}
}

func TestMissingNotifierIsQuiet(t *testing.T) {
	n := New(true)
	n.goos = "linux"
	n.look = func(string) (string, error) { return "", exec.ErrNotFound }
	if err := n.Send(context.Background(), Alert{Count: 1, Titles: []string{"a"}}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

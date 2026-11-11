package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ultus-net/attention-tui/internal/auth"
	"github.com/ultus-net/attention-tui/internal/config"
	"github.com/ultus-net/attention-tui/internal/graph"
	"github.com/ultus-net/attention-tui/internal/rules"
)

type fakeSource struct {
	delta graph.MailDeltaResult
	err   error
}

func (f *fakeSource) MailDelta(ctx context.Context, url string) (graph.MailDeltaResult, error) {
	if f.err != nil {
		return graph.MailDeltaResult{}, f.err
	}
	return f.delta, nil
}

func (f *fakeSource) Chats(ctx context.Context, url string) ([]graph.Chat, string, error) {
	return nil, "", nil
}

func (f *fakeSource) ChatMessages(ctx context.Context, chatID string, top int) ([]graph.Message, error) {
	return nil, nil
}

func (f *fakeSource) ChatMemberNames(ctx context.Context, chatID string) ([]string, error) {
	return nil, nil
}

func setup(t *testing.T) (cfgPath, dataDir string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "config.yaml")
	dataDir = filepath.Join(dir, "data", "attention-tui")
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	return cfgPath, dataDir
}

func seedToken(t *testing.T, dataDir string) {
	t.Helper()
	st := auth.NewFileStore(filepath.Join(dataDir, "token.json"))
	if err := st.Save(auth.Tokens{
		AccessToken:  "access",
		RefreshToken: "refresh",
		Expiry:       time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed token: %v", err)
	}
}

func TestVersionFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	text := strings.TrimSpace(out.String())
	if strings.Contains(text, "\n") {
		t.Fatalf("version printed %d lines, want 1: %q", strings.Count(text, "\n")+1, text)
	}
	if !strings.Contains(text, "attention-tui") {
		t.Fatalf("version = %q", text)
	}
}

func TestMissingConfigCreatesSample(t *testing.T) {
	cfgPath, _ := setup(t)
	var out, errOut bytes.Buffer
	code := run([]string{"--config", cfgPath, "--once"}, &out, &errOut)
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("sample config was not written: %v", err)
	}
	if code == 2 {
		t.Fatalf("run treated the first-run config as a usage error: %s", errOut.String())
	}
}

func TestSecondInstanceIsReadOnly(t *testing.T) {
	cfgPath, dataDir := setup(t)
	lockPath := filepath.Join(dataDir, "state.db.lock")
	lock, primary, err := acquireLock(lockPath)
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}
	if !primary {
		t.Fatal("the first lock should be primary")
	}
	defer lock.Close()

	var out, errOut bytes.Buffer
	code := run([]string{"--config", cfgPath, "--once"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, want 0, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "secondary: cache only") {
		t.Fatalf("stdout = %q, want the secondary banner", out.String())
	}
}

func TestOnceWithUnauthenticatedReturnsBanner(t *testing.T) {
	cfgPath, _ := setup(t)
	var out, errOut bytes.Buffer
	code := run([]string{"--config", cfgPath, "--once"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "--login") {
		t.Fatalf("stderr = %q, want the --login hint", errOut.String())
	}
}

func TestRunOnceWritesBlock(t *testing.T) {
	cfgPath, dataDir := setup(t)
	seedToken(t, dataDir)

	original := newSource
	t.Cleanup(func() { newSource = original })
	newSource = func(*graph.Client) appSource { return &fakeSource{} }

	var out, errOut bytes.Buffer
	if code := run([]string{"--config", cfgPath, "--once"}, &out, &errOut); code != 0 {
		t.Fatalf("code = %d, want 0, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "attention 0") {
		t.Fatalf("stdout = %q, want the passive block", out.String())
	}

	newSource = func(*graph.Client) appSource { return &fakeSource{err: errors.New("boom")} }
	out.Reset()
	errOut.Reset()
	if code := run([]string{"--config", cfgPath, "--once"}, &out, &errOut); code != 1 {
		t.Fatalf("code = %d, want 1 on a fetch error", code)
	}
	if !strings.Contains(out.String(), "attention 0") {
		t.Fatalf("stdout = %q, want the block even when the fetch failed", out.String())
	}
	if !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("stderr = %q, want the fetch error", errOut.String())
	}
}

func TestSIGHUPReloadsRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	rt, err := newRuntime(path, cfg, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}

	it := rules.Item{Source: "chat", ChatName: "Deploy Bot", From: "Deploy Bot"}
	if rt.verdict(it).Show {
		t.Fatal("the sample config puts Deploy Bot in quiet")
	}

	allAlert := `poll_interval: 30s
tiers:
  alert:
    notify: true
    show: true
  watch:
    notify: false
    show: true
  quiet:
    notify: false
    show: false
defaults:
  mail: watch
  chat: quiet
rules:
  - name: everything
    tier: alert
`
	if err := os.WriteFile(path, []byte(allAlert), 0o600); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	go rt.handleSignals(ctx, sigs)
	sigs <- syscall.SIGHUP

	deadline := time.Now().Add(2 * time.Second)
	for {
		if rt.verdict(it).Show {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("SIGHUP did not rebuild the rules engine")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

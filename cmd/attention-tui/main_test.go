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

func TestBannerForAuthErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"no token", auth.ErrNoToken, "auth: no token; run attention-tui --login"},
		{"claims", auth.ErrInteractionRequired, "auth: sign-in required; run attention-tui --login"},
		{"wrapped claims", errors.Join(errors.New("graph: refresh after 401"), auth.ErrInteractionRequired), "auth: sign-in required; run attention-tui --login"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bannerFor(tc.err); got != tc.want {
				t.Fatalf("bannerFor = %q, want %q", got, tc.want)
			}
		})
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

func TestTerminalSizeFallsBackToEnv(t *testing.T) {
	t.Setenv("COLUMNS", "132")
	t.Setenv("LINES", "43")

	width, height, tty := terminalSize(&bytes.Buffer{})
	if tty {
		t.Fatal("a bytes.Buffer is not a terminal")
	}
	if width != 132 || height != 43 {
		t.Fatalf("size = %dx%d, want 132x43 from COLUMNS/LINES", width, height)
	}
}

func TestPaneSizeOffTerminalUsesFullWidth(t *testing.T) {
	t.Setenv("COLUMNS", "100")
	t.Setenv("LINES", "30")

	width, height, tty, color := paneSize(&bytes.Buffer{})
	if width != 100 || height != 30 {
		t.Fatalf("size = %dx%d, want 100x30", width, height)
	}
	if tty || color {
		t.Fatalf("tty = %v, color = %v; a buffer must be plain", tty, color)
	}
}

func TestColorEnabled(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	if !colorEnabled(true) {
		t.Fatal("colour should be on for a terminal without NO_COLOR")
	}
	if colorEnabled(false) {
		t.Fatal("colour must be off for a non-terminal")
	}
	t.Setenv("NO_COLOR", "1")
	if colorEnabled(true) {
		t.Fatal("NO_COLOR must suppress colour")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if colorEnabled(true) {
		t.Fatal("TERM=dumb must suppress colour")
	}
}

func TestBlockRendererRedrawsInPlace(t *testing.T) {
	var out bytes.Buffer
	r := &blockRenderer{out: &out, interactive: true}

	r.Render([]string{"one", "two", "three"})
	if got := out.String(); strings.Count(got, "\r\x1b[2K") != 3 {
		t.Fatalf("first render = %q, want three cleared lines", got)
	}

	out.Reset()
	r.Render([]string{"one", "two"})
	got := out.String()
	if !strings.HasPrefix(got, "\x1b[3A") {
		t.Fatalf("second render = %q, want a cursor-up of 3 lines first", got)
	}
	if strings.Count(got, "\r\x1b[2K") != 3 {
		t.Fatalf("second render = %q, want three cleared lines", got)
	}
	if !strings.Contains(got, "\x1b[1A") {
		t.Fatalf("second render = %q, want the cursor lifted back to line 2", got)
	}

	out.Reset()
	r.Render([]string{"one", "two"})
	if !strings.HasPrefix(out.String(), "\x1b[2A") {
		t.Fatalf("third render = %q, want a cursor-up of 2 lines", out.String())
	}
}

func TestBlockRendererPlainForNonTerminal(t *testing.T) {
	var out bytes.Buffer
	r := &blockRenderer{out: &out, interactive: false}
	r.Render([]string{"one", "two"})
	r.Finish()
	if got := out.String(); got != "one\ntwo\n" {
		t.Fatalf("output = %q, want a plain block", got)
	}
}

func TestPassivePipedPrintsOneBlockAndExits(t *testing.T) {
	cfgPath, dataDir := setup(t)
	seedToken(t, dataDir)

	original := newSource
	t.Cleanup(func() { newSource = original })
	newSource = func(*graph.Client) appSource { return &fakeSource{} }

	done := make(chan struct{})
	var out, errOut bytes.Buffer
	code := 0
	go func() {
		code = run([]string{"--config", cfgPath, "--passive"}, &out, &errOut)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("--passive did not exit for a piped writer")
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0, stderr: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "attention 0") {
		t.Fatalf("stdout = %q, want one block", out.String())
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("stdout = %q, want no escapes for a piped writer", out.String())
	}
}

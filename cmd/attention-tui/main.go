// Command attention-tui keeps one delegated Microsoft Graph session alive and
// renders an always-on, rule-filtered attention pane.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ultus-net/attention-tui/internal/app"
	"github.com/ultus-net/attention-tui/internal/auth"
	"github.com/ultus-net/attention-tui/internal/config"
	"github.com/ultus-net/attention-tui/internal/graph"
	"github.com/ultus-net/attention-tui/internal/notify"
	"github.com/ultus-net/attention-tui/internal/rules"
	"github.com/ultus-net/attention-tui/internal/store"
	"github.com/ultus-net/attention-tui/internal/theme"
	"github.com/ultus-net/attention-tui/internal/ui"
)

const version = "0.1.0"

// appSource is the runner's view of Graph. Tests override newSource.
type appSource = app.Source

var newSource = func(c *graph.Client) appSource { return c }

var scopes = []string{
	"offline_access",
	"User.Read",
	"Mail.Read",
	"Chat.Read",
	"ChatMessage.Send",
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("attention-tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "config file (default: XDG config dir)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	login := fs.Bool("login", false, "run the sign-in flow and exit")
	once := fs.Bool("once", false, "poll once, render one block, exit")
	passive := fs.Bool("passive", false, "render a fixed block on a loop")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "attention-tui %s\n", version)
		return 0
	}

	cfg, created, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "config: %v\n", err)
		return 1
	}
	path := *configPath
	if path == "" {
		path, err = config.DefaultPath()
		if err != nil {
			fmt.Fprintf(stderr, "config path: %v\n", err)
			return 1
		}
	}
	if created {
		fmt.Fprintf(stderr, "wrote a sample config to %s\n", path)
	}

	dataDir, err := config.DataDir()
	if err != nil {
		fmt.Fprintf(stderr, "data dir: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "data dir: %v\n", err)
		return 1
	}
	st, err := store.Open(filepath.Join(dataDir, "state.db"))
	if err != nil {
		fmt.Fprintf(stderr, "store: %v\n", err)
		return 1
	}
	defer st.Close()

	rt, err := newRuntime(path, cfg, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "rules: %v\n", err)
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP)
	defer signal.Stop(sigs)
	go rt.handleSignals(ctx, sigs)

	lock, primary, err := acquireLock(filepath.Join(dataDir, "state.db.lock"))
	if err != nil {
		fmt.Fprintf(stderr, "lock: %v\n", err)
		return 1
	}
	if lock != nil {
		defer lock.Close()
	}

	manager := auth.NewManager(cfg.Auth, scopes, auth.NewFileStore(filepath.Join(dataDir, "token.json")))

	switch {
	case *login:
		if !primary {
			fmt.Fprintln(stderr, "secondary: cache only; run --login in the primary instance")
			return 1
		}
		if err := manager.Login(ctx, openBrowser); err != nil {
			fmt.Fprintf(stderr, "login: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "signed in")
		return 0
	case *once:
		return runOnce(ctx, cfg, rt, st, manager, primary, stdout, stderr)
	case *passive:
		return runPassive(ctx, cfg, rt, st, manager, primary, stdout, stderr)
	default:
		return runInteractive(ctx, cfg, rt, st, manager, primary, stderr)
	}
}

func pollOnce(ctx context.Context, cfg *config.Config, mgr *auth.Manager, st *store.Store) error {
	if _, err := mgr.Token(ctx); err != nil {
		return err
	}
	client := graph.New("", mgr)
	runner := app.NewRunner(cfg, st, newSource(client))
	_, err := runner.Tick(ctx)
	return err
}

func runOnce(ctx context.Context, cfg *config.Config, rt *appRuntime, st *store.Store, mgr *auth.Manager, primary bool, stdout, stderr io.Writer) int {
	code := 0
	if primary {
		if err := pollOnce(ctx, cfg, mgr, st); err != nil {
			fmt.Fprintln(stderr, bannerFor(err))
			code = 1
		}
	} else {
		fmt.Fprintln(stdout, "secondary: cache only")
	}

	rows := rowsFromStore(ctx, st, rt)
	width, height := terminalSize()
	fmt.Fprintln(stdout, ui.RenderPassive(limitRows(rows, cfg.PassiveItems), width, height, theme.Get(cfg.Theme)))
	return code
}

func runPassive(ctx context.Context, cfg *config.Config, rt *appRuntime, st *store.Store, mgr *auth.Manager, primary bool, stdout, stderr io.Writer) int {
	var runner *app.Runner
	notifier := notify.New(cfg.Notifications)
	if primary {
		runner = app.NewRunner(cfg, st, newSource(graph.New("", mgr)))
	}
	width, height := terminalSize()

	for {
		if runner != nil {
			res, err := runner.Tick(ctx)
			if err != nil {
				fmt.Fprintln(stderr, bannerFor(err))
			} else {
				sendAlerts(ctx, notifier, res.Alerts)
			}
		}
		rows := rowsFromStore(ctx, st, rt)
		block := ui.RenderPassive(limitRows(rows, cfg.PassiveItems), width, height, theme.Get(rt.config().Theme))
		fmt.Fprint(stdout, "\x1b[H\x1b[2J", block, "\n")

		wait := cfg.PollInterval.Std()
		if runner != nil && runner.Backoff() > 0 {
			wait = runner.Backoff()
		}
		if !sleepCtx(ctx, wait) {
			return 0
		}
	}
}

func runInteractive(ctx context.Context, cfg *config.Config, rt *appRuntime, st *store.Store, mgr *auth.Manager, primary bool, stderr io.Writer) int {
	banner := ""
	if !primary {
		banner = "secondary: cache only"
	}
	client := graph.New("", mgr)
	deps := ui.Deps{
		Store:      st,
		Engine:     rt.engineSnapshot(),
		EngineFunc: rt.engineSnapshot,
		Cfg:        cfg,
		Open:       openBrowser,
		Send:       client.SendChatMessage,
		Banner:     banner,
	}
	if !primary {
		deps.Send = nil
	}

	program := tea.NewProgram(ui.New(deps))
	if primary {
		go pollLoop(ctx, program, cfg, st, mgr)
	}
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(stderr, "ui: %v\n", err)
		return 1
	}
	return 0
}

func pollLoop(ctx context.Context, program *tea.Program, cfg *config.Config, st *store.Store, mgr *auth.Manager) {
	runner := app.NewRunner(cfg, st, newSource(graph.New("", mgr)))
	notifier := notify.New(cfg.Notifications)
	for {
		res, err := runner.Tick(ctx)
		if err != nil {
			program.Send(ui.BannerMsg{Text: bannerFor(err)})
			wait := runner.Backoff()
			if wait <= 0 {
				wait = time.Minute
			}
			if !sleepCtx(ctx, wait) {
				return
			}
			continue
		}
		program.Send(ui.BannerMsg{Text: ""})
		program.Send(ui.RefreshMsg{})
		sendAlerts(ctx, notifier, res.Alerts)
		if !sleepCtx(ctx, cfg.PollInterval.Std()) {
			return
		}
	}
}

func sendAlerts(ctx context.Context, notifier *notify.Notifier, alerts []rules.Item) {
	if len(alerts) == 0 || !notifier.Enabled() {
		return
	}
	titles := make([]string, 0, len(alerts))
	for _, it := range alerts {
		titles = append(titles, alertTitle(it))
	}
	alert := notify.Alert{Count: len(alerts), Titles: titles}
	go func() {
		_ = notifier.Send(ctx, alert)
	}()
}

func alertTitle(it rules.Item) string {
	name := it.ChatName
	if name == "" {
		name = it.From
	}
	if name == "" {
		name = it.Subject
	}
	if it.Preview != "" {
		return name + ": " + it.Preview
	}
	return name
}

func bannerFor(err error) string {
	if errors.Is(err, auth.ErrNoToken) {
		return "auth: no token; run attention-tui --login"
	}
	if err == nil {
		return ""
	}
	return "network: " + err.Error()
}

func rowsFromStore(ctx context.Context, st *store.Store, rt *appRuntime) []ui.Row {
	items, err := st.Items(ctx)
	if err != nil {
		return nil
	}
	rows := make([]ui.Row, 0, len(items))
	for _, it := range items {
		verdict := rt.verdict(it)
		if !verdict.Show {
			continue
		}
		name := it.ChatName
		if name == "" {
			name = it.From
		}
		rows = append(rows, ui.Row{Item: it, Verdict: verdict, ChatName: name})
	}
	return rows
}

func limitRows(rows []ui.Row, max int) []ui.Row {
	if max > 0 && len(rows) > max {
		return rows[:max]
	}
	return rows
}

func terminalSize() (int, int) {
	width, height := 80, 24
	if v, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && v > 0 {
		width = v
	}
	if v, err := strconv.Atoi(os.Getenv("LINES")); err == nil && v > 0 {
		height = v
	}
	return width, height
}

func openBrowser(url string) error {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	return exec.Command(cmd, url).Start()
}

func acquireLock(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("flock: %w", err)
	}
	return f, true, nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// runtime holds the config and compiled rules, and swaps both on SIGHUP.
type appRuntime struct {
	path   string
	stderr io.Writer
	mu     sync.RWMutex
	cfg    *config.Config
	engine *rules.Engine
}

func newRuntime(path string, cfg *config.Config, stderr io.Writer) (*appRuntime, error) {
	engine, err := rules.New(cfg)
	if err != nil {
		return nil, err
	}
	return &appRuntime{path: path, stderr: stderr, cfg: cfg, engine: engine}, nil
}

func (r *appRuntime) verdict(it rules.Item) rules.Verdict {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.engine.Eval(it)
}

func (r *appRuntime) engineSnapshot() *rules.Engine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.engine
}

func (r *appRuntime) config() *config.Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

func (r *appRuntime) reload() error {
	cfg, _, err := config.Load(r.path)
	if err != nil {
		return fmt.Errorf("reload config: %w", err)
	}
	engine, err := rules.New(cfg)
	if err != nil {
		return fmt.Errorf("reload rules: %w", err)
	}
	r.mu.Lock()
	r.cfg = cfg
	r.engine = engine
	r.mu.Unlock()
	return nil
}

func (r *appRuntime) handleSignals(ctx context.Context, sigs <-chan os.Signal) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig, ok := <-sigs:
			if !ok {
				return
			}
			if sig != syscall.SIGHUP {
				continue
			}
			if err := r.reload(); err != nil && r.stderr != nil {
				fmt.Fprintf(r.stderr, "%v\n", err)
			}
		}
	}
}

// Package config loads attention-tui settings from a YAML file and provides
// XDG-correct paths for the config file and state database.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultPollInterval  = 60 * time.Second
	MinPollInterval      = 30 * time.Second
	MaxPollInterval      = 24 * time.Hour
	DefaultPassiveItems  = 20
	DefaultBackfillDays  = 7
	DefaultBackfillMax   = 200
	DefaultTheme         = "catppuccin-mocha"
	DefaultRedirectPort  = 53147
	defaultTenant        = "organizations"
	defaultAuthMode      = "public"
	minPollIntervalLabel = "30s"
)

var themes = []string{
	"catppuccin-mocha",
	"dracula",
	"nord",
	"gruvbox-dark",
	"tokyo-night",
	"solarized-dark",
}

// Duration is a time.Duration that decodes from Go duration strings in YAML.
type Duration time.Duration

func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag == "!!int" {
		var seconds int64
		if err := node.Decode(&seconds); err != nil {
			return fmt.Errorf("invalid duration %q (use 30s, 10m, 2h)", node.Value)
		}
		*d = Duration(time.Duration(seconds) * time.Second)
		return nil
	}
	var raw string
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("invalid duration %q (use 30s, 10m, 2h)", node.Value)
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid duration %q (use 30s, 10m, 2h)", raw)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) MarshalYAML() (any, error) {
	dur := time.Duration(d)
	switch {
	case dur%time.Hour == 0:
		return fmt.Sprintf("%dh", int(dur.Hours())), nil
	case dur%time.Minute == 0:
		return fmt.Sprintf("%dm", int(dur.Minutes())), nil
	default:
		return dur.String(), nil
	}
}

// Auth is the Entra app registration and token flow settings.
type Auth struct {
	ClientID     string `yaml:"client_id"`
	Tenant       string `yaml:"tenant"`
	Mode         string `yaml:"mode"`
	RedirectPort int    `yaml:"redirect_port"`
}

// Tier is one attention tier.
type Tier struct {
	Notify bool `yaml:"notify"`
	Show   bool `yaml:"show"`
}

// Match is one rule predicate. An absent field matches everything.
type Match struct {
	Source     string   `yaml:"source,omitempty"`
	From       []string `yaml:"from,omitempty"`
	Subject    string   `yaml:"subject,omitempty"`
	Chat       []string `yaml:"chat,omitempty"`
	Mention    *bool    `yaml:"mention,omitempty"`
	Importance string   `yaml:"importance,omitempty"`
	Focused    *bool    `yaml:"focused,omitempty"`
	Flagged    *bool    `yaml:"flagged,omitempty"`
}

// Rule assigns a tier when its predicate matches.
type Rule struct {
	Name  string `yaml:"name"`
	Match Match  `yaml:"match"`
	Tier  string `yaml:"tier,omitempty"`
}

// Config is the on-disk configuration.
type Config struct {
	PollInterval  Duration          `yaml:"poll_interval"`
	PassiveItems  int               `yaml:"passive_items"`
	BackfillDays  int               `yaml:"backfill_days"`
	BackfillMax   int               `yaml:"backfill_max"`
	Theme         string            `yaml:"theme"`
	Browser       string            `yaml:"browser"`
	Notifications bool              `yaml:"notifications"`
	Auth          Auth              `yaml:"auth"`
	Tiers         map[string]Tier   `yaml:"tiers"`
	Defaults      map[string]string `yaml:"defaults"`
	Rules         []Rule            `yaml:"rules"`
}

// DefaultPath is $XDG_CONFIG_HOME/attention-tui/config.yaml.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return filepath.Join(dir, "attention-tui", "config.yaml"), nil
}

// DataDir is $XDG_DATA_HOME/attention-tui, falling back to ~/.local/share.
func DataDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return filepath.Join(dir, "attention-tui"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".local", "share", "attention-tui"), nil
}

// Load reads path. When the file is missing it writes the sample config and
// returns it with created=true.
func Load(path string) (cfg *Config, created bool, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path, err = DefaultPath()
		if err != nil {
			return nil, false, err
		}
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg, parseErr := parse([]byte(sampleYAML))
		if parseErr != nil {
			return nil, false, fmt.Errorf("sample config is invalid: %w", parseErr)
		}
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil {
			return nil, false, fmt.Errorf("create config dir: %w", mkErr)
		}
		if writeErr := os.WriteFile(path, []byte(sampleYAML), 0o644); writeErr != nil {
			return nil, false, fmt.Errorf("write sample config: %w", writeErr)
		}
		return cfg, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read config: %w", err)
	}
	cfg, err = parse(data)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, false, nil
}

// Save writes cfg to path and keeps the previous file at path.bak. The write is
// atomic so an interrupted save cannot truncate the live config.
func Save(path string, cfg *Config) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("config: empty path")
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := os.WriteFile(path+".bak", existing, 0o600); err != nil {
			return fmt.Errorf("back up config: %w", err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("read config for backup: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.PollInterval <= 0 {
		c.PollInterval = Duration(DefaultPollInterval)
	}
	if c.PassiveItems <= 0 {
		c.PassiveItems = DefaultPassiveItems
	}
	if c.BackfillDays <= 0 {
		c.BackfillDays = DefaultBackfillDays
	}
	if c.BackfillMax <= 0 {
		c.BackfillMax = DefaultBackfillMax
	}
	if len(c.Tiers) == 0 {
		c.Tiers = map[string]Tier{
			"alert": {Notify: true, Show: true},
			"watch": {Notify: false, Show: true},
			"quiet": {Notify: false, Show: false},
		}
	}
	if len(c.Defaults) == 0 {
		c.Defaults = map[string]string{"mail": "watch", "chat": "quiet"}
	}
	if strings.TrimSpace(c.Theme) == "" {
		c.Theme = DefaultTheme
	}
	c.Theme = strings.ToLower(strings.TrimSpace(c.Theme))
	if strings.TrimSpace(c.Auth.Tenant) == "" {
		c.Auth.Tenant = defaultTenant
	}
	if strings.TrimSpace(c.Auth.Mode) == "" {
		c.Auth.Mode = defaultAuthMode
	}
	c.Auth.Mode = strings.ToLower(strings.TrimSpace(c.Auth.Mode))
	if c.Auth.RedirectPort == 0 {
		c.Auth.RedirectPort = DefaultRedirectPort
	}
}

// Validate rejects settings the rest of the program cannot honour.
func (c Config) Validate() error {
	if d := c.PollInterval.Std(); d < MinPollInterval || d > MaxPollInterval {
		return fmt.Errorf("poll_interval must be between %s and 24h, got %s", minPollIntervalLabel, d)
	}
	if c.PassiveItems < 1 || c.PassiveItems > 200 {
		return fmt.Errorf("passive_items must be between 1 and 200, got %d", c.PassiveItems)
	}
	if c.BackfillDays < 1 || c.BackfillDays > 30 {
		return fmt.Errorf("backfill_days must be between 1 and 30, got %d", c.BackfillDays)
	}
	if c.BackfillMax < 1 || c.BackfillMax > 2000 {
		return fmt.Errorf("backfill_max must be between 1 and 2000, got %d", c.BackfillMax)
	}
	if !contains(themes, c.Theme) {
		return fmt.Errorf("theme %q is not one of %s", c.Theme, strings.Join(themes, ", "))
	}
	if c.Auth.Mode != "public" && c.Auth.Mode != "confidential" {
		return fmt.Errorf("auth.mode must be public or confidential, got %q", c.Auth.Mode)
	}
	if c.Auth.RedirectPort < 1 || c.Auth.RedirectPort > 65535 {
		return fmt.Errorf("auth.redirect_port must be between 1 and 65535, got %d", c.Auth.RedirectPort)
	}
	if len(c.Tiers) == 0 {
		return errors.New("at least one tier must be configured")
	}
	for source, tier := range c.Defaults {
		if source != "mail" && source != "chat" {
			return fmt.Errorf("defaults: unknown source %q", source)
		}
		if _, ok := c.Tiers[tier]; !ok {
			return fmt.Errorf("defaults: source %q names unknown tier %q", source, tier)
		}
	}
	for i, r := range c.Rules {
		if strings.TrimSpace(r.Name) == "" {
			return fmt.Errorf("rules[%d]: name is required", i)
		}
		if r.Match.Source != "" && r.Match.Source != "mail" && r.Match.Source != "chat" {
			return fmt.Errorf("rules[%d] %q: source must be mail or chat", i, r.Name)
		}
		if r.Tier != "" {
			if _, ok := c.Tiers[r.Tier]; !ok {
				return fmt.Errorf("rules[%d] %q: unknown tier %q", i, r.Name, r.Tier)
			}
		}
		if r.Match.Importance != "" {
			switch r.Match.Importance {
			case "high", "normal", "low":
			default:
				return fmt.Errorf("rules[%d] %q: importance must be high, normal or low", i, r.Name)
			}
		}
	}
	return nil
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

const sampleYAML = `# attention-tui configuration
#
# This file is read at startup. Deleting it regenerates this sample.
# State (cached items, delta link, watermarks) lives in
# ~/.local/share/attention-tui/state.db and survives config edits.
#
# Durations use Go syntax: 30s, 10m, 2h.

poll_interval: 60s

# Rows shown by --passive.
passive_items: 20

# How much history the first sync or a delta restart imports, so a cold start
# cannot announce months of unread mail.
backfill_days: 7
backfill_max: 200

# catppuccin-mocha | dracula | nord | gruvbox-dark | tokyo-night | solarized-dark
theme: catppuccin-mocha

# Empty uses the system opener (xdg-open on Linux, open on macOS).
browser: ""

# Desktop notification for alert-tier rows. Off by default.
notifications: false

auth:
  client_id: ""
  # organizations, never common: Teams chat permissions reject personal accounts.
  tenant: organizations
  # public sends no secret. confidential reads one from the OS keyring.
  mode: public
  redirect_port: 53147

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

# Rules are evaluated top to bottom. First match wins. A rule with no tier
# inherits the default for its source. Glob supports * only, case-insensitively.
rules:
  - name: mentions
    match:
      mention: true
    tier: alert

  - name: vip-senders
    match:
      from:
        - cfo@example.com
        - "*@legal.example.com"
    tier: alert

  - name: high-importance
    match:
      importance: high
    tier: alert

  - name: bots
    match:
      chat:
        - Deploy Bot
        - "#build-*"
    tier: quiet
`

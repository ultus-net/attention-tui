package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSampleConfigParses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	cfg, created, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !created {
		t.Fatal("expected the sample config to be created on first load")
	}
	if got := cfg.PollInterval.Std(); got != 60*time.Second {
		t.Fatalf("poll interval = %s, want 60s", got)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("sample config must validate: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("sample config not written: %v", err)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	p := writeConfig(t, "pasive_items: 10\ntiers:\n  alert:\n    notify: true\n    show: true\n")
	_, _, err := Load(p)
	if err == nil {
		t.Fatal("expected an unknown field to be rejected")
	}
	if !strings.Contains(err.Error(), "pasive_items") {
		t.Fatalf("error should name the bad field, got %v", err)
	}
}

func TestDurationParsing(t *testing.T) {
	var got struct {
		A Duration `yaml:"a"`
		B Duration `yaml:"b"`
	}
	src := `a: 90s
b: 45
`
	if err := yaml.Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.A.Std() != 90*time.Second {
		t.Fatalf("a = %s, want 90s", got.A.Std())
	}
	if got.B.Std() != 45*time.Second {
		t.Fatalf("b = %s, want 45s", got.B.Std())
	}

	var bad struct {
		A Duration `yaml:"a"`
	}
	if err := yaml.Unmarshal([]byte("a: banana\n"), &bad); err == nil {
		t.Fatal("expected a bad duration to be rejected")
	}
}

func TestPollIntervalFloor(t *testing.T) {
	base := `tiers:
  alert:
    notify: true
    show: true
  watch:
    notify: false
    show: true
  quiet:
    notify: false
    show: false
`
	tooShort := writeConfig(t, base+"poll_interval: 10s\n")
	if _, _, err := Load(tooShort); err == nil {
		t.Fatal("expected 10s poll interval to be rejected")
	}
	ok := writeConfig(t, base+"poll_interval: 30s\n")
	if _, _, err := Load(ok); err != nil {
		t.Fatalf("30s should be accepted: %v", err)
	}
	tooLong := writeConfig(t, base+"poll_interval: 25h\n")
	if _, _, err := Load(tooLong); err == nil {
		t.Fatal("expected 25h poll interval to be rejected")
	}
}

func TestTenantDefault(t *testing.T) {
	cfg, _, err := Load(writeConfig(t, "poll_interval: 30s\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.Tenant != "organizations" {
		t.Fatalf("tenant = %q, want organizations", cfg.Auth.Tenant)
	}
	if cfg.Auth.Mode != "public" {
		t.Fatalf("mode = %q, want public", cfg.Auth.Mode)
	}
	if cfg.Auth.RedirectPort != 53147 {
		t.Fatalf("redirect port = %d, want 53147", cfg.Auth.RedirectPort)
	}
}

func TestSaveLeavesBackup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := Save(p, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(p + ".bak"); err != nil {
		t.Fatalf("expected a backup file: %v", err)
	}
}

func TestPaths(t *testing.T) {
	cfgPath, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if !strings.HasSuffix(cfgPath, filepath.Join("attention-tui", "config.yaml")) {
		t.Fatalf("DefaultPath = %q", cfgPath)
	}
	dataDir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if !strings.HasSuffix(dataDir, "attention-tui") {
		t.Fatalf("DataDir = %q", dataDir)
	}
}

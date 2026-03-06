package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_Defaults_WhenNoFile(t *testing.T) {
	// Load with no args uses the default search path (~/.petri/config.yaml).
	// A missing default config file is not an error.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with missing default file should not error, got: %v", err)
	}
	if cfg.State.Backend == "" {
		t.Error("expected default state backend")
	}
	if cfg.Observability.MetricsPort == 0 {
		t.Error("expected default metrics port")
	}
	if cfg.Credentials.MasterKeyPath == "" {
		t.Error("expected default master key path")
	}
}

func TestLoad_ExplicitMissingFile_ReturnsError(t *testing.T) {
	// When an explicit file path is given and does not exist, Load returns an error.
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("expected error for explicit non-existent config file")
	}
}

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := `
state:
  backend: sqlite
  sqlite_path: /tmp/test.db
observability:
  log_level: debug
  metrics_port: 8080
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.State.Backend != "sqlite" {
		t.Errorf("state.backend: got %q, want %q", cfg.State.Backend, "sqlite")
	}
	if cfg.State.SQLitePath != "/tmp/test.db" {
		t.Errorf("state.sqlite_path: got %q, want %q", cfg.State.SQLitePath, "/tmp/test.db")
	}
	if cfg.Observability.LogLevel != "debug" {
		t.Errorf("observability.log_level: got %q, want %q", cfg.Observability.LogLevel, "debug")
	}
	if cfg.Observability.MetricsPort != 8080 {
		t.Errorf("observability.metrics_port: got %d, want 8080", cfg.Observability.MetricsPort)
	}
}

func TestLoad_ExpandsHomeTilde(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := `
state:
  backend: sqlite
  sqlite_path: "~/petri.db"
credentials:
  master_key_path: "~/.petri/master.key"
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, _ := os.UserHomeDir()
	if !strings.HasPrefix(cfg.State.SQLitePath, home) {
		t.Errorf("sqlite_path not expanded: %q", cfg.State.SQLitePath)
	}
	if !strings.HasPrefix(cfg.Credentials.MasterKeyPath, home) {
		t.Errorf("master_key_path not expanded: %q", cfg.Credentials.MasterKeyPath)
	}
}

func TestLoad_DefaultPath_WhenNoArgument(t *testing.T) {
	// Load with no args uses ~/.petri/config.yaml which may or may not exist.
	// Either way, no error expected (file is optional).
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() should not error when config file is missing: %v", err)
	}
	if cfg == nil {
		t.Error("expected non-nil config")
	}
}

func TestExpandHome_NoTilde(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"", ""},
	}
	for _, tt := range tests {
		got := expandHome(tt.input)
		if got != tt.expected {
			t.Errorf("expandHome(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestExpandHome_WithTilde(t *testing.T) {
	home, _ := os.UserHomeDir()
	got := expandHome("~/.petri/config.yaml")
	expected := filepath.Join(home, ".petri/config.yaml")
	if got != expected {
		t.Errorf("expandHome(~/.petri/config.yaml) = %q, want %q", got, expected)
	}
}

func TestLoadCompanies_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(badPath, []byte("not: valid: yaml: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCompanies(badPath)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

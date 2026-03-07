// Package config loads and validates Petri configuration from YAML files and environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jaimegago/petri/pkg/types"
)

// State backend identifiers.
const (
	BackendSQLite     = "sqlite"
	BackendPostgres   = "postgresql"
)

// Git provider identifiers.
const (
	ProviderGitHub = "github"
	ProviderGitLab = "gitlab"
)

// Default tool versions.
const (
	DefaultTerraformVersion = "1.7.0"
	DefaultPulumiVersion    = "3.100.0"
)

// Default observability settings.
const (
	DefaultMetricsPort = 9090
)

// Default cleanup durations.
const (
	DefaultCheckInterval = 5 * time.Minute
	DefaultGracePeriod   = 30 * time.Minute
)

// Config holds the full Petri configuration.
type Config struct {
	State         StateConfig         `yaml:"state"`
	Credentials   CredentialsConfig   `yaml:"credentials"`
	Observability ObservabilityConfig `yaml:"observability"`
	Git           GitConfig           `yaml:"git"`
	Cloud         CloudConfig         `yaml:"cloud"`
	Cleanup       CleanupConfig       `yaml:"cleanup"`
}

// StateConfig configures the state backend.
type StateConfig struct {
	Backend          string `yaml:"backend"`
	ConnectionString string `yaml:"connection_string"`
	SQLitePath       string `yaml:"sqlite_path"`
}

// CredentialsConfig configures credential storage.
type CredentialsConfig struct {
	MasterKeyPath string `yaml:"master_key_path"`
}

// ObservabilityConfig configures logging, metrics, and tracing.
type ObservabilityConfig struct {
	MetricsEnabled  bool   `yaml:"metrics_enabled"`
	MetricsPort     int    `yaml:"metrics_port"`
	TracingEnabled  bool   `yaml:"tracing_enabled"`
	TracingEndpoint string `yaml:"tracing_endpoint"`
	LogLevel        string `yaml:"log_level"`
}

// GitConfig configures git provider defaults.
type GitConfig struct {
	DefaultProvider string `yaml:"default_provider"`
}

// CloudConfig configures cloud tool versions.
type CloudConfig struct {
	TerraformVersion string `yaml:"terraform_version"`
	PulumiVersion    string `yaml:"pulumi_version"`
}

// CleanupConfig configures TTL-based cleanup behavior.
type CleanupConfig struct {
	CheckInterval time.Duration `yaml:"check_interval"`
	GracePeriod   time.Duration `yaml:"grace_period"`
}

// CompaniesFile is the top-level structure of companies.yaml.
type CompaniesFile struct {
	Companies []types.Company `yaml:"companies"`
}

// DefaultConfig returns sensible defaults for all config sections.
func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		State: StateConfig{
			Backend:    BackendSQLite,
			SQLitePath: filepath.Join(home, ".petri", "petri.db"),
		},
		Credentials: CredentialsConfig{
			MasterKeyPath: filepath.Join(home, ".petri", "master.key"),
		},
		Observability: ObservabilityConfig{
			MetricsEnabled: true,
			MetricsPort:    DefaultMetricsPort,
			LogLevel:       "info",
		},
		Git: GitConfig{
			DefaultProvider: ProviderGitHub,
		},
		Cloud: CloudConfig{
			TerraformVersion: DefaultTerraformVersion,
			PulumiVersion:    DefaultPulumiVersion,
		},
		Cleanup: CleanupConfig{
			CheckInterval: DefaultCheckInterval,
			GracePeriod:   DefaultGracePeriod,
		},
	}
}

// Load reads Petri configuration from ~/.petri/config.yaml and environment variables.
// If cfgFile is non-empty it is used as the config path instead of the default location.
func Load(cfgFile ...string) (*Config, error) {
	cfg := DefaultConfig()

	path, err := resolveConfigPath(cfgFile...)
	if err != nil {
		return nil, err
	}

	// Config file is optional at the default path, but required when explicitly provided.
	explicitPath := len(cfgFile) > 0 && cfgFile[0] != ""
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) || explicitPath {
			return nil, fmt.Errorf("reading config %s: %w", path, err)
		}
	}
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config %s: %w", path, err)
		}
	}

	applyEnvOverrides(cfg)

	// Expand ~ in file paths so users can write ~/.petri/... in config.yaml.
	cfg.State.SQLitePath = expandHome(cfg.State.SQLitePath)
	cfg.Credentials.MasterKeyPath = expandHome(cfg.Credentials.MasterKeyPath)

	return cfg, nil
}

// resolveConfigPath returns the config file path to load.
func resolveConfigPath(cfgFile ...string) (string, error) {
	if len(cfgFile) > 0 && cfgFile[0] != "" {
		return cfgFile[0], nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".petri", "config.yaml"), nil
}

// applyEnvOverrides applies PETRI_* environment variables over the loaded config.
// Only operationally useful fields are exposed; add entries here as needed.
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("PETRI_STATE_BACKEND"); v != "" {
		cfg.State.Backend = v
	}
	if v := os.Getenv("PETRI_STATE_CONNECTION_STRING"); v != "" {
		cfg.State.ConnectionString = v
	}
	if v := os.Getenv("PETRI_STATE_SQLITE_PATH"); v != "" {
		cfg.State.SQLitePath = v
	}
	if v := os.Getenv("PETRI_CREDENTIALS_MASTER_KEY_PATH"); v != "" {
		cfg.Credentials.MasterKeyPath = v
	}
	if v := os.Getenv("PETRI_LOG_LEVEL"); v != "" {
		cfg.Observability.LogLevel = v
	}
}

// expandHome replaces a leading ~ with the current user's home directory.
func expandHome(path string) string {
	if len(path) == 0 || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

// LoadCompanies reads company definitions from the given YAML file path.
func LoadCompanies(path string) ([]types.Company, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading companies file %s: %w", path, err)
	}

	var cf CompaniesFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parsing companies file: %w", err)
	}

	for i := range cf.Companies {
		if err := cf.Companies[i].Validate(); err != nil {
			return nil, fmt.Errorf("invalid company config: %w", err)
		}
	}

	return cf.Companies, nil
}

// PetriDir returns the path to ~/.petri.
func PetriDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".petri"), nil
}

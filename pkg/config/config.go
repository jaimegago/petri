// Package config loads and validates Petri configuration from YAML files and environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"github.com/jaimegago/petri/pkg/types"
)

// Config holds the full Petri configuration.
type Config struct {
	State         StateConfig         `mapstructure:"state"`
	Credentials   CredentialsConfig   `mapstructure:"credentials"`
	Observability ObservabilityConfig `mapstructure:"observability"`
	Git           GitConfig           `mapstructure:"git"`
	Cloud         CloudConfig         `mapstructure:"cloud"`
	Cleanup       CleanupConfig       `mapstructure:"cleanup"`
}

// StateConfig configures the state backend.
type StateConfig struct {
	Backend          string `mapstructure:"backend"`
	ConnectionString string `mapstructure:"connection_string"`
	SQLitePath       string `mapstructure:"sqlite_path"`
}

// CredentialsConfig configures credential storage.
type CredentialsConfig struct {
	MasterKeyPath string `mapstructure:"master_key_path"`
}

// ObservabilityConfig configures logging, metrics, and tracing.
type ObservabilityConfig struct {
	MetricsEnabled  bool   `mapstructure:"metrics_enabled"`
	MetricsPort     int    `mapstructure:"metrics_port"`
	TracingEnabled  bool   `mapstructure:"tracing_enabled"`
	TracingEndpoint string `mapstructure:"tracing_endpoint"`
	LogLevel        string `mapstructure:"log_level"`
}

// GitConfig configures git provider defaults.
type GitConfig struct {
	DefaultProvider string `mapstructure:"default_provider"`
}

// CloudConfig configures cloud tool versions.
type CloudConfig struct {
	TerraformVersion string `mapstructure:"terraform_version"`
	PulumiVersion    string `mapstructure:"pulumi_version"`
}

// CleanupConfig configures TTL-based cleanup behavior.
type CleanupConfig struct {
	CheckInterval time.Duration `mapstructure:"check_interval"`
	GracePeriod   time.Duration `mapstructure:"grace_period"`
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
			Backend:          "postgresql",
			ConnectionString: "postgres://localhost/petri?sslmode=disable",
			SQLitePath:       filepath.Join(home, ".petri", "petri.db"),
		},
		Credentials: CredentialsConfig{
			MasterKeyPath: filepath.Join(home, ".petri", "master.key"),
		},
		Observability: ObservabilityConfig{
			MetricsEnabled: true,
			MetricsPort:    9090,
			LogLevel:       "info",
		},
		Git: GitConfig{
			DefaultProvider: "github",
		},
		Cloud: CloudConfig{
			TerraformVersion: "1.7.0",
			PulumiVersion:    "3.100.0",
		},
		Cleanup: CleanupConfig{
			CheckInterval: 5 * time.Minute,
			GracePeriod:   30 * time.Minute,
		},
	}
}

// Load reads Petri configuration from ~/.petri/config.yaml and environment variables.
// If cfgFile is non-empty it is used as the config path instead of the default location.
func Load(cfgFile ...string) (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}

	v := viper.New()
	v.SetEnvPrefix("PETRI")
	v.AutomaticEnv()

	if len(cfgFile) > 0 && cfgFile[0] != "" {
		v.SetConfigFile(cfgFile[0])
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(filepath.Join(home, ".petri"))
	}

	// Set defaults.
	cfg := DefaultConfig()
	v.SetDefault("state.backend", cfg.State.Backend)
	v.SetDefault("state.connection_string", cfg.State.ConnectionString)
	v.SetDefault("credentials.master_key_path", cfg.Credentials.MasterKeyPath)
	v.SetDefault("observability.metrics_enabled", cfg.Observability.MetricsEnabled)
	v.SetDefault("observability.metrics_port", cfg.Observability.MetricsPort)
	v.SetDefault("observability.log_level", cfg.Observability.LogLevel)
	v.SetDefault("git.default_provider", cfg.Git.DefaultProvider)
	v.SetDefault("cloud.terraform_version", cfg.Cloud.TerraformVersion)
	v.SetDefault("cloud.pulumi_version", cfg.Cloud.PulumiVersion)
	v.SetDefault("cleanup.check_interval", "5m")
	v.SetDefault("cleanup.grace_period", "30m")

	if err := v.ReadInConfig(); err != nil {
		// Config file is optional.
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config: %w", err)
		}
	}

	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Expand ~ in file paths so users can write ~/.petri/... in config.yaml.
	cfg.State.SQLitePath = expandHome(cfg.State.SQLitePath)
	cfg.Credentials.MasterKeyPath = expandHome(cfg.Credentials.MasterKeyPath)

	return cfg, nil
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

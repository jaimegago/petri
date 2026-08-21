package main

import (
	"errors"
	"fmt"
	"strconv"
)

// Config is the service's validated startup configuration. Every field is read
// from the environment once, at startup; the process refuses to start on an
// invalid configuration rather than running with a default it cannot honour.
type Config struct {
	// Port is the HTTP listen port. PORT, default 8080.
	Port int
	// SMTP is the outbound mail configuration. Present only when SMTP_HOST is
	// set; a service that is not configured to send mail does not need it.
	SMTP *SMTPConfig
}

// SMTPConfig is the outbound mail relay the service delivers through.
type SMTPConfig struct {
	Host string
	Port int
}

// Getenv looks up one environment variable, reporting whether it was set.
// os.LookupEnv satisfies it; tests supply a map.
type Getenv func(key string) (string, bool)

// LoadConfig reads and validates the configuration from env.
//
// Validation is the service's own: a variable it needs and cannot find is
// reported by name, as a configuration error, before anything else starts.
// Errors are joined so an operator sees every problem in one start attempt.
func LoadConfig(env Getenv) (Config, error) {
	var errs []error
	cfg := Config{Port: 8080}

	if raw, ok := env("PORT"); ok {
		port, err := parsePort("PORT", raw)
		if err != nil {
			errs = append(errs, err)
		} else {
			cfg.Port = port
		}
	}

	if host, ok := env("SMTP_HOST"); ok {
		smtp, err := loadSMTP(host, env)
		if err != nil {
			errs = append(errs, err)
		} else {
			cfg.SMTP = smtp
		}
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

// loadSMTP reads the mail relay settings. SMTP_HOST being set is what
// enables outbound mail, and a relay without a port cannot be dialled, so
// SMTP_PORT is required whenever SMTP_HOST is.
func loadSMTP(host string, env Getenv) (*SMTPConfig, error) {
	if host == "" {
		return nil, errors.New("SMTP_HOST must not be empty")
	}
	raw, ok := env("SMTP_PORT")
	if !ok {
		return nil, errors.New("SMTP_PORT is required when SMTP_HOST is set")
	}
	port, err := parsePort("SMTP_PORT", raw)
	if err != nil {
		return nil, err
	}
	return &SMTPConfig{Host: host, Port: port}, nil
}

// parsePort parses a TCP port, naming the variable in any error.
func parsePort(name, raw string) (int, error) {
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer port, got %q", name, raw)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be in 1..65535, got %d", name, port)
	}
	return port, nil
}

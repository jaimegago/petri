package main

import (
	"strings"
	"testing"
)

func mapEnv(m map[string]string) Getenv {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
		check   func(t *testing.T, cfg Config)
	}{
		{
			name: "empty environment starts on the default port without mail",
			env:  map[string]string{},
			check: func(t *testing.T, cfg Config) {
				if cfg.Port != 8080 || cfg.SMTP != nil {
					t.Fatalf("got %+v", cfg)
				}
			},
		},
		{
			name: "complete smtp configuration",
			env:  map[string]string{"SMTP_HOST": "smtp.internal", "SMTP_PORT": "587"},
			check: func(t *testing.T, cfg Config) {
				if cfg.SMTP == nil || cfg.SMTP.Host != "smtp.internal" || cfg.SMTP.Port != 587 {
					t.Fatalf("got %+v", cfg.SMTP)
				}
			},
		},
		{
			name:    "smtp host without port is a configuration error naming the key",
			env:     map[string]string{"SMTP_HOST": "smtp.internal"},
			wantErr: "SMTP_PORT is required when SMTP_HOST is set",
		},
		{
			name:    "empty smtp host",
			env:     map[string]string{"SMTP_HOST": "", "SMTP_PORT": "587"},
			wantErr: "SMTP_HOST must not be empty",
		},
		{
			name:    "non-numeric smtp port",
			env:     map[string]string{"SMTP_HOST": "smtp.internal", "SMTP_PORT": "abc"},
			wantErr: `SMTP_PORT must be an integer port, got "abc"`,
		},
		{
			name:    "out of range listen port",
			env:     map[string]string{"PORT": "70000"},
			wantErr: "PORT must be in 1..65535",
		},
		{
			name:    "every error is reported in one attempt",
			env:     map[string]string{"PORT": "x", "SMTP_HOST": "smtp.internal"},
			wantErr: "PORT must be an integer port",
			check:   nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadConfig(mapEnv(tc.env))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				if tc.name == "every error is reported in one attempt" &&
					!strings.Contains(err.Error(), "SMTP_PORT is required") {
					t.Fatalf("second error missing from joined error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, cfg)
			}
		})
	}
}

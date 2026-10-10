package pulumi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testSecret = "s3cr3t-Value_x"

func TestSetSecretConfig(t *testing.T) {
	tests := []struct {
		name    string
		opts    SecretConfigOptions
		execFn  func(ctx context.Context, workDir string, env []string, args ...string) error
		wantErr string
		check   func(t *testing.T, r *mockRunner)
	}{
		{
			name: "value on stdin, never in args",
			opts: SecretConfigOptions{WorkDir: "/w", StackName: "lab", Key: "dbPassword", Value: testSecret},
			check: func(t *testing.T, r *mockRunner) {
				want := []string{"config", "set", "--secret", "--non-interactive", "--stack", "lab", "dbPassword"}
				if len(r.calls) != 1 || !slices.Equal(r.calls[0], want) {
					t.Fatalf("calls = %v, want [%v]", r.calls, want)
				}
				if len(r.inputs) != 1 || r.inputs[0] != testSecret {
					t.Errorf("stdin did not carry the value")
				}
				for _, a := range r.calls[0] {
					if strings.Contains(a, testSecret) {
						t.Errorf("value appears in args")
					}
				}
			},
		},
		{
			name:    "empty key refused",
			opts:    SecretConfigOptions{StackName: "lab", Value: testSecret},
			wantErr: "empty key",
		},
		{
			name:    "empty value refused",
			opts:    SecretConfigOptions{StackName: "lab", Key: "dbPassword"},
			wantErr: "empty value",
		},
		{
			name: "runner error wrapped without the value",
			opts: SecretConfigOptions{StackName: "lab", Key: "dbPassword", Value: testSecret},
			execFn: func(context.Context, string, []string, ...string) error {
				return errors.New("backend unreachable")
			},
			wantErr: `pulumi config set "dbPassword" on stack "lab": backend unreachable`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{execFn: tt.execFn}
			err := newWithRunner(r).SetSecretConfig(context.Background(), tt.opts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), testSecret) {
					t.Error("error carries the value")
				}
				return
			}
			if err != nil {
				t.Fatalf("SetSecretConfig: %v", err)
			}
			if tt.check != nil {
				tt.check(t, r)
			}
		})
	}
}

// TestCLIRunner_ExecInputWritesStdin runs a stand-in binary to check the
// real runner hands the input to the process on stdin and not in argv.
func TestCLIRunner_ExecInputWritesStdin(t *testing.T) {
	dir := t.TempDir()
	stdinOut, argsOut := filepath.Join(dir, "stdin"), filepath.Join(dir, "args")
	bin := filepath.Join(dir, "fake-pulumi")
	script := "#!/bin/sh\ncat > " + stdinOut + "\necho \"$@\" > " + argsOut + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	p := New(Config{Binary: bin})
	err := p.SetSecretConfig(context.Background(), SecretConfigOptions{
		WorkDir: dir, StackName: "lab", Key: "dbPassword", Value: testSecret,
	})
	if err != nil {
		t.Fatalf("SetSecretConfig: %v", err)
	}
	gotIn, err := os.ReadFile(stdinOut)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotIn) != testSecret {
		t.Error("process stdin did not carry the value")
	}
	gotArgs, err := os.ReadFile(argsOut)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(gotArgs), testSecret) {
		t.Error("value reached argv")
	}
	if !strings.Contains(string(gotArgs), "config set --secret") {
		t.Errorf("argv = %q", gotArgs)
	}
}

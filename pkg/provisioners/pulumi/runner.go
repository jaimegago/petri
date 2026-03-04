package pulumi

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// cliRunner implements pulumiRunner by shelling out to the real pulumi CLI.
type cliRunner struct {
	binary string
}

func (r *cliRunner) exec(ctx context.Context, workDir string, env []string, args ...string) error {
	_, err := r.capture(ctx, workDir, env, args...)
	return err
}

func (r *cliRunner) capture(ctx context.Context, workDir string, env []string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Dir = workDir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(),
		"PULUMI_SKIP_UPDATE_CHECK=1",  // suppress version-check HTTP calls
		"PULUMI_SKIP_CONFIRMATIONS=1", // treat all interactive prompts as confirmed
	)
	cmd.Env = append(cmd.Env, env...)

	if err := cmd.Run(); err != nil {
		errMsg := parsePulumiError(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("pulumi %s: %w: %s", args[0], err, errMsg)
		}
		return "", fmt.Errorf("pulumi %s: %w", args[0], err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// parsePulumiError extracts a concise error message from pulumi stderr output.
// It collects lines that start with "error:" or contain failure keywords.
func parsePulumiError(stderr string) string {
	var lines []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "error:") ||
			strings.HasPrefix(lower, "panic:") ||
			strings.Contains(lower, "failed to") ||
			strings.Contains(lower, "update failed") {
			lines = append(lines, line)
		}
	}
	if len(lines) > 6 {
		lines = lines[:6]
	}
	return strings.Join(lines, "; ")
}

package terraform

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// cliRunner implements tfRunner by shelling out to the real terraform CLI.
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
		"TF_IN_AUTOMATION=1",   // suppress interactive prompts and spinners
		"TF_INPUT=0",           // disable interactive variable prompts
		"CHECKPOINT_DISABLE=1", // disable upgrade-check HTTP calls
	)
	cmd.Env = append(cmd.Env, env...)

	if err := cmd.Run(); err != nil {
		errMsg := parseTerraformError(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("terraform %s: %w: %s", args[0], err, errMsg)
		}
		return "", fmt.Errorf("terraform %s: %w", args[0], err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// parseTerraformError strips Terraform's box-drawing UI decorations from
// stderr and returns a condensed, readable error message.
func parseTerraformError(stderr string) string {
	var lines []string
	for _, line := range strings.Split(stderr, "\n") {
		// Remove box-drawing prefixes used by Terraform's diagnostic renderer.
		for _, prefix := range []string{"│ ", "╷", "╵", "╰ ", "├─"} {
			line = strings.TrimPrefix(line, prefix)
		}
		line = strings.TrimSpace(line)
		// Skip blank lines and bare box characters.
		if line == "" || line == "│" || line == "╷" || line == "╵" {
			continue
		}
		lines = append(lines, line)
	}
	// Keep the most useful leading lines only.
	if len(lines) > 6 {
		lines = lines[:6]
	}
	return strings.Join(lines, "; ")
}

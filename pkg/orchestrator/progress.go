package orchestrator

import (
	"fmt"
	"log/slog"
)

// progress tracks and reports workflow steps to both the logger and stdout.
type progress struct {
	log   *slog.Logger
	step  int
	total int
}

// newProgress creates a progress reporter for a workflow with the given step count.
func newProgress(total int, log *slog.Logger) *progress {
	return &progress{log: log, total: total}
}

// Step advances the step counter and prints the current step to stdout.
func (p *progress) Step(name string) {
	p.step++
	p.log.Info("progress", "step", p.step, "total", p.total, "action", name)
	fmt.Printf("[%d/%d] %s\n", p.step, p.total, name)
}

// Done prints a completion message.
func (p *progress) Done(msg string) {
	fmt.Printf("✓ %s\n", msg)
}

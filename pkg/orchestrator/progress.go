package orchestrator

import (
	"fmt"

	"github.com/rs/zerolog"
)

// progress tracks and reports workflow steps to both the logger and stdout.
type progress struct {
	log   zerolog.Logger
	step  int
	total int
}

// newProgress creates a progress reporter for a workflow with the given step count.
func newProgress(total int, log zerolog.Logger) *progress {
	return &progress{log: log, total: total}
}

// Step advances the step counter and prints the current step to stdout.
func (p *progress) Step(name string) {
	p.step++
	p.log.Info().Int("step", p.step).Int("total", p.total).Str("action", name).Msg("progress")
	fmt.Printf("[%d/%d] %s\n", p.step, p.total, name)
}

// Done prints a completion message.
func (p *progress) Done(msg string) {
	fmt.Printf("✓ %s\n", msg)
}

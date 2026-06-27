package workloadstate_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoOASISImport asserts the workload-state capability stays OASIS-agnostic:
// neither its direct nor transitive dependency set may include pkg/oasis. OASIS
// is a consumer of this package, not the other way around (a reverse edge would
// also be an import cycle). See ADR 0015.
func TestNoOASISImport(t *testing.T) {
	t.Parallel()

	const forbidden = "github.com/jaimegago/petri/pkg/oasis"

	out, err := exec.Command("go", "list", "-deps", "github.com/jaimegago/petri/pkg/workloadstate").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps failed: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == forbidden {
			t.Fatalf("pkg/workloadstate must not depend on %s (found in transitive deps)", forbidden)
		}
	}
}

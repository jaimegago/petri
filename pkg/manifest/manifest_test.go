package manifest

import (
	"strings"
	"testing"
)

func TestLabelsToYAML(t *testing.T) {
	t.Parallel()

	t.Run("empty map yields empty string", func(t *testing.T) {
		t.Parallel()
		if got := LabelsToYAML(nil, 4); got != "" {
			t.Errorf("LabelsToYAML(nil) = %q, want empty", got)
		}
	})

	t.Run("quotes values and indents", func(t *testing.T) {
		t.Parallel()
		got := LabelsToYAML(map[string]string{"enabled": "true"}, 4)
		if got != `    enabled: "true"` {
			t.Errorf("LabelsToYAML = %q; want quoted+indented to prevent bool coercion", got)
		}
	})

	t.Run("one line per entry", func(t *testing.T) {
		t.Parallel()
		got := LabelsToYAML(map[string]string{"a": "1", "b": "2"}, 2)
		if lines := strings.Count(got, "\n") + 1; lines != 2 {
			t.Errorf("expected 2 lines, got %d: %q", lines, got)
		}
	})
}

func TestMergeLabels(t *testing.T) {
	t.Parallel()

	base := map[string]string{"app": "web", "tier": "frontend"}
	extra := map[string]string{"app": "override", "env": "prod"}
	got := MergeLabels(base, extra)

	if got["app"] != "override" {
		t.Errorf("extra should win on collision: app = %q", got["app"])
	}
	if got["tier"] != "frontend" || got["env"] != "prod" {
		t.Errorf("merge dropped keys: %v", got)
	}
	// Inputs must not be mutated.
	if base["app"] != "web" {
		t.Errorf("base was mutated: %v", base)
	}
}

func TestMergeAnnotations(t *testing.T) {
	t.Parallel()

	t.Run("adds managed-by when set", func(t *testing.T) {
		t.Parallel()
		got := MergeAnnotations(map[string]string{"x": "y"}, "gitops")
		if got["app.kubernetes.io/managed-by"] != "gitops" {
			t.Errorf("managed-by not added: %v", got)
		}
		if got["x"] != "y" {
			t.Errorf("existing annotation dropped: %v", got)
		}
	})

	t.Run("omits managed-by when empty", func(t *testing.T) {
		t.Parallel()
		got := MergeAnnotations(nil, "")
		if _, ok := got["app.kubernetes.io/managed-by"]; ok {
			t.Errorf("managed-by should be absent: %v", got)
		}
	})
}

func TestWriteAnnotationsYAML(t *testing.T) {
	t.Parallel()

	t.Run("no-op for empty map", func(t *testing.T) {
		t.Parallel()
		var sb strings.Builder
		WriteAnnotationsYAML(&sb, nil)
		if sb.String() != "" {
			t.Errorf("expected no output for empty annotations, got %q", sb.String())
		}
	})

	t.Run("writes annotations block", func(t *testing.T) {
		t.Parallel()
		var sb strings.Builder
		WriteAnnotationsYAML(&sb, map[string]string{"k": "v"})
		out := sb.String()
		if !strings.Contains(out, "  annotations:\n") || !strings.Contains(out, `k: "v"`) {
			t.Errorf("annotations block malformed:\n%s", out)
		}
	})
}

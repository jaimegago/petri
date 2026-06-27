package preflight

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Render writes a human-readable view of the report to w. Same code path is
// used by `petri verify` (writing to stdout) and `petri serve --verify`
// (writing to a logger sink), so it makes no assumptions about color or
// terminal capabilities — plain ASCII glyphs only. The first write error, if
// any, is returned; later writes are skipped once an error is seen.
func Render(w io.Writer, r *Report) error {
	mode := "default"
	if r.Deep {
		mode = "deep"
	}
	var err error
	p := func(format string, args ...any) {
		if err != nil {
			return
		}
		_, err = fmt.Fprintf(w, format, args...)
	}

	p("Petri preflight report (%s)\n", mode)
	p("%s\n", strings.Repeat("=", 28))

	for _, c := range r.Checks {
		glyph := glyphFor(c.Status)
		trail := formatDuration(c.Duration)
		// On a passing multi-arch image probe the resolved platform is the
		// only signal in the report that says which manifest we actually
		// verified — surface it next to the duration so it's visible
		// without grepping the JSON output.
		if c.Status == StatusPass && c.Platform != "" {
			trail = fmt.Sprintf("%s, %s", c.Platform, trail)
		}
		p("  %s %-44s %s  (%s)\n", glyph, c.Title, statusLabel(c.Status), trail)
		if c.Summary != "" && c.Status != StatusPass {
			p("      %s\n", c.Summary)
		}
		if c.Diagnostic != "" {
			for _, line := range strings.Split(strings.TrimRight(c.Diagnostic, "\n"), "\n") {
				p("      %s\n", line)
			}
		}
		if c.NextSteps != "" {
			p("      → %s\n", c.NextSteps)
		}
	}
	p("\n")
	if r.Passed {
		p("Result: PASS  (%s total)\n", formatDuration(r.Duration))
	} else {
		p("Result: FAIL  (%s total)\n", formatDuration(r.Duration))
	}
	return err
}

// RenderJSON writes the report as JSON. Output is stable enough to parse in
// CI (every field on Report and Check is tagged).
func RenderJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// glyphFor maps a Status to its single-character renderer glyph. We use
// ASCII-safe characters (no emoji) so the output renders the same in
// terminals and in JSON-pretty-printed log lines. ✓ ✗ ⊘ would be friendlier
// but break in some logger sinks.
func glyphFor(s Status) string {
	switch s {
	case StatusPass:
		return "[PASS]"
	case StatusFail:
		return "[FAIL]"
	case StatusSkip:
		return "[SKIP]"
	}
	return "[????]"
}

// statusLabel returns the short string written next to a check title.
func statusLabel(s Status) string {
	switch s {
	case StatusPass:
		return "pass"
	case StatusFail:
		return "fail"
	case StatusSkip:
		return "skip"
	}
	return string(s)
}

// formatDuration prints check timings compactly. Sub-second resolutions
// are useful here — `2ms` reads better than `0.002s`.
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return d.Round(10 * time.Millisecond).String()
	}
}

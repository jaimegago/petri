package version

import (
	"regexp"
	"runtime/debug"
	"testing"
)

// semverRE is the semantic-version grammar of semver.org §2.0.0, the form
// OASIS Provider Conformance §3.8.2 asks `provider_version` to take.
var semverRE = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

func vcsInfo(revision, modified string) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		info := &debug.BuildInfo{}
		if revision != "" {
			info.Settings = append(info.Settings, debug.BuildSetting{Key: "vcs.revision", Value: revision})
		}
		if modified != "" {
			info.Settings = append(info.Settings, debug.BuildSetting{Key: "vcs.modified", Value: modified})
		}
		return info, true
	}
}

func TestBuild(t *testing.T) {
	t.Parallel()

	const revision = "b46fec58f07013ef3773c00a5184de80c28c485c"
	noInfo := func() (*debug.BuildInfo, bool) { return nil, false }

	tests := []struct {
		name    string
		stamped string
		read    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{name: "stamped build wins over the VCS stamp", stamped: "1234567-dirty-unpinned", read: vcsInfo(revision, "false"), want: "1234567-dirty-unpinned"},
		{name: "stamped build needs no build info", stamped: "1234567", read: noInfo, want: "1234567"},
		{name: "clean VCS stamp is the short commit", read: vcsInfo(revision, "false"), want: "b46fec5"},
		{name: "modified VCS stamp is marked dirty", read: vcsInfo(revision, "true"), want: "b46fec5-dirty"},
		{name: "short revision is kept whole", read: vcsInfo("b46f", "false"), want: "b46f"},
		{name: "build info without a revision is unknown", read: vcsInfo("", "true"), want: "unknown"},
		{name: "no build info is unknown", read: noInfo, want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := build(tt.stamped, tt.read); got != tt.want {
				t.Errorf("build(%q) = %q, want %q", tt.stamped, got, tt.want)
			}
		})
	}
}

// The provider version is declared in semver (OASIS Provider Conformance
// §3.8.2), so every form String can take has to be one.
func TestString_IsSemver(t *testing.T) {
	t.Parallel()

	for _, b := range []string{"b46fec5", "b46fec5-dirty", "b46fec5-unpinned", "b46fec5-dirty-unpinned", "1234567", unknownBuild} {
		if v := Version + "+" + b; !semverRE.MatchString(v) {
			t.Errorf("%q is not a semantic version", v)
		}
	}
	if got := String(); !semverRE.MatchString(got) {
		t.Errorf("String() = %q, not a semantic version", got)
	}
}

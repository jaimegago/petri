// Package version reports what a petri binary is: its semantic version and the
// source it was built from. `petri --version` and the provider version declared
// at the OASIS conformance handshake are the same string, read from here.
// See ADR 0021.
package version

import "runtime/debug"

// Version is petri's semantic version. No git tag carries it: the repository's
// only tags are svc/v*, the application image's. A variable so that a release
// build can set it with -ldflags -X.
var Version = "0.1.0"

// Build identifies the source a binary was built from: the short commit, then
// -dirty when the working tree differed from it. A build sets it:
//
//	go build -ldflags "-X github.com/jaimegago/petri/internal/version.Build=b46fec5-dirty"
//
// `make build` and `make install` do. A builder that knows more about the
// checkout appends its own markers the same way; joe-oasis-e2e adds -unpinned.
// Only letters, digits and hyphens keep String a valid semantic version.
//
// Left empty, the commit and the dirty marker are read from the VCS stamp the
// Go toolchain embeds. That stamp exists for a `go build` of the package in a
// plain clone. It is absent in a git worktree, in a submodule checkout, and
// under `go run` and `go test`, and a build with no stamp at all reports
// "unknown".
var Build = ""

// shortCommitLen is the length a VCS-stamped revision is cut to: git's own
// default for `rev-parse --short`.
const shortCommitLen = 7

// unknownBuild is what a binary with neither stamp reports.
const unknownBuild = "unknown"

// String returns Version with the build as semantic-version build metadata:
// "0.1.0+b46fec5", "0.1.0+b46fec5-dirty", "0.1.0+unknown". Build metadata takes
// no part in version precedence, so two builds of one version compare equal as
// versions and still read as different builds.
func String() string {
	return Version + "+" + build(Build, debug.ReadBuildInfo)
}

// build resolves the build identity: the stamped value when there is one, the
// toolchain's VCS stamp otherwise.
func build(stamped string, readBuildInfo func() (*debug.BuildInfo, bool)) string {
	if stamped != "" {
		return stamped
	}
	info, ok := readBuildInfo()
	if !ok || info == nil {
		return unknownBuild
	}
	var revision string
	var modified bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return unknownBuild
	}
	if len(revision) > shortCommitLen {
		revision = revision[:shortCommitLen]
	}
	if modified {
		revision += "-dirty"
	}
	return revision
}

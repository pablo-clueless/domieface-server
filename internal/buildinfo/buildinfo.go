// Package buildinfo reports what this binary was built from, for the health
// endpoints and the startup log.
//
// It answers the question you actually have during an incident: is the thing
// serving traffic the thing I think I deployed?
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// version is stamped at build time:
//
//	go build -ldflags "-X domieface/com/internal/buildinfo.version=1.4.0"
//
// The Makefile does this from its VERSION variable.
var version string

// Version returns the release version, falling back to the VCS revision the
// toolchain embeds, and finally to "dev" for a plain `go build` or `go run`.
func Version() string {
	if version != "" {
		return version
	}
	if revision := Revision(); revision != "" {
		return revision
	}
	return "dev"
}

// Revision returns the short VCS revision, with "-dirty" appended when the
// working tree had uncommitted changes at build time. It is empty when the
// binary was not built from a repository.
func Revision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}

	var revision string
	var modified bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return ""
	}

	// A full hash is noise in a health response; the first 12 characters are
	// unambiguous in any repository you will ever have.
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if modified {
		revision += "-dirty"
	}
	return revision
}

// GoVersion reports the toolchain that built this binary.
func GoVersion() string { return strings.TrimPrefix(runtime.Version(), "go") }

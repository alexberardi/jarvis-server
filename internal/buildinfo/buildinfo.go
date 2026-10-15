// Package buildinfo describes a binary's build: the version stamped with -ldflags -X for
// releases (and dev scripts), and otherwise the VCS details `go build` embeds on its own when
// run in a git checkout (vcs.revision, vcs.time, vcs.modified).
package buildinfo

import (
	"runtime/debug"
	"strings"
	"time"
)

// VCS is the commit a binary was built from, as go build recorded it.
type VCS struct {
	Revision string    // full commit hash
	Time     time.Time // the commit's time; zero when not recorded
	Modified bool      // the tree had uncommitted changes
}

// Read returns this binary's VCS details; false when the build has none (go run, go test,
// -buildvcs=false, or built outside a checkout).
func Read() (VCS, bool) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return VCS{}, false
	}
	return FromSettings(bi.Settings)
}

// FromSettings picks the VCS details out of a build's settings; false without a revision.
func FromSettings(settings []debug.BuildSetting) (VCS, bool) {
	var v VCS
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			v.Revision = s.Value
		case "vcs.time":
			v.Time, _ = time.Parse(time.RFC3339, s.Value)
		case "vcs.modified":
			v.Modified = s.Value == "true"
		}
	}
	return v, v.Revision != ""
}

// Short is the abbreviated revision, with "+dirty" when the tree was modified.
func (v VCS) Short() string {
	s := v.Revision[:min(len(v.Revision), 7)]
	if v.Modified {
		s += "+dirty"
	}
	return s
}

// IsDev reports whether version is a development build's ("dev", the default, or the dev
// scripts' "dev-…") rather than a release tag.
func IsDev(version string) bool {
	return version == "dev" || strings.HasPrefix(version, "dev-")
}

// Describe is what `version` prints: a release's tag as is; a dev build's version followed by
// its revision when go build recorded one, e.g. "dev (0123abc+dirty)".
func Describe(version string, v VCS, ok bool) string {
	if !IsDev(version) || !ok {
		return version
	}
	return version + " (" + v.Short() + ")"
}

// Version is Describe for this binary.
func Version(version string) string {
	v, ok := Read()
	return Describe(version, v, ok)
}

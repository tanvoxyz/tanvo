// Package buildinfo reports the version of the running binary from the
// build information the Go toolchain embeds (the module version and the
// VCS revision).
package buildinfo

import (
	"runtime/debug"
)

// Info describes the binary.
type Info struct {
	// Version is the main module's version ("v1.2.3", or "(devel)").
	Version string
	// Revision is the VCS revision, when known.
	Revision string
	// Modified reports uncommitted changes in the build.
	Modified bool
	// GoVersion is the toolchain that built the binary.
	GoVersion string
}

// Read returns the binary's build information.
func Read() Info {
	info := Info{Version: "(devel)"}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	info.GoVersion = bi.GoVersion
	if v := bi.Main.Version; v != "" {
		info.Version = v
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Revision = s.Value
		case "vcs.modified":
			info.Modified = s.Value == "true"
		}
	}
	return info
}

// String returns "version (revision)".
func (i Info) String() string {
	s := i.Version
	if i.Revision != "" {
		rev := i.Revision[:min(len(i.Revision), 12)]
		if i.Modified {
			rev += "-dirty"
		}
		s += " (" + rev + ")"
	}
	return s
}

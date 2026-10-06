// Package buildinfo reports which binary is running: its version, and the commit it was built
// from. There are two sources, in order of precedence.
//
//   - A link-time stamp: `-X github.com/bavix/sol/internal/buildinfo.version=v1.2.3`. `make
//     build` does this with `git describe`, so a local build is identifiable as well.
//   - The metadata the toolchain embeds on its own. A build from a checkout gets a
//     pseudo-version naming the tree - `v0.0.0-20261006002016-fadb54ddefce+dirty` - and a build
//     from `go install ...@v1.2.3` gets that tag. This is what the release pipeline produces
//     with a plain `go build`, so it needs no cooperation from a workflow this repository does
//     not control.
//
// "dev" is the last resort, for a build that carries no metadata at all. Version is deliberately
// not guessed from the revision ("dev+abc1234") because Revision reports the commit separately: a
// bug report can name the exact tree without pretending the build had a release number.
package buildinfo

import "runtime/debug"

const (
	// devVersion is what an unstamped build without any embedded metadata reports.
	devVersion = "dev"
	// develVersion is what the toolchain writes for a build that is not a module release.
	develVersion = "(devel)"
	// revisionLength keeps the reported commit short; twelve hex digits are unambiguous in
	// practice and fit on a status line.
	revisionLength = 12
)

// version is stamped at link time (-X ...buildinfo.version=...); empty means "not stamped".
var version = ""

// Version returns the version string to display.
func Version() string {
	if version != "" {
		return version
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != develVersion {
			return v
		}
	}

	return devVersion
}

// Revision returns the commit the binary was built from, shortened, with a "-dirty" suffix when
// the tree had uncommitted changes at build time. It is empty when the build carries no VCS
// information at all, which happens for a module install or a build outside a checkout.
func Revision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}

	var (
		revision string
		modified bool
	)

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

	if len(revision) > revisionLength {
		revision = revision[:revisionLength]
	}

	if modified {
		revision += "-dirty"
	}

	return revision
}

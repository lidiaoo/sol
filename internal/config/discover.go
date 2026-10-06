package config

import (
	"os"
)

// Source names the level of the discovery order that supplied a configuration path. Reporting it
// is what turns "which file is actually in use" from a guess into an answer, both for the
// start-up log line and for `sol paths` (install design §5.4). The values are deliberately the
// spellings an operator would type.
type Source string

// The discovery levels, in the order they are checked.
const (
	// SourceFlag is an explicit --config path.
	SourceFlag Source = "--config"
	// SourceEnv is the SOL_CONFIG environment variable.
	SourceEnv Source = "$SOL_CONFIG"
	// SourceSystem is the system-wide default location, the first entry of DefaultPaths.
	SourceSystem Source = "system"
	// SourceUser is the per-user default location, the second entry of DefaultPaths.
	SourceUser Source = "user"
	// SourceNone means no file was found, so the built-in defaults apply.
	SourceNone Source = "none"
)

// Location is the configuration file Load reads together with the level that chose it.
type Location struct {
	// Path is the file Load reads; it is empty when no file exists.
	Path string
	// Source is where Path came from.
	Source Source
}

// Discover reports the path Load would read together with its source. An explicit path and
// SOL_CONFIG are taken as given - Load then fails loudly when they do not exist instead of
// falling back - while the default locations are probed in order and the first existing one wins.
func Discover(path string) Location {
	return discover(path, DefaultPaths())
}

// discover is Discover with the default locations injected, so every branch is testable without
// writing to /etc.
func discover(path string, candidates []string) Location {
	if path != "" {
		return Location{Path: path, Source: SourceFlag}
	}

	if fromEnv := os.Getenv(EnvConfig); fromEnv != "" {
		return Location{Path: fromEnv, Source: SourceEnv}
	}

	for index, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return Location{Path: candidate, Source: candidateSource(index)}
		}
	}

	return Location{Source: SourceNone}
}

// candidateSource names the default location at index: the system-wide file first, then the
// per-user one.
func candidateSource(index int) Source {
	if index == 0 {
		return SourceSystem
	}

	return SourceUser
}

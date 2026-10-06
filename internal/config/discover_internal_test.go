package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// discoveryCase is one discover() input and the answer it must produce.
type discoveryCase struct {
	name       string
	path       string
	env        string
	candidates []string
	wantPath   string
	wantSource Source
}

// runDiscoveryCases keeps the two tables below short enough to read in one screen.
func runDiscoveryCases(t *testing.T, tests []discoveryCase) {
	t.Helper()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(EnvConfig, testCase.env)

			got := discover(testCase.path, testCase.candidates)
			if got.Path != testCase.wantPath || got.Source != testCase.wantSource {
				t.Fatalf("discover() = %+v, want path %q with source %q", got, testCase.wantPath, testCase.wantSource)
			}
		})
	}
}

func TestDiscoverPrefersTheFlagThenTheEnvironment(t *testing.T) {
	system := writeConfigForDiscovery(t, t.TempDir())
	user := writeConfigForDiscovery(t, t.TempDir())
	explicitMissing := filepath.Join(t.TempDir(), "missing-explicit.yaml")

	runDiscoveryCases(t, []discoveryCase{
		{
			name:       "the flag wins over the environment and the defaults",
			path:       "/flag/sol.yaml",
			env:        "/env/sol.yaml",
			candidates: []string{system, user},
			wantPath:   "/flag/sol.yaml",
			wantSource: SourceFlag,
		},
		{
			name:       "a flag path is reported even when it does not exist",
			path:       explicitMissing,
			env:        "/env/sol.yaml",
			candidates: []string{system},
			wantPath:   explicitMissing,
			wantSource: SourceFlag,
		},
		{
			name:       "the environment wins over the defaults",
			env:        "/env/sol.yaml",
			candidates: []string{system, user},
			wantPath:   "/env/sol.yaml",
			wantSource: SourceEnv,
		},
	})
}

func TestDiscoverProbesTheDefaultLocationsInOrder(t *testing.T) {
	system := writeConfigForDiscovery(t, t.TempDir())
	user := writeConfigForDiscovery(t, t.TempDir())
	missing := filepath.Join(t.TempDir(), "missing.yaml")

	runDiscoveryCases(t, []discoveryCase{
		{
			name:       "the system file is found before the user file",
			candidates: []string{system, user},
			wantPath:   system,
			wantSource: SourceSystem,
		},
		{
			name:       "the user file is used when the system one is absent",
			candidates: []string{missing, user},
			wantPath:   user,
			wantSource: SourceUser,
		},
		{
			name:       "no file at all falls back to the built-in defaults",
			candidates: []string{missing},
			wantSource: SourceNone,
		},
	})
}

func TestDiscoverFindsTheRealDefaults(t *testing.T) {
	paths := DefaultPaths()
	if len(paths) == 0 {
		t.Fatal("DefaultPaths() is empty")
	}

	if paths[0] != systemConfigPath {
		t.Fatalf("DefaultPaths()[0] = %q, want the system path %q", paths[0], systemConfigPath)
	}

	if len(paths) > 1 {
		want := filepath.Join(".config", "sol", "sol.yaml")
		if !strings.HasSuffix(paths[1], want) {
			t.Fatalf("DefaultPaths()[1] = %q, want a path ending in %q", paths[1], want)
		}
	}
}

// writeConfigForDiscovery creates a file standing in for a configuration file in a default
// location; discovery only stats it, so the contents do not matter.
func writeConfigForDiscovery(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "sol.yaml")
	if err := os.WriteFile(path, []byte("rules: []\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	return path
}

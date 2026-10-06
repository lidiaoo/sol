package install

import (
	"path/filepath"
	"runtime"
	"testing"
)

// Windows paths are compared through filepath.Join for the same reason the code builds them that
// way: the separator belongs to the platform, and these tests also run on Linux.
func TestDirFollowsThePlatform(t *testing.T) {
	tests := []struct {
		name        string
		goos        string
		programData string
		want        string
	}{
		{
			name: "linux keeps the ledger under /usr/local/share",
			goos: "linux",
			want: "/usr/local/share/sol",
		},
		{
			name: "darwin matches linux",
			goos: "darwin",
			want: "/usr/local/share/sol",
		},
		{
			name:        "windows uses ProgramData",
			goos:        "windows",
			programData: `D:\ProgramData`,
			want:        filepath.Join(`D:\ProgramData`, "sol"),
		},
		{
			name: "windows falls back when the environment is silent",
			goos: "windows",
			want: filepath.Join(`C:\ProgramData`, "sol"),
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := dirFor(testCase.goos, testCase.programData); got != testCase.want {
				t.Fatalf("dirFor(%q, %q) = %q, want %q", testCase.goos, testCase.programData, got, testCase.want)
			}
		})
	}
}

func TestConfigPathFollowsThePlatform(t *testing.T) {
	roaming := filepath.Join(`C:\Users\you\AppData\Roaming`, "sol", ConfigFile)

	tests := []struct {
		name    string
		goos    string
		appData string
		home    string
		want    string
	}{
		{
			name: "linux uses ~/.config",
			goos: "linux",
			home: "/home/you",
			want: "/home/you/.config/sol/install.yaml",
		},
		{
			name: "darwin uses ~/.config too, not Application Support",
			goos: "darwin",
			home: "/Users/you",
			want: "/Users/you/.config/sol/install.yaml",
		},
		{
			name:    "windows uses APPDATA",
			goos:    "windows",
			appData: `C:\Users\you\AppData\Roaming`,
			home:    `C:\Users\you`,
			want:    roaming,
		},
		{
			name: "windows without APPDATA falls back to the home directory",
			goos: "windows",
			home: `C:\Users\you`,
			want: filepath.Join(`C:\Users\you`, ".config", "sol", ConfigFile),
		},
		{
			name: "no home directory at all",
			goos: "linux",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := configPathFor(testCase.goos, testCase.appData, testCase.home)
			if got != testCase.want {
				t.Fatalf("configPathFor(%q, %q, %q) = %q, want %q",
					testCase.goos, testCase.appData, testCase.home, got, testCase.want)
			}
		})
	}
}

func TestPathsSitInTheInstallDirectory(t *testing.T) {
	dir := Dir()

	if got := LedgerPath(); got != filepath.Join(dir, LedgerFile) {
		t.Fatalf("LedgerPath() = %q, want it under %q", got, dir)
	}

	if got := HistoryPath(); got != filepath.Join(dir, HistoryFile) {
		t.Fatalf("HistoryPath() = %q, want it under %q", got, dir)
	}

	// On Windows the binary lives beside the ledger, so the install configuration is the only
	// path outside the install directory; on Unix both are.
	if runtime.GOOS == "windows" {
		if filepath.Dir(ConfigPath()) == dir {
			t.Fatalf("ConfigPath() = %q, want it outside the install directory %q", ConfigPath(), dir)
		}

		return
	}

	if filepath.Base(ConfigPath()) != ConfigFile {
		t.Fatalf("ConfigPath() = %q, want it to end in %q", ConfigPath(), ConfigFile)
	}
}

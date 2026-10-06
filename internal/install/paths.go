// Package install knows where an installed SoL keeps its pieces: the directory holding the
// binary (on Windows) and the ledger, the install configuration the user edits, and the history
// file. It is the single place the layout is written down, so the Go-side helpers (`sol paths`,
// `sol status`) and the install scripts cannot drift apart (install design §9).
package install

import (
	"os"
	"path/filepath"
	"runtime"
)

// Names of the files inside the install directory (install design §3 and §6.1).
const (
	// LedgerFile records what the installer did; it is what makes uninstall and upgrade safe
	// instead of a guess.
	LedgerFile = "install.json"
	// HistoryFile is the append-only install / upgrade / uninstall log.
	HistoryFile = "install.log"
	// ConfigFile is the install configuration the installer generates for the user to edit. It is
	// not the runtime configuration sol reads when it listens - confusing the two is exactly what
	// the distinct names are here to prevent.
	ConfigFile = "install.yaml"
)

// Environment variables and fallbacks the Windows layout hangs off.
const (
	envProgramData = "ProgramData"
	envAppData     = "APPDATA"
	// windowsProgramData is used when the environment does not name ProgramData.
	windowsProgramData = `C:\ProgramData`
	// goosWindows is the GOOS value that switches to the Windows layout.
	goosWindows = "windows"
)

// The Unix side of the layout.
const (
	unixInstallDir = "/usr/local/share/sol"
	unixConfigDir  = ".config"
	solDirName     = "sol"
)

// Dir is the install directory: the ledger and the history live here everywhere, and on Windows
// the binary does too.
func Dir() string {
	return dirFor(runtime.GOOS, os.Getenv(envProgramData))
}

// LedgerPath is the file recording what the installer created (§3).
func LedgerPath() string {
	return filepath.Join(Dir(), LedgerFile)
}

// HistoryPath is the append-only install history.
func HistoryPath() string {
	return filepath.Join(Dir(), HistoryFile)
}

// ConfigPath is the install configuration the user edits and re-runs the installer with:
// %APPDATA%\sol\install.yaml on Windows, ~/.config/sol/install.yaml elsewhere - macOS included,
// matching where sol itself looks for its runtime configuration.
func ConfigPath() string {
	return configPathFor(runtime.GOOS, os.Getenv(envAppData), homeDir())
}

// dirFor is Dir with the platform and the environment injected.
func dirFor(goos string, programData string) string {
	if goos != goosWindows {
		return unixInstallDir
	}

	if programData == "" {
		programData = windowsProgramData
	}

	return filepath.Join(programData, solDirName)
}

// configPathFor is ConfigPath with the platform and the environment injected.
func configPathFor(goos string, appData string, home string) string {
	if goos == goosWindows && appData != "" {
		return filepath.Join(appData, solDirName, ConfigFile)
	}

	if home == "" {
		return ""
	}

	return filepath.Join(home, unixConfigDir, solDirName, ConfigFile)
}

// homeDir is the user's home directory, either from the environment or from the OS.
func homeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return home
}

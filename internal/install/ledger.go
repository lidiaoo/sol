package install

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// LedgerSchema is the ledger layout this build understands; a file with a higher number was
// written by a newer sol and is refused rather than half-read.
const LedgerSchema = 1

// ErrLedgerSchema reports a ledger written by a newer sol: it is refused instead of half-read,
// because the fields this build does not know about are exactly the ones it would mis-report.
var ErrLedgerSchema = errors.New("install ledger uses a newer schema")

// ErrNoLedger reports that there is no ledger: the binary was not placed by the install script.
// It is a normal state, not a failure - `sol status` reports it as "not managed", and the
// installer treats it as "this is a binary somebody else put here".
var ErrNoLedger = errors.New("no install ledger")

// Ledger is the installer's record of what it created. It is the reason uninstall and upgrade can
// be safe rather than a guess: without it, nothing may be deleted (§3 of the install design).
type Ledger struct {
	Schema           int             `json:"schema"`
	InstalledVersion string          `json:"installed_version"`
	InstalledAt      string          `json:"installed_at"`
	Method           string          `json:"method"`
	Prefix           string          `json:"prefix"`
	Binary           string          `json:"binary"`
	SHA256           string          `json:"sha256"`
	Service          LedgerService   `json:"service"`
	FirewallRules    []string        `json:"firewall_rules"`
	ConfigPaths      []string        `json:"config_paths"`
	LogPaths         []string        `json:"log_paths"`
	Incomplete       bool            `json:"incomplete"`
	Previous         *LedgerPrevious `json:"previous,omitempty"`
}

// LedgerService is the service the installer set up, as it recorded it.
type LedgerService struct {
	Enabled bool `json:"enabled"`
	// Kind is the service manager: systemd, launchd or task (a scheduled task).
	Kind string `json:"kind"`
	// Path is the unit file, the plist, or the scheduled task name.
	Path string `json:"path"`
}

// LedgerPrevious is the binary this one replaced, so a rollback has something to go back to.
type LedgerPrevious struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// ReadLedger reads the ledger at path. A missing file returns ErrNoLedger, which callers report
// as "not managed" instead of treating it as damage.
func ReadLedger(path string) (*Ledger, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w at %s", ErrNoLedger, path)
	}

	if err != nil {
		return nil, fmt.Errorf("read ledger %s: %w", path, err)
	}

	var ledger Ledger

	if err := json.Unmarshal(raw, &ledger); err != nil {
		return nil, fmt.Errorf("decode ledger %s: %w", path, err)
	}

	if ledger.Schema > LedgerSchema {
		return nil, fmt.Errorf("%w: %s is schema %d and this sol understands %d: upgrade sol",
			ErrLedgerSchema, path, ledger.Schema, LedgerSchema)
	}

	return &ledger, nil
}

// FileSHA256 hashes a file, so a binary can be compared with what the ledger recorded. It is
// streamed: an installed binary is not read into memory to be identified.
func FileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}

	defer func() { _ = file.Close() }()

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}

	return hex.EncodeToString(digest.Sum(nil)), nil
}

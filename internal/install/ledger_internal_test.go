package install

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLedgerFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), LedgerFile)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	return path
}

func TestReadLedgerKeepsEveryFieldTheScriptsWrite(t *testing.T) {
	path := writeLedgerFile(t, `{
	  "schema": 1,
	  "installed_version": "0.3.0",
	  "installed_at": "2026-10-06T12:40:00Z",
	  "method": "script",
	  "prefix": "/usr/local/bin",
	  "binary": "/usr/local/bin/sol",
	  "sha256": "abc",
	  "service": {"enabled": true, "kind": "systemd", "path": "/etc/systemd/system/sol.service"},
	  "firewall_rules": ["ufw allow 10010/udp"],
	  "config_paths": ["/etc/sol/sol.yaml"],
	  "log_paths": ["/var/log/sol.log"],
	  "incomplete": false,
	  "previous": {"version": "0.2.1", "sha256": "def"}
	}`)

	ledger, err := ReadLedger(path)
	if err != nil {
		t.Fatalf("ReadLedger() = %v", err)
	}

	if ledger.Schema != LedgerSchema || ledger.InstalledVersion != "0.3.0" || ledger.Method != "script" {
		t.Fatalf("ledger = %+v, want the recorded install", ledger)
	}

	if !ledger.Service.Enabled || ledger.Service.Kind != "systemd" {
		t.Fatalf("service = %+v, want the recorded systemd unit", ledger.Service)
	}
}

func TestReadLedgerKeepsWhatUninstallNeedsToRemove(t *testing.T) {
	path := writeLedgerFile(t, `{
	  "schema": 1,
	  "firewall_rules": ["ufw allow 10010/udp"],
	  "config_paths": ["/etc/sol/sol.yaml"],
	  "log_paths": ["/var/log/sol.log"],
	  "previous": {"version": "0.2.1", "sha256": "def"}
	}`)

	ledger, err := ReadLedger(path)
	if err != nil {
		t.Fatalf("ReadLedger() = %v", err)
	}

	// Uninstall deletes only what the ledger lists, so every list has to survive the round trip.
	if len(ledger.ConfigPaths) != 1 || len(ledger.FirewallRules) != 1 || len(ledger.LogPaths) != 1 {
		t.Fatalf("ledger = %+v, want the config paths, firewall rules and log paths", ledger)
	}

	if ledger.Previous == nil || ledger.Previous.Version != "0.2.1" {
		t.Fatalf("previous = %+v, want the replaced binary recorded", ledger.Previous)
	}
}

func TestReadLedgerReportsTheServiceItRecorded(t *testing.T) {
	path := writeLedgerFile(t, `{
	  "schema": 1,
	  "method": "winget",
	  "service": {"enabled": true, "kind": "task", "path": "sol"},
	  "incomplete": true
	}`)

	ledger, err := ReadLedger(path)
	if err != nil {
		t.Fatalf("ReadLedger() = %v", err)
	}

	if ledger.Method != "winget" || ledger.Service.Kind != "task" || ledger.Service.Path != "sol" {
		t.Fatalf("ledger = %+v, want the recorded scheduled task", ledger)
	}

	if !ledger.Incomplete {
		t.Fatalf("ledger = %+v, want the unfinished install recorded", ledger)
	}
}

func TestReadLedgerTreatsAMissingFileAsNotManaged(t *testing.T) {
	_, err := ReadLedger(filepath.Join(t.TempDir(), LedgerFile))

	if !errors.Is(err, ErrNoLedger) {
		t.Fatalf("ReadLedger() = %v, want ErrNoLedger", err)
	}
}

func TestReadLedgerRefusesANewerSchema(t *testing.T) {
	path := writeLedgerFile(t, `{"schema": 99}`)

	_, err := ReadLedger(path)
	if err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("ReadLedger() = %v, want a refusal that names the schema", err)
	}
}

func TestReadLedgerRefusesBrokenJSON(t *testing.T) {
	if _, err := ReadLedger(writeLedgerFile(t, "{")); err == nil {
		t.Fatal("ReadLedger() accepted a malformed ledger")
	}
}

func TestFileSHA256IdentifiesContent(t *testing.T) {
	first := writeLedgerFile(t, "sol")

	sum, err := FileSHA256(first)
	if err != nil {
		t.Fatalf("FileSHA256() = %v", err)
	}

	if len(sum) != 64 {
		t.Fatalf("FileSHA256() = %q, want a hex sha256", sum)
	}

	if missing, err := FileSHA256(filepath.Join(t.TempDir(), "missing")); err == nil || missing != "" {
		t.Fatalf("FileSHA256() = %q, %v for a missing file, want an error", missing, err)
	}
}

// The ledger is the contract between the install scripts and this package: the field names are
// what the scripts write, so a rename here would silently orphan every installed machine.
func TestLedgerJSONFieldNamesStayStable(t *testing.T) {
	raw, err := json.Marshal(Ledger{})
	if err != nil {
		t.Fatalf("marshal ledger: %v", err)
	}

	for _, field := range []string{
		"schema", "installed_version", "installed_at", "method", "prefix", "binary", "sha256",
		"service", "firewall_rules", "config_paths", "log_paths", "incomplete",
	} {
		if !strings.Contains(string(raw), `"`+field+`"`) {
			t.Fatalf("ledger JSON is missing the %q field: %s", field, raw)
		}
	}
}

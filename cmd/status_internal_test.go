package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lidiaoo/sol/internal/config"
	"github.com/lidiaoo/sol/internal/install"
)

func TestPrivilegedPorts(t *testing.T) {
	tests := []struct {
		name  string
		ports []int
		want  []int
	}{
		{name: "high ports need nothing", ports: []int{10010, 10011, 65535}},
		{name: "the reserved Wake-on-LAN ports count as privileged", ports: []int{7, 9}, want: []int{7, 9}},
		{name: "1024 is the first unprivileged port", ports: []int{1023, 1024}, want: []int{1023}},
		{name: "no ports at all", ports: nil},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := privilegedPorts(testCase.ports)
			if len(got) != len(testCase.want) {
				t.Fatalf("privilegedPorts(%v) = %v, want %v", testCase.ports, got, testCase.want)
			}

			for index := range got {
				if got[index] != testCase.want[index] {
					t.Fatalf("privilegedPorts(%v) = %v, want %v", testCase.ports, got, testCase.want)
				}
			}
		})
	}
}

func TestServiceQueryDerivesTheCommandFromTheLedger(t *testing.T) {
	tests := []struct {
		name    string
		service install.LedgerService
		want    string
	}{
		{
			name:    "systemd",
			service: install.LedgerService{Enabled: true, Kind: "systemd", Path: "/etc/systemd/system/sol.service"},
			want:    "systemctl status sol.service",
		},
		{
			name:    "launchd",
			service: install.LedgerService{Enabled: true, Kind: "launchd", Path: "/Library/LaunchDaemons/com.lidiaoo.sol.plist"},
			want:    "launchctl print system/com.lidiaoo.sol",
		},
		{
			name:    "a scheduled task is named, not pathed",
			service: install.LedgerService{Enabled: true, Kind: "task", Path: "sol"},
			want:    "schtasks /Query /TN sol",
		},
		{
			name:    "an unknown kind has no query",
			service: install.LedgerService{Enabled: true, Kind: "supervisord", Path: "/etc/supervisord.d/sol.conf"},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := serviceQuery(testCase.service); got != testCase.want {
				t.Fatalf("serviceQuery(%+v) = %q, want %q", testCase.service, got, testCase.want)
			}
		})
	}
}

func TestDescribeServicePlan(t *testing.T) {
	tests := []struct {
		name    string
		service install.LedgerService
		want    string
	}{
		{name: "no service", service: install.LedgerService{}, want: "none (the ledger says no service was set up)"},
		{
			name:    "systemd names the unit",
			service: install.LedgerService{Enabled: true, Kind: "systemd", Path: "/etc/systemd/system/sol.service"},
			want:    "systemd, enabled (/etc/systemd/system/sol.service)",
		},
		{
			name:    "a task is called what it is",
			service: install.LedgerService{Enabled: true, Kind: "task", Path: "sol"},
			want:    "scheduled task, enabled (sol)",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := describeServicePlan(testCase.service); got != testCase.want {
				t.Fatalf("describeServicePlan() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestLogIssueOnlySpeaksAboutFacts(t *testing.T) {
	dir := t.TempDir()
	readOnly := filepath.Join(dir, "audit.log")

	if err := os.WriteFile(readOnly, nil, 0o400); err != nil {
		t.Fatalf("write %s: %v", readOnly, err)
	}

	tests := []struct {
		name     string
		settings config.Logging
		want     string
	}{
		{name: "another destination is not this function's business", settings: config.Logging{Output: "stderr"}},
		{name: "a file output without a path is the check's finding", settings: config.Logging{Output: "file"}},
		{
			name:     "a file in an existing directory will simply be created",
			settings: config.Logging{Output: "file", File: filepath.Join(dir, "new.log")},
		},
		{
			name:     "a missing directory is a fact",
			settings: config.Logging{Output: "file", File: filepath.Join(dir, "nope", "audit.log")},
			want:     "does not exist",
		},
		{
			name:     "a file without the write bit is a fact",
			settings: config.Logging{Output: "file", File: readOnly},
			want:     "not writable",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := logIssue(testCase.settings)

			if testCase.want == "" {
				if got != "" {
					t.Fatalf("logIssue(%+v) = %q, want nothing", testCase.settings, got)
				}

				return
			}

			if !strings.Contains(got, testCase.want) {
				t.Fatalf("logIssue(%+v) = %q, want it to mention %q", testCase.settings, got, testCase.want)
			}
		})
	}
}

func TestShortHash(t *testing.T) {
	if got := shortHash("abcdef0123456789"); got != "abcdef012345" {
		t.Fatalf("shortHash() = %q, want the first twelve characters", got)
	}

	if got := shortHash("abc"); got != "abc" {
		t.Fatalf("shortHash() = %q, want a short hash untouched", got)
	}
}

func TestStatusRenderingLines(t *testing.T) {
	unmanaged := statusReport{LedgerPath: "/usr/local/share/sol/install.json"}

	if got := statusInstallLine(unmanaged); !strings.Contains(got, "not managed") {
		t.Fatalf("statusInstallLine() = %q, want it to say the binary is unmanaged", got)
	}

	managed := statusReport{
		Managed:       true,
		BinaryMatches: true,
		Ledger: &install.Ledger{
			Schema:      1,
			Method:      "script",
			InstalledAt: "2026-10-06T12:40:00Z",
		},
	}

	if got := statusInstallLine(managed); !strings.Contains(got, "binary matches") {
		t.Fatalf("statusInstallLine() = %q, want it to say the binary matches", got)
	}

	managed.BinaryMatches = false

	if got := statusInstallLine(managed); !strings.Contains(got, "binary replaced") {
		t.Fatalf("statusInstallLine() = %q, want it to say the binary was replaced", got)
	}

	if got := statusConfigLine(statusReport{ConfigOK: true}); got != "would start" {
		t.Fatalf("statusConfigLine() = %q, want it to say the configuration would start", got)
	}

	if got := statusConfigLine(statusReport{}); got != "could not be read" {
		t.Fatalf("statusConfigLine() = %q, want it to say the file could not be read", got)
	}

	if got := statusConfigLine(statusReport{ConfigProblems: 2}); !strings.Contains(got, "2 problem(s)") {
		t.Fatalf("statusConfigLine() = %q, want the problem count", got)
	}

	if got := statusVersion(statusReport{Version: "0.3.0"}); got != "version 0.3.0" {
		t.Fatalf("statusVersion() = %q, want the version alone", got)
	}

	if got := statusVersion(statusReport{Version: "0.3.0", Revision: "abc1234"}); got != "version 0.3.0 (abc1234)" {
		t.Fatalf("statusVersion() = %q, want the revision named", got)
	}
}

func TestCollectStatusReportsTheConfigurationItWasGiven(t *testing.T) {
	good := writeConfigForCheck(t,
		"version: 1\nrules:\n  - match: { ports: [10010], content: { kind: none } }\n    action: noop\n")

	statusConfig = good

	t.Cleanup(func() { statusConfig = "" })

	report := collectStatus()

	if !report.ConfigOK || report.Config != good {
		t.Fatalf("report = %+v, want the given configuration to be reported as startable", report)
	}

	if len(report.Ports) != 1 || report.Ports[0] != 10010 {
		t.Fatalf("report = %+v, want the configured port", report)
	}

	if report.SHA256 == "" || len(report.SHA256) != 64 {
		t.Fatalf("report.SHA256 = %q, want the running binary's checksum", report.SHA256)
	}

	if len(report.PrivilegedPorts) != 0 {
		t.Fatalf("report = %+v, want no privileged ports for a high port", report)
	}

	// A configuration with a privileged port cannot be bound by an ordinary user, and saying so is
	// the point of the check.
	reserved := writeConfigForCheck(t,
		"version: 1\nrules:\n  - match: { ports: [9], content: { kind: none } }\n    action: noop\n")

	statusConfig = reserved

	report = collectStatus()

	if len(report.PrivilegedPorts) != 1 || report.PrivilegedPorts[0] != 9 {
		t.Fatalf("report = %+v, want port 9 reported as privileged", report)
	}
}

func TestWriteStatusTableAndJSON(t *testing.T) {
	report := statusReport{
		Version:      "0.3.0",
		Revision:     "abc1234",
		Binary:       "/usr/local/bin/sol",
		SHA256:       "abcdef0123456789",
		LedgerPath:   "/usr/local/share/sol/install.json",
		ServicePlan:  "unknown (no ledger)",
		StartedBy:    "systemd (INVOCATION_ID is set)",
		Config:       "/etc/sol/sol.yaml",
		ConfigSource: config.SourceSystem,
		ConfigOK:     true,
		Ports:        []int{10010},
		Log:          "stderr (default)",
		Problems:     []string{},
		Notes:        []string{"not managed: no ledger at /usr/local/share/sol/install.json"},
		Next:         "sol config check",
	}

	var table bytes.Buffer

	writeStatusTable(&table, report)

	for _, want := range []string{
		"version 0.3.0 (abc1234)", "/usr/local/bin/sol", "abcdef012345",
		"not managed (no ledger", "systemd (INVOCATION_ID is set)",
		"would start", "(source: system default)", "10010",
		"none - this sol would run as configured", "note", "sol config check",
	} {
		if !strings.Contains(table.String(), want) {
			t.Fatalf("table output is missing %q:\n%s", want, table.String())
		}
	}

	// With a problem the report says so, and the notes are still printed.
	report.Problems = []string{"the configuration would not start (1 problem(s)): run sol config check"}

	table.Reset()

	writeStatusTable(&table, report)

	if !strings.Contains(table.String(), "problems") || !strings.Contains(table.String(), "  -  ") {
		t.Fatalf("table output does not list the problem:\n%s", table.String())
	}
}

func TestWriteStatusJSON(t *testing.T) {
	report := statusReport{
		Version:      "0.3.0",
		Config:       "/etc/sol/sol.yaml",
		ConfigSource: config.SourceSystem,
		ConfigOK:     true,
		Problems:     []string{"the configuration would not start (1 problem(s)): run sol config check"},
		Notes:        []string{"not managed: no install ledger"},
	}

	var out bytes.Buffer

	if err := writeStatusJSON(&out, report); err != nil {
		t.Fatalf("writeStatusJSON() = %v", err)
	}

	var decoded struct {
		Managed  bool     `json:"managed"`
		ConfigOK bool     `json:"config_ok"`
		Problems []string `json:"problems"`
		Notes    []string `json:"notes"`
	}

	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal JSON output: %v\n%s", err, out.String())
	}

	if decoded.Managed || !decoded.ConfigOK || len(decoded.Problems) != 1 || len(decoded.Notes) != 1 {
		t.Fatalf("JSON output = %+v, want an unmanaged report with one problem and one note", decoded)
	}
}

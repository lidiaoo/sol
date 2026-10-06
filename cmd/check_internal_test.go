package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lidiaoo/sol/internal/config"
)

// errUnmapped stands in for a refusal nobody has advice for yet.
var errUnmapped = errors.New("something nobody mapped")

// writeConfigForCheck puts a configuration in a fresh temporary file, so each case is judged on
// its own content and never on the machine's real configuration.
func writeConfigForCheck(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sol.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	return path
}

// Two rules may share a port as long as their conditions tell the packets apart: only the same
// port with the same conditions is a duplicate.
func TestBuildCheckReportAcceptsAStartableConfiguration(t *testing.T) {
	path := writeConfigForCheck(t, "version: 1\nrules:\n"+
		"  - match: { ports: [10010], content: { kind: none } }\n    action: noop\n"+
		"  - match: { ports: [10010], content: { kind: suffix, value: reboot } }\n    action: power.reboot\n")

	report := buildCheckReport(path)

	if !report.OK || len(report.Problems) != 0 {
		t.Fatalf("report = %+v, want a configuration that would start", report)
	}

	if report.Summary.Rules != 2 || len(report.Summary.Ports) != 2 {
		t.Fatalf("summary = %+v, want two rules", report.Summary)
	}

	if !report.Loaded {
		t.Fatalf("report = %+v, want it to report that the configuration was read", report)
	}

	if report.ConfigSource != config.SourceFlag {
		t.Fatalf("config source = %q, want %q", report.ConfigSource, config.SourceFlag)
	}
}

func TestBuildCheckReportTurnsRefusalsIntoAdvice(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantHint string
	}{
		{
			name: "the same port with the same conditions in two rules",
			content: "version: 1\nrules:\n" +
				"  - match: { ports: [10010], content: { kind: none } }\n    action: noop\n" +
				"  - match: { ports: [10010], content: { kind: none } }\n    action: power.reboot\n",
			wantHint: "drop one",
		},
		{
			name:     "an action on a reserved port",
			content:  "version: 1\nrules:\n  - match: { ports: [9], content: { kind: none } }\n    action: power.shutdown\n",
			wantHint: "reserved",
		},
		{
			name:     "nothing to listen for",
			content:  "version: 1\n",
			wantHint: "nothing is configured to listen for",
		},
		{
			name:     "a file output without a path",
			content:  "version: 1\nlogging: { output: file }\nrules:\n  - match: { ports: [10012], content: { kind: none } }\n    action: noop\n",
			wantHint: "logging.output",
		},
		{
			name: "a malformed allowlist entry",
			content: "version: 1\nsecurity: { url_allowlist: [\"http://\"] }\n" +
				"rules:\n  - match: { ports: [10013], content: { kind: none } }\n    action: noop\n",
			wantHint: "url_allowlist",
		},
		{
			name: "an environment variable that is not set",
			content: "version: 1\nlogging: { output: file, file: \"${SOL_CHECK_UNSET}/audit.log\" }\n" +
				"rules:\n  - match: { ports: [10014], content: { kind: none } }\n    action: noop\n",
			wantHint: "environment variable",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			report := buildCheckReport(writeConfigForCheck(t, testCase.content))

			if report.OK || len(report.Problems) == 0 {
				t.Fatalf("report = %+v, want a refusal", report)
			}

			if !strings.Contains(report.Problems[0].Hint, testCase.wantHint) {
				t.Fatalf("hint = %q, want it to mention %q", report.Problems[0].Hint, testCase.wantHint)
			}
		})
	}
}

func TestBuildCheckReportExplainsAMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")

	report := buildCheckReport(missing)

	if report.OK || len(report.Problems) == 0 {
		t.Fatalf("report = %+v, want a refusal", report)
	}

	if !strings.Contains(report.Problems[0].Hint, "does not exist") {
		t.Fatalf("hint = %q, want it to explain the missing file", report.Problems[0].Hint)
	}

	if report.Loaded || report.Summary.Rules != 0 {
		t.Fatalf("report = %+v, want no summary for a file that could not be read", report)
	}
}

func TestHintForKeepsAnUnknownRefusalUnadorned(t *testing.T) {
	if got := hintFor(errUnmapped); got != "" {
		t.Fatalf("hintFor() = %q, want no hint", got)
	}
}

func TestCheckFailedErrorNamesTheCount(t *testing.T) {
	if got := (checkFailedError{Problems: 2}).Error(); !strings.Contains(got, "2") {
		t.Fatalf("Error() = %q, want it to name the count", got)
	}
}

func TestWriteCheckTableAndJSON(t *testing.T) {
	report := checkReport{
		Config:       "/etc/sol/sol.yaml",
		ConfigSource: config.SourceSystem,
		Loaded:       true,
		Problems: []checkFinding{
			{Problem: "duplicate port condition in rules", Hint: "drop one, or merge them"},
		},
		Summary: checkSummary{
			Rules:      2,
			Ports:      []int{10010, 10011},
			Interfaces: "auto (every eligible interface)",
			Log:        "stderr (default)",
		},
	}

	var table bytes.Buffer

	writeCheckTable(&table, report)

	for _, want := range []string{
		"/etc/sol/sol.yaml", "(source: system default)",
		"1 problem(s)", "duplicate port condition in rules", "fix: drop one, or merge them",
		"10010, 10011", "next", "sol config check",
	} {
		if !strings.Contains(table.String(), want) {
			t.Fatalf("table output is missing %q:\n%s", want, table.String())
		}
	}

	// A passing configuration says so, and points at the next command instead of the fix loop.
	report.Problems = []checkFinding{}
	report.OK = true

	table.Reset()

	writeCheckTable(&table, report)

	if !strings.Contains(table.String(), "this configuration would start") {
		t.Fatalf("table output does not say the configuration is fine:\n%s", table.String())
	}

	if !strings.Contains(table.String(), "sol listen") {
		t.Fatalf("table output does not point at sol listen:\n%s", table.String())
	}
}

func TestWriteCheckJSON(t *testing.T) {
	report := checkReport{
		Config:       "/etc/sol/sol.yaml",
		ConfigSource: config.SourceSystem,
		OK:           true,
		Loaded:       true,
		Problems:     []checkFinding{},
		Summary:      checkSummary{Rules: 2},
	}

	var out bytes.Buffer

	if err := writeCheckJSON(&out, report); err != nil {
		t.Fatalf("writeCheckJSON() = %v", err)
	}

	var decoded struct {
		OK       bool `json:"ok"`
		Problems []struct {
			Hint string `json:"hint"`
		} `json:"problems"`
		Summary struct {
			Rules int `json:"rules"`
		} `json:"summary"`
	}

	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal JSON output: %v\n%s", err, out.String())
	}

	if !decoded.OK || decoded.Summary.Rules != 2 {
		t.Fatalf("JSON output = %+v, want ok with two rules", decoded)
	}
}

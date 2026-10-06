package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lidiaoo/sol/internal/config"
	"github.com/lidiaoo/sol/internal/infra/logging"
)

func TestBuildPathsReportMarksTheCandidates(t *testing.T) {
	system := filepath.Join(t.TempDir(), "sol.yaml")
	user := filepath.Join(t.TempDir(), "sol.yaml")

	if err := os.WriteFile(system, []byte("rules: []\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", system, err)
	}

	report := buildPathsReport(
		"/usr/local/bin/sol",
		config.Location{Path: system, Source: config.SourceSystem},
		[]string{system, user},
		logging.OutputStderr+" (default)",
		"",
	)

	if report.Config != system || report.ConfigSource != config.SourceSystem {
		t.Fatalf("report = %+v, want the system file and its source", report)
	}

	if len(report.Candidates) != 2 {
		t.Fatalf("got %d candidates, want 2", len(report.Candidates))
	}

	if !report.Candidates[0].Exists || !report.Candidates[0].InUse {
		t.Fatalf("first candidate = %+v, want it present and in use", report.Candidates[0])
	}

	if report.Candidates[1].Exists || report.Candidates[1].InUse {
		t.Fatalf("second candidate = %+v, want it absent and unused", report.Candidates[1])
	}
}

func TestBuildPathsReportCarriesTheInstallPaths(t *testing.T) {
	// These are reported whether or not a previous install left anything behind: the point of
	// `sol paths` is to answer where the files are, not only where they already exist.
	report := buildPathsReport("", config.Location{Source: config.SourceNone}, nil, "stderr (default)", "")

	if report.Ledger == "" || report.History == "" || report.InstallConfig == "" {
		t.Fatalf("report = %+v, want the ledger, history and install configuration paths", report)
	}
}

func TestBuildPathsReportKeepsGoingWithoutAConfiguration(t *testing.T) {
	report := buildPathsReport("", config.Location{Source: config.SourceNone}, nil, "stderr (default)", "boom")

	if report.Config != "" {
		t.Fatalf("report.Config = %q, want empty", report.Config)
	}

	// A configuration that does not parse must not cost the operator the rest of the answer.
	if report.ConfigError != "boom" || report.InstallConfig == "" {
		t.Fatalf("report = %+v, want the parse error reported alongside the locations", report)
	}
}

func TestDescribeLog(t *testing.T) {
	tests := []struct {
		name     string
		settings config.Logging
		want     string
	}{
		{
			name: "the empty value is the default",
			want: "stderr (default)",
		},
		{
			name:     "stderr is named as configured",
			settings: config.Logging{Output: logging.OutputStderr},
			want:     "stderr (default)",
		},
		{
			name:     "stdout",
			settings: config.Logging{Output: logging.OutputStdout},
			want:     "stdout",
		},
		{
			name:     "a file output carries its path",
			settings: config.Logging{Output: logging.OutputFile, File: "/var/log/sol.log"},
			want:     "file: /var/log/sol.log",
		},
		{
			name:     "an unknown value is reported, not judged",
			settings: config.Logging{Output: "syslog"},
			want:     "syslog",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := describeLog(testCase.settings); got != testCase.want {
				t.Fatalf("describeLog(%+v) = %q, want %q", testCase.settings, got, testCase.want)
			}
		})
	}
}

func TestSourceLabelSpellsOutTheDiscoveryLevel(t *testing.T) {
	tests := []struct {
		source config.Source
		want   string
	}{
		{config.SourceFlag, "command line --config"},
		{config.SourceEnv, "environment $SOL_CONFIG"},
		{config.SourceSystem, "system default"},
		{config.SourceUser, "user default"},
		{config.SourceNone, "no file found"},
	}

	for _, testCase := range tests {
		if got := sourceLabel(testCase.source); got != testCase.want {
			t.Fatalf("sourceLabel(%q) = %q, want %q", testCase.source, got, testCase.want)
		}
	}
}

func TestWritePathsTableAndJSON(t *testing.T) {
	report := pathsReport{
		Binary:        "/usr/local/bin/sol",
		Config:        "/etc/sol/sol.yaml",
		ConfigSource:  config.SourceSystem,
		Log:           logging.OutputStderr + " (default)",
		Ledger:        "/usr/local/share/sol/install.json",
		InstallConfig: "/home/you/.config/sol/install.yaml",
		History:       "/usr/local/share/sol/install.log",
		Candidates: []pathsCandidate{
			{Path: "/etc/sol/sol.yaml", Exists: true, InUse: true},
			{Path: "/home/you/.config/sol/sol.yaml"},
		},
	}

	var table bytes.Buffer

	writePathsTable(&table, report)

	for _, want := range []string{
		"binary", "/usr/local/bin/sol",
		"runtime config", "/etc/sol/sol.yaml", "(source: system default)",
		"candidates", "/home/you/.config/sol/sol.yaml", "(not present)",
		"install config", "/home/you/.config/sol/install.yaml",
		"next", "sol config check",
	} {
		if !strings.Contains(table.String(), want) {
			t.Fatalf("table output is missing %q:\n%s", want, table.String())
		}
	}

	var out bytes.Buffer

	if err := writePathsJSON(&out, report); err != nil {
		t.Fatalf("writePathsJSON() = %v", err)
	}

	var decoded map[string]any

	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal JSON output: %v\n%s", err, out.String())
	}

	for _, key := range []string{"binary", "runtime_config", "runtime_config_source", "candidates", "ledger", "install_config"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("JSON output is missing %q: %s", key, out.String())
		}
	}

	if _, exists := decoded["runtime_config_error"]; exists {
		t.Fatalf("JSON output carries an empty error field: %s", out.String())
	}
}

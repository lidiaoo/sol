package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lidiaoo/sol/internal/config"
	"github.com/lidiaoo/sol/internal/infra/logging"
	"github.com/lidiaoo/sol/internal/install"
)

// pathsTablePadding is the minimum column spacing of the `sol paths` table.
const pathsTablePadding = ifaceTablePadding

var (
	pathsJSON   bool
	pathsConfig string
)

var pathsCmd = &cobra.Command{
	Use:   "paths",
	Short: "Show which files sol uses: binary, configuration, log and install ledger",
	Long: "Show the files sol works with: where the running binary is, which configuration file\n" +
		"would be read and which level of the discovery order picked it, every candidate location,\n" +
		"where audit lines go, and the installer's ledger. It reads only and changes nothing, and\n" +
		"it still reports the locations when the configuration cannot be parsed.",
	RunE: func(_ *cobra.Command, _ []string) error {
		report := collectPaths()

		if pathsJSON {
			return writePathsJSON(os.Stdout, report)
		}

		writePathsTable(os.Stdout, report)

		return nil
	},
}

func init() {
	rootCmd.AddCommand(pathsCmd)

	pathsCmd.Flags().StringVar(&pathsConfig, "config", "",
		"Configuration file to report on (default: $SOL_CONFIG, then /etc/sol/sol.yaml, "+
			"then ~/.config/sol/sol.yaml)")
	pathsCmd.Flags().BoolVar(&pathsJSON, "json", false, "Print the locations as JSON")
}

// pathsReport is the resolved set of locations, in the order an operator reads them.
type pathsReport struct {
	Binary        string           `json:"binary"`
	Config        string           `json:"runtime_config"`
	ConfigSource  config.Source    `json:"runtime_config_source"`
	ConfigError   string           `json:"runtime_config_error,omitempty"`
	Candidates    []pathsCandidate `json:"candidates"`
	Log           string           `json:"log_destination"`
	Ledger        string           `json:"ledger"`
	LedgerExists  bool             `json:"ledger_exists"`
	InstallConfig string           `json:"install_config"`
	History       string           `json:"history"`
}

// pathsCandidate is one probed default location.
type pathsCandidate struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	InUse  bool   `json:"in_use"`
}

// collectPaths gathers the report from the running process and the machine's layout. Nothing here
// is fatal: a process that cannot read its own executable path, or a configuration that does not
// parse, still gets its other locations reported.
func collectPaths() pathsReport {
	destination, configErr := logDestination(pathsConfig)

	return buildPathsReport(
		runningBinary(),
		config.Discover(pathsConfig),
		config.DefaultPaths(),
		destination,
		configErr,
	)
}

// runningBinary is the path of the running executable with symlinks resolved, so the answer is
// the file itself rather than the link an operator may have created.
func runningBinary() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}

	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return executable
	}

	return resolved
}

// logDestination describes where the audit log goes, and the reason when that cannot be told:
// reading the configuration is best effort here, because `sol paths` exists for the case where
// the configuration is not usable yet.
func logDestination(path string) (string, string) {
	cfg, err := config.Load(path)
	if err != nil {
		return describeLog(config.Logging{}), err.Error()
	}

	return describeLog(cfg.Logging), ""
}

// describeLog renders the logging section the way an operator reads it: the destination, and the
// file when there is one.
func describeLog(settings config.Logging) string {
	switch strings.ToLower(strings.TrimSpace(settings.Output)) {
	case "", logging.OutputStderr:
		return logging.OutputStderr + " (default)"
	case logging.OutputStdout:
		return logging.OutputStdout
	case logging.OutputFile:
		return logging.OutputFile + ": " + settings.File
	default:
		// An unknown value is not `sol paths`'s to judge; `sol config check` explains it.
		return settings.Output
	}
}

// buildPathsReport assembles the report from already resolved inputs, which keeps the interesting
// decisions - source naming, candidate marking, ledger probing - testable without a machine.
func buildPathsReport(binary string, location config.Location, candidates []string, log string, configErr string) pathsReport {
	report := pathsReport{
		Binary:        binary,
		Config:        location.Path,
		ConfigSource:  location.Source,
		ConfigError:   configErr,
		Log:           log,
		Ledger:        install.LedgerPath(),
		InstallConfig: install.ConfigPath(),
		History:       install.HistoryPath(),
		Candidates:    make([]pathsCandidate, 0, len(candidates)),
	}

	for _, candidate := range candidates {
		_, statErr := os.Stat(candidate)

		report.Candidates = append(report.Candidates, pathsCandidate{
			Path:   candidate,
			Exists: statErr == nil,
			InUse:  candidate == location.Path,
		})
	}

	if _, err := os.Stat(report.Ledger); err == nil {
		report.LedgerExists = true
	}

	return report
}

// writePathsTable prints the report as labelled lines: one location per line, the candidates
// indented under the runtime configuration they belong to.
func writePathsTable(out io.Writer, report pathsReport) {
	writer := tabwriter.NewWriter(out, 0, 0, pathsTablePadding, ' ', 0)

	fmt.Fprintf(writer, "binary\t%s\n", dash(report.Binary))
	fmt.Fprintf(writer, "runtime config\t%s\t(source: %s)\n", configLine(report), sourceLabel(report.ConfigSource))

	if report.ConfigError != "" {
		fmt.Fprintf(writer, "\tconfiguration not usable: %s\n", report.ConfigError)
	}

	fmt.Fprintln(writer, "candidates")

	for _, candidate := range report.Candidates {
		mark := " "

		if candidate.InUse {
			mark = "*"
		}

		fmt.Fprintf(writer, "  %s\t%s\t%s\n", mark, candidate.Path, presence(candidate.Exists))
	}

	fmt.Fprintf(writer, "log\t%s\n", report.Log)
	fmt.Fprintf(writer, "ledger\t%s\t%s\n", report.Ledger, presence(report.LedgerExists))
	fmt.Fprintf(writer, "install config\t%s\n", dash(report.InstallConfig))
	fmt.Fprintf(writer, "history\t%s\n", dash(report.History))
	fmt.Fprintf(writer, "next\t%s\n", nextStep(report))

	_ = writer.Flush()
}

// configLine is the runtime configuration path, or a marker when there is none.
func configLine(report pathsReport) string {
	if report.Config == "" {
		return "(none - the built-in defaults, which carry no rules)"
	}

	return report.Config
}

// nextStep names the single command worth running after reading the report. A missing
// configuration is not a command: there is nothing to check until rules exist somewhere.
func nextStep(report pathsReport) string {
	if report.Config == "" {
		return "write rules to one of the candidates above, then run: sol config check"
	}

	return "sol config check"
}

// presence renders whether a location exists.
func presence(exists bool) string {
	if exists {
		return "(present)"
	}

	return "(not present)"
}

// sourceLabel spells out the discovery level that chose the configuration path.
func sourceLabel(source config.Source) string {
	switch source {
	case config.SourceFlag:
		return "command line --config"
	case config.SourceEnv:
		return "environment $SOL_CONFIG"
	case config.SourceSystem:
		return "system default"
	case config.SourceUser:
		return "user default"
	case config.SourceNone:
		return "no file found"
	default:
		// A source this command does not know about is still worth printing as it is.
		return string(source)
	}
}

// writePathsJSON prints the report as JSON, for scripts and for reporting bugs.
func writePathsJSON(out io.Writer, report pathsReport) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)

	return encoder.Encode(report)
}

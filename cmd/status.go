package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lidiaoo/sol/internal/buildinfo"
	"github.com/lidiaoo/sol/internal/config"
	"github.com/lidiaoo/sol/internal/infra/logging"
	"github.com/lidiaoo/sol/internal/infra/system"
	"github.com/lidiaoo/sol/internal/install"
)

// statusTablePadding is the minimum column spacing of the `sol status` table.
const statusTablePadding = ifaceTablePadding

// firstUnprivilegedPort is the boundary of the privileged range on Unix. Ports 7 and 9, the
// reserved Wake-on-LAN ports, are below it too, so one comparison covers both.
const firstUnprivilegedPort = 1024

// modeWriteBit is the permission bit that says an owner may write a file.
const modeWriteBit = 0o200

// serviceKindSystemd, serviceKindLaunchd and serviceKindTask are the service managers the install
// ledger records.
const (
	serviceKindSystemd  = "systemd"
	serviceKindLaunchd  = "launchd"
	serviceKindTask     = "task"
	servicePlistSuffix  = ".plist"
	serviceLedgerSchema = "ledger schema "
)

var (
	statusJSON   bool
	statusConfig string
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report what this sol is, which files it uses, and whether it would work",
	Long: "Report the running binary and its checksum, whether the install script placed it (the\n" +
		"ledger), what the service setup is, whether this process was started by a service manager,\n" +
		"whether the configuration would start, which ports it listens on, and whether this process\n" +
		"may bind them. Facts that do not stop the service are printed as notes; anything that would\n" +
		"stop it is a problem, and the exit code is 1 when there is one.",
	SilenceUsage: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runStatus(os.Stdout)
	},
}

// statusFailedError reports how many problems the report carries, in the same shape as
// checkFailedError, so the process status is the command's decision and not an exit call inside it.
type statusFailedError struct {
	Problems int
}

func (e statusFailedError) Error() string {
	return strconv.Itoa(e.Problems) + " problem(s) found, see the problems above"
}

// statusReport is the machine-readable form of `sol status`. Problems would stop the service;
// notes are facts worth knowing that do not.
type statusReport struct {
	Version       string `json:"version"`
	Revision      string `json:"revision"`
	Binary        string `json:"binary"`
	SHA256        string `json:"sha256"`
	Managed       bool   `json:"managed"`
	BinaryMatches bool   `json:"binary_matches_ledger"`
	// LedgerPath is where a ledger would live; Ledger is its content, when there is one. They are
	// named apart on purpose: a script that read the path where it expected the record would
	// otherwise have to work out why its field is a string.
	LedgerPath        string          `json:"ledger_path"`
	Ledger            *install.Ledger `json:"ledger,omitempty"`
	ServicePlan       string          `json:"service_plan"`
	StartedBy         string          `json:"started_by"`
	Config            string          `json:"runtime_config"`
	ConfigSource      config.Source   `json:"runtime_config_source"`
	ConfigOK          bool            `json:"config_ok"`
	ConfigProblems    int             `json:"config_problems"`
	Ports             []int           `json:"ports"`
	PrivilegedPorts   []int           `json:"privileged_ports"`
	CanBindPrivileged bool            `json:"can_bind_privileged_ports"`
	Log               string          `json:"log_destination"`
	Problems          []string        `json:"problems"`
	Notes             []string        `json:"notes"`
	Next              string          `json:"next"`
}

// serviceMarker is an environment variable a service manager leaves on the process it started.
type serviceMarker struct {
	env  string
	name string
}

// serviceMarkers are the markers that prove a process was started by a service manager: systemd
// sets INVOCATION_ID (and JOURNAL_STREAM), launchd sets XPC_SERVICE_NAME. A Windows scheduled task
// sets nothing, so on Windows the answer is honestly "no marker".
var serviceMarkers = []serviceMarker{
	{env: "INVOCATION_ID", name: serviceKindSystemd},
	{env: "JOURNAL_STREAM", name: serviceKindSystemd},
	{env: "XPC_SERVICE_NAME", name: serviceKindLaunchd},
}

func init() {
	rootCmd.AddCommand(statusCmd)

	statusCmd.Flags().StringVar(&statusConfig, "config", "",
		"Configuration file to report on (default: $SOL_CONFIG, then /etc/sol/sol.yaml, "+
			"then ~/.config/sol/sol.yaml)")
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Print the report as JSON")
}

// runStatus prints the report and turns problems into a non-zero exit.
func runStatus(out io.Writer) error {
	report := collectStatus()

	if statusJSON {
		if err := writeStatusJSON(out, report); err != nil {
			return err
		}
	} else {
		writeStatusTable(out, report)
	}

	if len(report.Problems) > 0 {
		return statusFailedError{Problems: len(report.Problems)}
	}

	return nil
}

// collectStatus gathers the report from this machine only. It never shells out to systemctl,
// launchctl or schtasks: what sol cannot prove locally it reports as a command for the user to run,
// which also keeps the binary from being tied to one init system.
func collectStatus() statusReport {
	report := statusReport{
		Version:    buildinfo.Version(),
		Revision:   buildinfo.Revision(),
		Binary:     runningBinary(),
		StartedBy:  startedBy(),
		LedgerPath: install.LedgerPath(),
		Problems:   []string{},
		Notes:      []string{},
	}

	hashRunningBinary(&report)
	collectConfigStatus(&report)
	collectPermissionStatus(&report)
	collectInstallStatus(&report)
	report.Next = nextStatusCommand(report)

	return report
}

// hashRunningBinary identifies the binary that is running, which is what a ledger comparison and a
// bug report both need.
func hashRunningBinary(report *statusReport) {
	if report.Binary == "" {
		report.Notes = append(report.Notes, "the path of the running binary could not be read")

		return
	}

	sum, err := install.FileSHA256(report.Binary)
	if err != nil {
		report.Notes = append(report.Notes, "the running binary could not be hashed: "+err.Error())

		return
	}

	report.SHA256 = sum
}

// collectConfigStatus judges the configuration with the listener's own checks, and adds the one
// detail a warning needs that the check report does not carry: where the audit log goes.
func collectConfigStatus(report *statusReport) {
	check := buildCheckReport(statusConfig)

	report.Config = check.Config
	report.ConfigSource = check.ConfigSource
	report.ConfigOK = check.OK
	report.ConfigProblems = len(check.Problems)
	report.Ports = check.Summary.Ports
	report.Log = check.Summary.Log

	switch {
	case !check.Loaded:
		report.Problems = append(report.Problems,
			"the configuration could not be read: run sol config check")
	case !check.OK:
		report.Problems = append(report.Problems, fmt.Sprintf(
			"the configuration would not start (%d problem(s)): run sol config check", len(check.Problems)))
	}

	if cfg, err := config.Load(statusConfig); err == nil {
		if issue := logIssue(cfg.Logging); issue != "" {
			report.Problems = append(report.Problems, issue)
		}
	}
}

// collectPermissionStatus reports the one privilege question that decides whether the configured
// ports can be bound at all.
func collectPermissionStatus(report *statusReport) {
	report.PrivilegedPorts = privilegedPorts(report.Ports)
	report.CanBindPrivileged = system.CanBindPrivilegedPorts()

	if len(report.PrivilegedPorts) == 0 || report.CanBindPrivileged {
		return
	}

	report.Problems = append(report.Problems, fmt.Sprintf(
		"ports %s need root or CAP_NET_BIND_SERVICE and this process has neither: run sol as root, "+
			"grant the capability (for a systemd unit: AmbientCapabilities=CAP_NET_BIND_SERVICE), "+
			"or move those rules to ports 1024 and above",
		listSummary(report.PrivilegedPorts)))
}

// collectInstallStatus answers whether the install script placed this binary, and whether the
// binary is still the one it placed.
func collectInstallStatus(report *statusReport) {
	ledger, err := install.ReadLedger(install.LedgerPath())

	switch {
	case errors.Is(err, install.ErrNoLedger):
		report.Notes = append(report.Notes, fmt.Sprintf(
			"%v: the install script did not place this binary, so uninstall and upgrade have "+
				"nothing to read", err))
		report.ServicePlan = "unknown (no ledger)"

		return
	case err != nil:
		report.Problems = append(report.Problems, "the install ledger cannot be read: "+err.Error())
		report.ServicePlan = "unknown (unreadable ledger)"

		return
	}

	report.Managed = true
	report.Ledger = ledger
	report.ServicePlan = describeServicePlan(ledger.Service)
	report.BinaryMatches = compareWithLedger(report, ledger)

	if ledger.Incomplete {
		report.Notes = append(report.Notes,
			"the ledger records an install that did not finish: re-run the install script")
	}
}

// compareWithLedger checks the running binary against what the ledger recorded, which is how a
// binary replaced behind sol's back becomes visible.
func compareWithLedger(report *statusReport, ledger *install.Ledger) bool {
	if ledger.SHA256 == "" {
		report.Notes = append(report.Notes, "the ledger records no checksum, so the binary cannot be compared")

		return true
	}

	if ledger.SHA256 == report.SHA256 {
		return true
	}

	report.Notes = append(report.Notes, fmt.Sprintf(
		"the binary does not match the ledger: something replaced %s (ledger %s, on disk %s)",
		ledger.Binary, shortHash(ledger.SHA256), shortHash(report.SHA256)))

	if ledger.Previous != nil {
		report.Notes = append(report.Notes, fmt.Sprintf(
			"the ledger keeps a previous binary: %s (%s), so a rollback is possible",
			ledger.Previous.Version, shortHash(ledger.Previous.SHA256)))
	}

	return false
}

// describeServicePlan renders the service the ledger recorded, in the words of the platform
// command that would show it.
func describeServicePlan(service install.LedgerService) string {
	if !service.Enabled {
		return "none (the ledger says no service was set up)"
	}

	switch service.Kind {
	case serviceKindSystemd:
		return fmt.Sprintf("%s, enabled (%s)", serviceKindSystemd, service.Path)
	case serviceKindLaunchd:
		return fmt.Sprintf("%s, enabled (%s)", serviceKindLaunchd, service.Path)
	case serviceKindTask:
		return fmt.Sprintf("scheduled task, enabled (%s)", service.Path)
	default:
		return fmt.Sprintf("%s, enabled (%s)", service.Kind, service.Path)
	}
}

// startedBy names the service manager that started this process, if the environment carries its
// marker.
func startedBy() string {
	for _, marker := range serviceMarkers {
		if strings.TrimSpace(os.Getenv(marker.env)) != "" {
			return marker.name + " (" + marker.env + " is set)"
		}
	}

	return "no service marker (an interactive shell, or a manager that leaves none)"
}

// privilegedPorts picks the ports that need privilege on this platform.
func privilegedPorts(ports []int) []int {
	privileged := []int{}

	for _, port := range ports {
		if port < firstUnprivilegedPort {
			privileged = append(privileged, port)
		}
	}

	return privileged
}

// logIssue reports a log destination that provably cannot work, without guessing about permissions
// it cannot see: a missing directory is a fact, and a file without the write bit is one too.
func logIssue(settings config.Logging) string {
	if strings.ToLower(strings.TrimSpace(settings.Output)) != logging.OutputFile {
		return ""
	}

	// A missing logging.file is already a start-up refusal that `sol config check` reports.
	if strings.TrimSpace(settings.File) == "" {
		return ""
	}

	info, err := os.Stat(settings.File)

	switch {
	case errors.Is(err, fs.ErrNotExist):
		dir := filepath.Dir(settings.File)
		if _, dirErr := os.Stat(dir); errors.Is(dirErr, fs.ErrNotExist) {
			return fmt.Sprintf("logging.file names %s but its directory %s does not exist: create the "+
				"directory, or fix logging.file", settings.File, dir)
		}

		// The directory exists: the file is created at start-up, which is normal.
		return ""
	case err != nil:
		return "logging.file cannot be inspected: " + err.Error()
	}

	if info.Mode().Perm()&modeWriteBit == 0 {
		return fmt.Sprintf("logging.file %s is not writable (mode %s): fix the mode, or the audit log "+
			"will be lost", settings.File, info.Mode().Perm())
	}

	return ""
}

// shortHash is the first twelve characters of a checksum: enough to compare two hashes by eye,
// short enough to read in a table.
func shortHash(sum string) string {
	const shortHashLength = 12

	if len(sum) <= shortHashLength {
		return sum
	}

	return sum[:shortHashLength]
}

// nextStatusCommand is the single command worth running next: the platform's own service query when
// the ledger says a service was set up, and the configuration check otherwise.
func nextStatusCommand(report statusReport) string {
	if report.Ledger != nil && report.Ledger.Service.Enabled {
		if command := serviceQuery(report.Ledger.Service); command != "" {
			return command
		}
	}

	if !report.ConfigOK {
		return "sol config check"
	}

	return "sol listen          (start listening)"
}

// serviceQuery turns the recorded service into the query the user would type, deriving the unit or
// task name from the path the ledger recorded.
func serviceQuery(service install.LedgerService) string {
	name := filepath.Base(service.Path)

	switch service.Kind {
	case serviceKindSystemd:
		// systemctl wants the unit name with its suffix: sol.service, not sol.
		return "systemctl status " + name
	case serviceKindLaunchd:
		// launchctl wants the label, which is the plist file name without its extension.
		return "launchctl print system/" + strings.TrimSuffix(name, servicePlistSuffix)
	case serviceKindTask:
		// A scheduled task is addressed by name, which is exactly what the ledger recorded.
		return "schtasks /Query /TN " + service.Path
	default:
		return ""
	}
}

// writeStatusTable prints the report as labelled lines: the identity, then the configuration, then
// anything wrong with it, then the notes.
func writeStatusTable(out io.Writer, report statusReport) {
	writer := tabwriter.NewWriter(out, 0, 0, statusTablePadding, ' ', 0)

	fmt.Fprintf(writer, "sol\t%s\n", statusVersion(report))
	fmt.Fprintf(writer, "binary\t%s\n", dash(report.Binary))
	fmt.Fprintf(writer, "sha256\t%s\n", dash(shortHash(report.SHA256)))
	fmt.Fprintf(writer, "installed by\t%s\n", statusInstallLine(report))
	fmt.Fprintf(writer, "service\t%s\n", report.ServicePlan)
	fmt.Fprintf(writer, "started by\t%s\n", report.StartedBy)
	fmt.Fprintf(writer, "configuration\t%s\t(source: %s) - %s\n",
		checkConfigLine(report.Config), sourceLabel(report.ConfigSource), statusConfigLine(report))
	fmt.Fprintf(writer, "ports\t%s\n", listSummary(report.Ports))
	fmt.Fprintf(writer, "privileged ports\t%s\n", statusPrivilegedLine(report))
	fmt.Fprintf(writer, "log\t%s\n", dash(report.Log))

	if len(report.Problems) > 0 {
		fmt.Fprintf(writer, "problems\t%d\n", len(report.Problems))

		for _, problem := range report.Problems {
			fmt.Fprintf(writer, "  -\t%s\n", problem)
		}
	} else {
		fmt.Fprintln(writer, "problems\tnone - this sol would run as configured")
	}

	for _, note := range report.Notes {
		fmt.Fprintf(writer, "note\t%s\n", note)
	}

	fmt.Fprintf(writer, "next\t%s\n", report.Next)

	_ = writer.Flush()
}

// statusVersion renders the build identity, naming the commit when the build carries one.
func statusVersion(report statusReport) string {
	if report.Revision == "" {
		return "version " + report.Version
	}

	return "version " + report.Version + " (" + report.Revision + ")"
}

// statusInstallLine says whether the install script placed this binary.
func statusInstallLine(report statusReport) string {
	if !report.Managed {
		return "not managed (no ledger at " + report.LedgerPath + ")"
	}

	line := fmt.Sprintf("%s, installed %s (%s%s)", report.Ledger.Method, report.Ledger.InstalledAt,
		serviceLedgerSchema, strconv.Itoa(report.Ledger.Schema))

	if !report.BinaryMatches {
		return line + " - binary replaced"
	}

	return line + " - binary matches"
}

// statusConfigLine says whether the configuration would start.
func statusConfigLine(report statusReport) string {
	switch {
	case !report.ConfigOK && report.ConfigProblems == 0:
		return "could not be read"
	case !report.ConfigOK:
		return fmt.Sprintf("would not start (%d problem(s))", report.ConfigProblems)
	default:
		return "would start"
	}
}

// statusPrivilegedLine reports whether this process may bind the privileged ports it configured.
func statusPrivilegedLine(report statusReport) string {
	if len(report.PrivilegedPorts) == 0 {
		return "none"
	}

	rights := "this process may bind them"

	if !report.CanBindPrivileged {
		rights = "this process may NOT bind them"
	}

	return listSummary(report.PrivilegedPorts) + " (" + rights + ")"
}

// writeStatusJSON prints the report as JSON, which is what the installer's verification and the
// Hermes skill read.
func writeStatusJSON(out io.Writer, report statusReport) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)

	return encoder.Encode(report)
}

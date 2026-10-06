package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lidiaoo/sol/internal/config"
	"github.com/lidiaoo/sol/internal/deps"
	"github.com/lidiaoo/sol/internal/domain/wol"
	"github.com/lidiaoo/sol/internal/infra/exec"
	"github.com/lidiaoo/sol/internal/infra/network"
	"github.com/lidiaoo/sol/internal/infra/outbound"
	"github.com/lidiaoo/sol/internal/infra/sequence"
)

// checkTablePadding is the minimum column spacing of the `sol config check` table.
const checkTablePadding = ifaceTablePadding

var (
	checkJSON   bool
	checkConfig string
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Work with the configuration file sol reads",
	Long:  "Inspect the configuration sol would read at start-up, without starting anything.",
}

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Check a configuration without listening, and say how to fix what it finds",
	Long: "Load the configuration and run every start-up check on it: the rule set (reserved ports,\n" +
		"duplicates, ambiguity), the secrets and environment variables, the logging destination,\n" +
		"the allowlist, the action parameters and the control plane. Nothing listens, no privilege\n" +
		"is needed, and every problem comes with the change that fixes it.\n" +
		"Exit code is 0 when the configuration would start, 1 otherwise.",
	SilenceUsage: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runConfigCheck(os.Stdout)
	},
}

// checkFailedError reports how many findings the check produced. Returning it instead of exiting
// here keeps the command testable and lets Execute own the process status.
type checkFailedError struct {
	Problems int
}

func (e checkFailedError) Error() string {
	return strconv.Itoa(e.Problems) + " configuration problem(s) found"
}

// checkReport is the machine-readable result of `sol config check`.
type checkReport struct {
	Config       string         `json:"config"`
	ConfigSource config.Source  `json:"config_source"`
	OK           bool           `json:"ok"`
	Loaded       bool           `json:"loaded"`
	Problems     []checkFinding `json:"problems"`
	Summary      checkSummary   `json:"summary"`
}

// checkFinding is one refusal and the change that fixes it.
type checkFinding struct {
	Problem string `json:"problem"`
	Hint    string `json:"hint,omitempty"`
}

// checkSummary is what the configuration resolves to, in the terms an operator asked about.
type checkSummary struct {
	Rules       int    `json:"rules"`
	Actions     int    `json:"actions"`
	Ports       []int  `json:"ports"`
	RemotePorts []int  `json:"remote_ports"`
	Interfaces  string `json:"interfaces"`
	Log         string `json:"log"`
	DryRun      bool   `json:"dry_run"`
}

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(checkCmd)

	checkCmd.Flags().StringVar(&checkConfig, "config", "",
		"Configuration file to check (default: $SOL_CONFIG, then /etc/sol/sol.yaml, "+
			"then ~/.config/sol/sol.yaml)")
	checkCmd.Flags().BoolVar(&checkJSON, "json", false, "Print the result as JSON")
}

// runConfigCheck prints the report and turns findings into a non-zero exit, which is what a
// pre-upgrade check or a CI job keys off.
func runConfigCheck(out io.Writer) error {
	report := buildCheckReport(checkConfig)

	if checkJSON {
		if err := writeCheckJSON(out, report); err != nil {
			return err
		}
	} else {
		writeCheckTable(out, report)
	}

	if !report.OK {
		return checkFailedError{Problems: len(report.Problems)}
	}

	return nil
}

// buildCheckReport judges the configuration with the code that would start the service, not with
// a second copy of the checks: config.Load reads and resolves the file, Builder.Validate runs the
// interface, action and rule checks, and the control plane adds its own auth and TLS checks.
func buildCheckReport(path string) checkReport {
	location := config.Discover(path)
	report := checkReport{
		Config:       location.Path,
		ConfigSource: location.Source,
		Problems:     []checkFinding{},
	}

	cfg, err := config.Load(path)
	if err != nil {
		report.Problems = append(report.Problems, newFinding(err))

		return report
	}

	report.Loaded = true
	report.Summary = summariseConfig(cfg)

	// The same rule the listener applies: a configuration with nothing to listen for is a
	// start-up refusal, not an empty success.
	if len(cfg.Rules) == 0 && len(cfg.Remote.Ports) == 0 {
		report.Problems = append(report.Problems, newFinding(errNoRules))
	}

	builder := deps.NewBuilder(cfg)

	if err := builder.Validate(); err != nil {
		report.Problems = append(report.Problems, newFinding(err))
	}

	// Building the control plane composes only - nothing listens until Run - so its checks run
	// here without a free port or any privilege.
	if _, err := builder.BuildHTTPServer(); err != nil {
		report.Problems = append(report.Problems, newFinding(err))
	}

	report.OK = len(report.Problems) == 0

	return report
}

// summariseConfig describes what the configuration would do, so a passing check still answers
// "what did I just validate".
func summariseConfig(cfg *config.Config) checkSummary {
	ports := []int{}

	for _, rule := range cfg.Rules {
		ports = append(ports, rule.Match.Ports...)
	}

	return checkSummary{
		Rules:       len(cfg.Rules),
		Actions:     len(cfg.Actions),
		Ports:       ports,
		RemotePorts: cfg.Remote.Ports,
		Interfaces:  interfaceSummary(cfg.InterfaceNames),
		Log:         describeLog(cfg.Logging),
		DryRun:      cfg.DryRun,
	}
}

// interfaceSummary names the interface selection the way the configuration expresses it.
func interfaceSummary(names []string) string {
	if len(names) == 0 {
		return "auto (every eligible interface)"
	}

	return strings.Join(names, ", ")
}

// newFinding pairs a refusal with its fix.
func newFinding(err error) checkFinding {
	return checkFinding{Problem: err.Error(), Hint: hintFor(err)}
}

// hintFor looks the refusal up in the hint table. An error with no entry keeps its own message,
// which names the offending field, so the output is never empty of advice by accident.
func hintFor(err error) string {
	for _, entry := range startupHints {
		if errors.Is(err, entry.err) {
			return entry.hint
		}
	}

	return ""
}

// startupHints pairs a start-up refusal with the change that clears it. The keys are the sentinels
// the packages already raise, so the advice cannot drift away from the check that produced it.
var startupHints = []struct {
	err  error
	hint string
}{
	// The rule set (§19).
	{
		wol.ErrDuplicatePort,
		"the same port belongs to two rules: drop one, or merge them into a single rule",
	},
	{
		wol.ErrAmbiguousRule,
		"two rules are equally specific for the same packet: make one more specific (content, auth, mac or src_cidrs)",
	},
	{
		wol.ErrRuleConflict,
		"two rule scopes overlap with matching conditions: delete one, or narrow one of them",
	},
	{
		wol.ErrReservedPortAction,
		"ports 7 and 9 are reserved for plain Wake-on-LAN and accept only the noop action: move the action " +
			"to a high port, or pass --allow-reserved-actions to keep the old behaviour",
	},
	{
		config.ErrActionRequired,
		"every rule needs an action: add the action key (for example action: noop)",
	},
	{
		config.ErrSequenceStepsRequired,
		"a sequence action needs steps: name at least one action under steps",
	},
	{sequence.ErrStepsRequired, "a sequence action needs steps: name at least one action under steps"},
	{sequence.ErrUnknownStep, "a sequence step names an action that does not exist: check the spelling"},
	{sequence.ErrNestedSequence, "a sequence may not contain another sequence: flatten the steps"},
	// Secrets and environment (§18).
	{
		config.ErrMissingEnvVar,
		"the configuration references an environment variable that is not set: export it for the service " +
			"(systemd: Environment=), or keep the value in a 600-mode file",
	},
	{
		config.ErrSecret,
		"the secret cannot be read: point it at an environment variable or a file the service can read, " +
			"and never inline it in the YAML",
	},
	{
		config.ErrPacketAuth,
		"the packet authorization setting is invalid: check security.packet_key and security.packet_window",
	},
	// Logging (§18).
	{
		config.ErrLogOutput,
		"logging.output is stderr, stdout or file: any other value is refused, and output: file needs logging.file",
	},
	// Actions.
	{config.ErrExecCommandRequired, "an exec action needs command: give the absolute path to run"},
	{
		config.ErrActionParams,
		"the action parameters do not match its type: check the keys for this action type in docs/routing-design.md",
	},
	{
		config.ErrUnknownActionType,
		"unknown action type: use noop, power.shutdown, power.reboot, power.sleep, exec, http, sequence, " +
			"wol.send or remote:<id>",
	},
	{
		config.ErrHTTPURLRequired,
		"an http action needs url: give the full http(s) address to call",
	},
	{
		exec.ErrCommandNotAllowed,
		"the command is outside security.exec_allowlist: add its directory to the allowlist, or point the " +
			"action at a program inside it",
	},
	{exec.ErrCommandNotFound, "the command does not exist on this machine: check the path"},
	{
		exec.ErrNotRoot,
		"an exec action with user/group must run as root: run sol as root, or drop the user/group keys",
	},
	{exec.ErrUserUnsupported, "user/group only exist on Unix: drop them on this platform"},
	{
		outbound.ErrAllowlistEntry,
		"a security.url_allowlist entry is malformed: entries look like example.com or https://example.com/path",
	},
	{
		outbound.ErrURLNotAllowed,
		"the http action url is not covered by security.url_allowlist: add the host, or change the url",
	},
	{outbound.ErrProxy, "an action proxy must be http, https, socks5 or socks5h with host:port"},
	// Interfaces (§17).
	{
		network.ErrNoEligibleInterface,
		"no interface qualifies for auto mode: name one with server.interfaces, or bring a NIC up",
	},
	{
		network.ErrNoMACAddress,
		"the named interface has no MAC address, so mac: interface cannot match on it: use an explicit MAC " +
			"or another NIC",
	},
	{
		config.ErrInterfaceBlock,
		"a server.interfaces entry is malformed: each one is either a name or a block with name and rules",
	},
	// The remote command channel (§21).
	{
		config.ErrRemoteAuth,
		"the remote command channel needs authorization: set security.remote_command_key from the environment " +
			"or a file, so commands can be authenticated",
	},
	{config.ErrRemotePort, "a remote command port cannot be a reserved port (7/9): pick a high port"},
	{
		config.ErrRawShell,
		"security.allow_raw_shell is misconfigured: check its timeout, and its user/group settings",
	},
	// The control plane (§18.1).
	{config.ErrHTTPListen, "server.http.listen is not a valid host:port: for example 127.0.0.1:8081"},
	{config.ErrHTTPAuthType, "server.http.auth.type must be bearer, basic or mtls"},
	{config.ErrHTTPUser, "basic auth needs server.http.auth.user"},
	{
		config.ErrHTTPTLS,
		"the TLS settings are incomplete: cert_file and key_file must both point at readable files",
	},
	// The file itself.
	{config.ErrUnsupportedVersion, "this build does not know that configuration version: set version: 1"},
	{config.ErrRulesConflict, "rules are defined twice (top-level and under server): keep only one of them"},
	{config.ErrDuplicateAction, "two actions share a name: rename one of them"},
	{config.ErrActionNameRequired, "an actions[] entry needs a name"},
	{
		config.ErrInterfaceDryRun,
		"dry_run on an interface block needs that block to carry its own rules",
	},
	{config.ErrWatchInterval, "server.watch must be 0 or a duration of at least 1s"},
	{
		config.ErrRateLimit,
		"security.rate_limit must be a positive rate (10/s, 600/m, 3600/h) or 0 to disable it",
	},
	{config.ErrCooldown, "security.cooldown must be 0 or a duration such as 5s"},
	{
		os.ErrNotExist,
		"that file does not exist: check the path, or drop --config to use the default locations",
	},
	{errNoRules, "nothing is configured to listen for: add rules to the file, or pass --port on the command line"},
}

// writeCheckTable prints the report the way a person reads it: the file, then each problem with
// its fix, then what the configuration resolves to.
func writeCheckTable(out io.Writer, report checkReport) {
	writer := tabwriter.NewWriter(out, 0, 0, checkTablePadding, ' ', 0)

	fmt.Fprintf(writer, "configuration\t%s\t(source: %s)\n", checkConfigLine(report.Config), sourceLabel(report.ConfigSource))

	if report.OK {
		fmt.Fprintln(writer, "result\tok - this configuration would start")
	} else {
		fmt.Fprintf(writer, "result\t%d problem(s)\n", len(report.Problems))
	}

	for index, problem := range report.Problems {
		fmt.Fprintf(writer, "  %d\t%s\n", index+1, problem.Problem)

		if problem.Hint != "" {
			fmt.Fprintf(writer, "    \tfix: %s\n", problem.Hint)
		}
	}

	// A configuration that could not even be read has nothing to summarise: empty lines would
	// read as "no interfaces, no log destination" instead of "we never got that far".
	if report.Loaded {
		fmt.Fprintf(writer, "rules\t%d\t%s\n", report.Summary.Rules, portSummary(report.Summary.Ports))
		fmt.Fprintf(writer, "remote ports\t%s\n", listSummary(report.Summary.RemotePorts))
		fmt.Fprintf(writer, "named actions\t%d\n", report.Summary.Actions)
		fmt.Fprintf(writer, "interfaces\t%s\n", report.Summary.Interfaces)
		fmt.Fprintf(writer, "log\t%s\n", report.Summary.Log)
		fmt.Fprintf(writer, "dry run\t%s\n", yesNo(report.Summary.DryRun))
	}

	if report.OK {
		fmt.Fprintln(writer, "next\tsol listen          (start listening)")
	} else {
		fmt.Fprintln(writer, "next\tfix the problem above, then run: sol config check")
	}

	_ = writer.Flush()
}

// checkConfigLine is the configuration path, or a marker when there is none.
func checkConfigLine(path string) string {
	if path == "" {
		return "(none - the built-in defaults, which carry no rules)"
	}

	return path
}

// portSummary renders the ports the rules listen on, or says that there are none.
func portSummary(ports []int) string {
	if len(ports) == 0 {
		return "(none)"
	}

	return listSummary(ports)
}

// listSummary renders a list of numbers, or a dash when it is empty.
func listSummary(values []int) string {
	if len(values) == 0 {
		return "-"
	}

	parts := make([]string, 0, len(values))

	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}

	return strings.Join(parts, ", ")
}

// yesNo renders a boolean the way the report reads it.
func yesNo(value bool) string {
	if value {
		return "yes"
	}

	return "no"
}

// writeCheckJSON prints the report as JSON, for the installer's pre-upgrade check and for CI.
func writeCheckJSON(out io.Writer, report checkReport) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)

	return encoder.Encode(report)
}

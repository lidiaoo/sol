package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bavix/sol/internal/config"
	"github.com/bavix/sol/internal/deps"
	"github.com/bavix/sol/internal/domain/wol"
	"github.com/bavix/sol/internal/infra/logging"
)

var errNoRules = errors.New("no rules configured: pass --port or set rules in the config file")

const portPartsCount = 2

var listenCmd = &cobra.Command{
	Use:   "listen",
	Short: "Listen for magic packets and trigger an action",
	Long: "Listen for Wake-on-LAN magic packets on the given interfaces (or every eligible\n" +
		"interface by default) and trigger an action when a packet matches a rule.\n" +
		"Rules come from the configuration file unless --port is given, which takes over\n" +
		"the whole rule set.",
	RunE: func(command *cobra.Command, _ []string) error {
		cfg, err := buildConfig(command)
		if err != nil {
			return err
		}

		builder := deps.NewBuilder(cfg)

		application, buildErr := builder.BuildListenService()
		if buildErr != nil {
			return buildErr
		}

		server, httpErr := builder.BuildHTTPServer()
		if httpErr != nil {
			return httpErr
		}

		ctx := command.Context()

		if server != nil {
			go func() {
				if runErr := server.Run(ctx); runErr != nil {
					slog.Error("http control plane stopped", "error", runErr)
				}
			}()
		}

		return application.Run(ctx)
	},
}

var (
	interfaceNames       []string
	dryRun               bool
	portStrings          []string
	defaultActionName    string
	allowReservedActions bool
	configPath           string
)

func init() {
	rootCmd.AddCommand(listenCmd)

	listenCmd.Flags().StringVar(&configPath, "config", "",
		"Configuration file to load (default: $SOL_CONFIG, then /etc/sol/sol.yaml, "+
			"then ~/.config/sol/sol.yaml)")
	listenCmd.Flags().StringArrayVar(&interfaceNames, "iface", nil,
		"Network interface to match on; repeatable (--iface eth0 --iface wlan0). "+
			"Omit to auto-select every eligible interface")
	listenCmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Log when a matching packet is received instead of executing the action")
	listenCmd.Flags().StringArrayVar(&portStrings, "port", nil,
		"UDP port to listen on, optionally with action (e.g. '8' for shutdown, '8:reboot'). "+
			"Ports 7 and 9 are reserved for plain WOL and always map to noop. Can be specified multiple times")
	listenCmd.Flags().StringVar(&defaultActionName, "default-action", "shutdown",
		"Action for ports given without an explicit action (noop|sleep|shutdown|reboot)")
	listenCmd.Flags().BoolVar(&allowReservedActions, "allow-reserved-actions", false,
		"Allow non-noop actions on the reserved ports 7 and 9 (reverts to the old behaviour)")
}

// buildConfig merges the configuration file with the CLI flags. Precedence is
// defaults < file < environment < flags.
func buildConfig(command *cobra.Command) (*config.Config, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}

	// Install the configured logger before anything else logs.
	if _, logErr := logging.Setup(cfg.Logging.Level, cfg.Logging.Format); logErr != nil {
		return nil, logErr
	}

	if len(interfaceNames) > 0 {
		cfg.InterfaceNames = interfaceNames
	}

	if command.Flags().Changed("dry-run") {
		cfg.DryRun = dryRun
	}

	if command.Flags().Changed("allow-reserved-actions") {
		cfg.AllowReservedActions = allowReservedActions
	}

	// Any --port makes the command line take over the rule set completely.
	if len(portStrings) > 0 {
		rules, rulesErr := parsePorts(cfg.AllowReservedActions)
		if rulesErr != nil {
			return nil, rulesErr
		}

		cfg.Rules = rules
	}

	// Remote command ports are bound without rules, so a config with only the §21
	// channel is valid.
	if len(cfg.Rules) == 0 && len(cfg.Remote.Ports) == 0 {
		return nil, errNoRules
	}

	return cfg, nil
}

func parsePorts(allowReserved bool) ([]wol.Rule, error) {
	defaultAction, err := wol.ParseAction(defaultActionName)
	if err != nil {
		return nil, fmt.Errorf("invalid --default-action: %w", err)
	}

	rules := make([]wol.Rule, 0, len(portStrings))

	for _, spec := range portStrings {
		port, action, specErr := parsePortSpec(spec, defaultAction)
		if specErr != nil {
			return nil, specErr
		}

		rules = append(rules, wol.Rule{
			Match:  wol.Match{Ports: []int{port}, MAC: wol.MACSelector{Kind: wol.MACSelf}},
			Action: guardReservedPort(port, action, allowReserved),
		})
	}

	return rules, nil
}

func parsePortSpec(spec string, defaultAction wol.Action) (int, wol.Action, error) {
	parts := strings.Split(spec, ":")
	action := defaultAction

	if len(parts) == portPartsCount {
		parsed, err := wol.ParseAction(parts[1])
		if err != nil {
			return 0, "", fmt.Errorf("invalid action in port %s: %w", spec, err)
		}

		action = parsed
	}

	var port int
	if _, err := fmt.Sscanf(parts[0], "%d", &port); err != nil {
		return 0, "", fmt.Errorf("invalid port %s: %w", parts[0], err)
	}

	return port, action, nil
}

// guardReservedPort forces reserved WOL ports to noop and warns about the behaviour change.
func guardReservedPort(port int, action wol.Action, allow bool) wol.Action {
	if allow || action == wol.ActionNoop || !slices.Contains(wol.DefaultReservedPorts(), port) {
		return action
	}

	slog.Warn("reserved port downgraded to noop", "port", port, "action", string(action))

	return wol.ActionNoop
}

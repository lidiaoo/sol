package cmd

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bavix/sol/internal/config"
	"github.com/bavix/sol/internal/deps"
	"github.com/bavix/sol/internal/domain/wol"
)

var errNoPorts = errors.New("at least one --port flag is required")

const portPartsCount = 2

var listenCmd = &cobra.Command{
	Use:   "listen",
	Short: "Listen for magic packets and trigger an action",
	Long: "Listen for Wake-on-LAN magic packets on the given interfaces (or every eligible\n" +
		"interface by default) and trigger an action when a packet matches a rule.",
	RunE: func(command *cobra.Command, _ []string) error {
		parsedRules, err := parsePorts()
		if err != nil {
			return err
		}

		cfg := &config.Config{
			InterfaceNames:       interfaceNames,
			DryRun:               dryRun,
			AllowReservedActions: allowReservedActions,
			Rules:                parsedRules,
		}

		builder := deps.NewBuilder(cfg)

		application, buildErr := builder.BuildListenService()
		if buildErr != nil {
			return buildErr
		}

		return application.Run(command.Context())
	},
}

var (
	interfaceNames       []string
	dryRun               bool
	portStrings          []string
	defaultActionName    string
	allowReservedActions bool
)

func init() {
	rootCmd.AddCommand(listenCmd)

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

func parsePorts() ([]wol.Rule, error) {
	if len(portStrings) == 0 {
		return nil, errNoPorts
	}

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
			Action: guardReservedPort(port, action, allowReservedActions),
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

	log.Printf("WARNING: port %d is reserved for plain WOL packets; downgrading action %q to noop", port, action)

	return wol.ActionNoop
}

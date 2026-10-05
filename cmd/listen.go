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
	Short: "Listen for magic packets and trigger power action",
	Long:  "Listen for Wake-on-LAN magic packets on the specified network interface and trigger power action (shutdown/reboot) when received.",
	RunE: func(command *cobra.Command, _ []string) error {
		parsedRules, err := parsePorts()
		if err != nil {
			return err
		}

		cfg := &config.Config{
			InterfaceName: interfaceName,
			DryRun:        dryRun,
			Rules:         parsedRules,
		}

		builder := deps.NewBuilder(cfg)

		application, buildErr := builder.BuildListenService()
		if buildErr != nil {
			return buildErr
		}

		return application.Run(command.Context(), cfg.InterfaceName)
	},
}

var (
	interfaceName string
	dryRun        bool
	portStrings   []string
)

func init() {
	rootCmd.AddCommand(listenCmd)

	listenCmd.Flags().StringVar(&interfaceName, "iface", "", "Network interface name to bind to (required)")
	listenCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Log when a matching packet is received instead of executing the power action")
	listenCmd.Flags().StringArrayVar(&portStrings, "port", nil,
		"UDP port to listen on, optionally with action (e.g. '8' for shutdown, '8:reboot'). "+
			"Ports 7 and 9 are reserved for plain WOL and always map to noop. Can be specified multiple times")

	_ = listenCmd.MarkFlagRequired("iface")
}

func parsePorts() ([]wol.Rule, error) {
	if len(portStrings) == 0 {
		return nil, errNoPorts
	}

	rules := make([]wol.Rule, 0, len(portStrings))

	for _, spec := range portStrings {
		port, action, err := parsePortSpec(spec)
		if err != nil {
			return nil, err
		}

		rules = append(rules, wol.Rule{
			Match:  wol.Match{Ports: []int{port}, MAC: wol.MACSelector{Kind: wol.MACSelf}},
			Action: guardReservedPort(port, action),
		})
	}

	return rules, nil
}

func parsePortSpec(spec string) (int, wol.Action, error) {
	parts := strings.Split(spec, ":")
	action := wol.ActionShutdown

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
func guardReservedPort(port int, action wol.Action) wol.Action {
	if action == wol.ActionNoop || !slices.Contains(wol.DefaultReservedPorts(), port) {
		return action
	}

	log.Printf("WARNING: port %d is reserved for plain WOL packets; downgrading action %q to noop", port, action)

	return wol.ActionNoop
}

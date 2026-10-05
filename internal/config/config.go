package config

import "github.com/bavix/sol/internal/domain/wol"

// Config is the resolved application configuration: the result of merging
// built-in defaults, the configuration file, environment variables and CLI flags.
type Config struct {
	// InterfaceNames lists the interfaces to match on; empty means auto-select
	// every eligible interface.
	InterfaceNames []string
	// DryRun logs matching packets instead of executing their action.
	DryRun bool
	// AllowReservedActions permits non-noop actions on the reserved ports.
	AllowReservedActions bool
	// ReservedPorts overrides the reserved WOL ports; empty means the defaults.
	ReservedPorts []int
	// SecureOn is the optional SecureOn password expected after the magic packet.
	SecureOn []byte
	// Actions are the known named actions; empty means the built-in set.
	Actions map[wol.Action]wol.ActionDef
	// Rules are the routing rules, already expanded from any interface blocks.
	Rules []wol.Rule
}

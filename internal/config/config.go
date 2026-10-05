package config

import "github.com/bavix/sol/internal/domain/wol"

// Control-plane auth types accepted by server.http.auth.type (§18.1).
const (
	AuthTypeBearer = "bearer"
	AuthTypeBasic  = "basic"
	AuthTypeMTLS   = "mtls"
)

// HTTP describes the optional control plane (server.http).
type HTTP struct {
	Enabled  bool
	Listen   string
	AuthType string
	// Token is the resolved bearer secret; never logged.
	Token string
	// User and Password are the resolved basic-auth credentials; never logged.
	User     string
	Password string
	CertFile string
	KeyFile  string
	// ClientCAFile is required for auth type mtls.
	ClientCAFile string
}

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
	// ExecAllowlist restricts absolute exec commands to these directories; empty allows any.
	ExecAllowlist []string
	// Actions are the known named actions; empty means the built-in set.
	Actions map[wol.Action]wol.ActionDef
	// Logging holds the raw logging settings; empty values mean the defaults.
	Logging Logging
	// HTTP holds the resolved control-plane settings.
	HTTP HTTP
	// Rules are the routing rules, already expanded from any interface blocks.
	Rules []wol.Rule
}

// Logging holds the configured log level and format. Empty values mean the built-in
// defaults (info / text); validation lives in internal/infra/logging.
type Logging struct {
	Level  string
	Format string
}

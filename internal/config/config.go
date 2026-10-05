package config

import (
	"net"
	"regexp"
	"time"

	"github.com/bavix/sol/internal/domain/wol"
)

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
	// Cooldown is the minimum interval between two executions of the same action; zero disables it.
	Cooldown time.Duration
	// ActionCooldowns overrides Cooldown per action name.
	ActionCooldowns map[wol.Action]time.Duration
	// RateLimit is the global cap on action executions per second; zero disables it.
	RateLimit float64
	// RateBurst is the size of the rate-limit bucket; zero means one second of RateLimit.
	RateBurst int
	// Actions are the known named actions; empty means the built-in set.
	Actions map[wol.Action]wol.ActionDef
	// Logging holds the raw logging settings; empty values mean the defaults.
	Logging Logging
	// URLAllowlist, when set, restricts outbound HTTP actions (§18.2).
	URLAllowlist []string
	// PacketKey, when set, authenticates whole packets (§19.14); rules with `auth: hmac`
	// only match packets carrying a valid tag.
	PacketKey []byte
	// PacketWindow, when positive, makes those packets carry a stamp the receiver accepts only
	// once, so a captured packet cannot be replayed (§19.16).
	PacketWindow time.Duration
	// Remote holds the resolved remote command channel (§21).
	Remote RemoteCommands
	// HTTP holds the resolved control-plane settings.
	HTTP HTTP
	// Watch is the configuration-file poll interval of the automatic reload; zero disables it.
	Watch time.Duration
	// Rules are the routing rules, already expanded from any interface blocks.
	Rules []wol.Rule
}

// RemoteCommands is the resolved remote command channel (§21).
type RemoteCommands struct {
	// Enabled reports whether remote senders may invoke the whitelisted commands.
	Enabled bool
	// Ports are the UDP ports whose packets may carry a remote command segment.
	Ports []int
	// HMACKey authenticates UDP command segments; empty means the UDP transport is
	// not configured (only the authenticated HTTP transport may then be used).
	HMACKey []byte
	// Commands maps a command id to its whitelisted definition.
	Commands map[string]wol.RemoteCommand
	// RawShell is the resolved raw shell transport (§21.6); Enabled is false unless the
	// operator asked for it.
	RawShell RawShell
}

// RawShell is the resolved raw shell channel of §21.6: remote senders may put a shell command
// line into the packet instead of a whitelisted command id. It is the only place in sol where a
// shell runs, and it stays off by default.
type RawShell struct {
	// Enabled reports whether the channel is on at all.
	Enabled bool
	// Ports are the dedicated UDP ports whose packets carry a shell command.
	Ports []int
	// Key authenticates those packets; the channel is never enabled without it.
	Key []byte
	// SrcNets, when set, restricts which senders may use the channel (empty allows any).
	SrcNets []*net.IPNet
	// Allowlist, when set, restricts which command lines may run (empty allows any).
	Allowlist []*regexp.Regexp
	// Exec carries the execution settings that apply to every command: shell mode, timeout
	// and the optional privilege drop. Command itself is empty and filled per packet.
	Exec wol.ExecParams
}

// Logging holds the configured log level and format. Empty values mean the built-in
// defaults (info / text); validation lives in internal/infra/logging.
type Logging struct {
	Level  string
	Format string
}

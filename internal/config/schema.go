package config

import (
	"errors"
	"fmt"
	"net"

	"gopkg.in/yaml.v3"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

// supportedVersion is the only configuration schema version accepted for now.
const supportedVersion = 1

var (
	ErrUnsupportedVersion = errors.New("unsupported config version")
	ErrRulesConflict      = errors.New("both top-level rules and server.rules are set; keep only one (they are equivalent)")
	ErrUnknownActionType  = errors.New("unknown action type")
	ErrDuplicateAction    = errors.New("duplicate action name")
	ErrInterfaceBlock     = errors.New("invalid server.interfaces entry")
)

// fileConfig mirrors the on-disk YAML document.
type fileConfig struct {
	Version  int             `yaml:"version"`
	Server   serverConfig    `yaml:"server"`
	Security securityConfig  `yaml:"security"`
	Logging  loggingConfig   `yaml:"logging"`
	Actions  []actionConfig  `yaml:"actions"`
	Commands []commandConfig `yaml:"commands"`
	Rules    []ruleConfig    `yaml:"rules"`
}

type loggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
	// Output is where the audit log goes: stderr (default), stdout, or the file named by File
	// (§18). The names are duplicated in internal/infra/logging on purpose -- config does not
	// depend on infra -- and a test keeps the two sets equal.
	Output string `yaml:"output"`
	File   string `yaml:"file"`
}

const (
	loggingOutputStderr = "stderr"
	loggingOutputStdout = "stdout"
	loggingOutputFile   = "file"
)

// serverConfig is the server section: the control plane, the interfaces and the rules.
type serverConfig struct {
	HTTP       httpConfig    `yaml:"http"`
	Interfaces []ifaceConfig `yaml:"interfaces"`
	Rules      []ruleConfig  `yaml:"rules"`
	// Watch is the config-file poll interval ("5s"); empty disables watching.
	Watch string `yaml:"watch"`
}

type ifaceConfig struct {
	Name   string `yaml:"name"`
	DryRun *bool  `yaml:"dry_run"`
	// SecureOn is the password this block's rules require; a rule can override it, and an
	// explicitly empty value requires packets without a password (§19.18).
	SecureOn *string      `yaml:"secure_on"`
	Rules    []ruleConfig `yaml:"rules"`
}

// UnmarshalYAML accepts either a bare interface name or a block with per-interface overrides.
func (i *ifaceConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var name string
		if err := node.Decode(&name); err != nil {
			return fmt.Errorf("%w: %w", ErrInterfaceBlock, err)
		}

		*i = ifaceConfig{Name: name}

		return nil
	}

	type plain ifaceConfig

	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return fmt.Errorf("%w: %w", ErrInterfaceBlock, err)
	}

	*i = ifaceConfig(decoded)

	if i.Name == "" {
		return fmt.Errorf("%w: a block entry requires a name", ErrInterfaceBlock)
	}

	return nil
}

type httpConfig struct {
	Enabled bool       `yaml:"enabled"`
	Listen  string     `yaml:"listen"`
	Auth    authConfig `yaml:"auth"`
	TLS     tlsConfig  `yaml:"tls"`
}

type authConfig struct {
	Type         string `yaml:"type"`
	TokenEnv     string `yaml:"token_env"`
	TokenFile    string `yaml:"token_file"`
	User         string `yaml:"user"`
	PasswordEnv  string `yaml:"password_env"`
	PasswordFile string `yaml:"password_file"`
}

type tlsConfig struct {
	CertFile     string `yaml:"cert_file"`
	KeyFile      string `yaml:"key_file"`
	ClientCAFile string `yaml:"client_ca_file"`
}

type securityConfig struct {
	DryRun                   bool              `yaml:"dry_run"`
	ReservedPorts            []int             `yaml:"reserved_ports"`
	AllowReservedPortActions bool              `yaml:"allow_reserved_port_actions"`
	SecureOn                 string            `yaml:"secure_on"`
	ExecAllowlist            []string          `yaml:"exec_allowlist"`
	Cooldown                 string            `yaml:"cooldown"`
	Cooldowns                map[string]string `yaml:"cooldowns"`
	// Settle is the window right after sol starts (a boot, or a restart) or right after the machine
	// resumes from suspend during which SettleActions are suppressed; empty means the built-in 2m.
	Settle              string           `yaml:"settle"`
	SettleActions       []string         `yaml:"settle_actions"`
	RateLimit           string           `yaml:"rate_limit"`
	RateBurst           int              `yaml:"rate_burst"`
	AllowRemoteCommands bool             `yaml:"allow_remote_commands"`
	RemoteCommandAuth   remoteAuthConfig `yaml:"remote_command_auth"`
	PacketAuth          packetAuthConfig `yaml:"packet_auth"`
	RemoteCommandPorts  []int            `yaml:"remote_command_ports"`
	URLAllowlist        []string         `yaml:"url_allowlist"`
	AllowRawShell       bool             `yaml:"allow_raw_shell"`
	RawShellPorts       []int            `yaml:"raw_shell_ports"`
	RawShellAuth        remoteAuthConfig `yaml:"raw_shell_auth"`
	RawShellSrcCIDRs    []string         `yaml:"raw_shell_src_cidrs"`
	RawShellAllowlist   []string         `yaml:"raw_shell_allowlist"`
	RawShellTimeout     string           `yaml:"raw_shell_timeout"`
	RawShellUser        string           `yaml:"raw_shell_user"`
	RawShellGroup       string           `yaml:"raw_shell_group"`
}

// remoteAuthConfig is security.remote_command_auth: the shared key authenticating UDP
// remote command segments (§21.3). The key never lives in YAML.
type remoteAuthConfig struct {
	Type    string `yaml:"type"`
	KeyEnv  string `yaml:"key_env"`
	KeyFile string `yaml:"key_file"`
	// Window, when set, turns on replay protection for the channel this key belongs to: the
	// remote command segment becomes [stamp][tag] and each tag is accepted once (§21.3).
	Window string `yaml:"window"`
}

// packetAuthConfig is security.packet_auth: the shared key that authenticates whole packets
// (§19.14). The key never lives in YAML, exactly like the remote command key.
type packetAuthConfig struct {
	Type    string `yaml:"type"`
	KeyEnv  string `yaml:"key_env"`
	KeyFile string `yaml:"key_file"`
	// Window, when set, turns on replay protection (§19.16): packets must carry a stamp the
	// receiver accepts, and each tag is used once.
	Window string `yaml:"window"`
}

// commandConfig is one whitelisted remote command of the commands[] section (§21.3).
type commandConfig struct {
	ID      string               `yaml:"id"`
	Type    string               `yaml:"type"`
	Command []string             `yaml:"command"`
	Args    map[string]argConfig `yaml:"args"`
	Timeout string               `yaml:"timeout"`
	Workdir string               `yaml:"workdir"`
	Env     []string             `yaml:"env"`
	User    string               `yaml:"user"`
	Group   string               `yaml:"group"`
}

// argConfig constrains one remote command argument.
type argConfig struct {
	Type     string   `yaml:"type"`
	Enum     []string `yaml:"enum"`
	Pattern  string   `yaml:"pattern"`
	Required *bool    `yaml:"required"`
}

type actionConfig struct {
	Name      string            `yaml:"name"`
	Type      string            `yaml:"type"`
	Command   []string          `yaml:"command"`
	Timeout   string            `yaml:"timeout"`
	Workdir   string            `yaml:"workdir"`
	Env       []string          `yaml:"env"`
	User      string            `yaml:"user"`
	Group     string            `yaml:"group"`
	Shell     bool              `yaml:"shell"`
	Method    string            `yaml:"method"`
	URL       string            `yaml:"url"`
	Headers   map[string]string `yaml:"headers"`
	Body      string            `yaml:"body"`
	Retries   int               `yaml:"retries"`
	Proxy     string            `yaml:"proxy"`
	Steps     []string          `yaml:"steps"`
	MAC       string            `yaml:"mac"`
	Broadcast string            `yaml:"broadcast"`
	Port      int               `yaml:"port"`
	SecureOn  string            `yaml:"secure_on"`
	Repeat    int               `yaml:"repeat"`
	Interval  string            `yaml:"interval"`
	Sign      bool              `yaml:"sign"`
}

type ruleConfig struct {
	Match  matchConfig `yaml:"match"`
	Action string      `yaml:"action"`
	DryRun *bool       `yaml:"dry_run"`
}

type matchConfig struct {
	Ports      []int         `yaml:"ports"`
	Interfaces []string      `yaml:"interfaces"`
	MAC        macConfig     `yaml:"mac"`
	Content    contentConfig `yaml:"content"`
	SrcCIDRs   []string      `yaml:"src_cidrs"`
	Auth       string        `yaml:"auth"`
	SecureOn   *string       `yaml:"secure_on"`
}

type macConfig struct {
	Kind       string   `yaml:"kind"`
	Address    string   `yaml:"address"`
	Interfaces []string `yaml:"interfaces"`
}

// UnmarshalYAML accepts a scalar ("self", "any" or a MAC address) or a block.
func (m *macConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var value string
		if err := node.Decode(&value); err != nil {
			return err
		}

		if _, err := net.ParseMAC(value); err == nil {
			*m = macConfig{Kind: string(wol.MACExplicit), Address: value}

			return nil
		}

		*m = macConfig{Kind: value}

		return nil
	}

	type plain macConfig

	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}

	*m = macConfig(decoded)

	return nil
}

type contentConfig struct {
	Kind     string `yaml:"kind"`
	Value    string `yaml:"value"`
	ValueHex string `yaml:"value_hex"`
	Offset   int    `yaml:"offset"`
}

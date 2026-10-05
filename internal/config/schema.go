package config

import (
	"errors"
	"fmt"
	"net"

	"gopkg.in/yaml.v3"

	"github.com/bavix/sol/internal/domain/wol"
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
	Version  int            `yaml:"version"`
	Server   serverConfig   `yaml:"server"`
	Security securityConfig `yaml:"security"`
	Logging  loggingConfig  `yaml:"logging"`
	Actions  []actionConfig `yaml:"actions"`
	Rules    []ruleConfig   `yaml:"rules"`
}

type loggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type serverConfig struct {
	HTTP       httpConfig    `yaml:"http"`
	Interfaces []ifaceConfig `yaml:"interfaces"`
	Rules      []ruleConfig  `yaml:"rules"`
}

type ifaceConfig struct {
	Name     string       `yaml:"name"`
	DryRun   *bool        `yaml:"dry_run"`
	SecureOn string       `yaml:"secure_on"`
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
}

type actionConfig struct {
	Name    string   `yaml:"name"`
	Type    string   `yaml:"type"`
	Command []string `yaml:"command"`
	Timeout string   `yaml:"timeout"`
	Workdir string   `yaml:"workdir"`
	Env     []string `yaml:"env"`
	User    string   `yaml:"user"`
	Group   string   `yaml:"group"`
	Shell   bool     `yaml:"shell"`
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

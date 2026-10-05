package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bavix/sol/internal/domain/wol"
)

var (
	ErrEnvValue             = errors.New("invalid environment override")
	ErrMissingEnvVar        = errors.New("environment variable referenced by the config is not set")
	ErrActionRequired       = errors.New("rule requires an action")
	ErrInterfaceDryRun      = errors.New("dry_run on an interface block requires block-level rules")
	ErrActionNameRequired   = errors.New("actions[] entry requires a name")
	ErrPerInterfaceSecureOn = errors.New("per-interface secure_on is not implemented yet")
)

// systemConfigPath is the system-wide configuration location.
const systemConfigPath = "/etc/sol/sol.yaml"

// Environment overrides, applied after the file and before CLI flags.
const (
	EnvConfig                   = "SOL_CONFIG"
	EnvDryRun                   = "SOL_DRY_RUN"
	EnvAllowReservedPortActions = "SOL_ALLOW_RESERVED_PORT_ACTIONS"
	EnvInterfaces               = "SOL_INTERFACES"
	EnvSecureOn                 = "SOL_SECURE_ON"
	EnvLogLevel                 = "SOL_LOG_LEVEL"
	EnvLogFormat                = "SOL_LOG_FORMAT"
)

// DefaultPaths returns the configuration file locations checked in order.
func DefaultPaths() []string {
	paths := []string{systemConfigPath}

	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".config", "sol", "sol.yaml"))
	}

	return paths
}

// Load reads the configuration from path. An empty path falls back to $SOL_CONFIG and
// then to DefaultPaths(); when no file exists the built-in defaults are returned.
// Environment overrides are applied here, CLI flags are applied by the caller.
func Load(path string) (*Config, error) {
	cfg, err := loadFile(path)
	if err != nil {
		return nil, err
	}

	if err := applyEnv(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func loadFile(path string) (*Config, error) {
	resolved := resolvePath(path)
	if resolved == "" {
		return defaults(), nil
	}

	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", resolved, err)
	}

	expanded, err := expandEnv(string(raw))
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", resolved, err)
	}

	var file fileConfig

	decoder := yaml.NewDecoder(strings.NewReader(expanded))
	decoder.KnownFields(true)

	if decodeErr := decoder.Decode(&file); decodeErr != nil && !errors.Is(decodeErr, io.EOF) {
		return nil, fmt.Errorf("decode config %s: %w", resolved, decodeErr)
	}

	cfg, err := file.toConfig()
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", resolved, err)
	}

	return cfg, nil
}

func resolvePath(path string) string {
	if path != "" {
		return path
	}

	if fromEnv := os.Getenv(EnvConfig); fromEnv != "" {
		return fromEnv
	}

	for _, candidate := range DefaultPaths() {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	return ""
}

func defaults() *Config {
	return &Config{Actions: wol.BuiltinActions()}
}

// expandEnv substitutes $VAR and ${VAR} using the process environment and fails
// loudly when a referenced variable is missing.
func expandEnv(text string) (string, error) {
	var missing []string

	expanded := os.Expand(text, func(key string) string {
		value, ok := os.LookupEnv(key)
		if !ok {
			missing = append(missing, key)

			return ""
		}

		return value
	})

	if len(missing) > 0 {
		return "", fmt.Errorf("%w: %s", ErrMissingEnvVar, strings.Join(missing, ", "))
	}

	return expanded, nil
}

func applyEnv(cfg *Config) error {
	boolOverrides := []struct {
		name  string
		apply func(bool)
	}{
		{name: EnvDryRun, apply: func(v bool) { cfg.DryRun = v }},
		{name: EnvAllowReservedPortActions, apply: func(v bool) { cfg.AllowReservedActions = v }},
	}

	for _, override := range boolOverrides {
		value, ok := os.LookupEnv(override.name)
		if !ok {
			continue
		}

		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%w: %s=%q", ErrEnvValue, override.name, value)
		}

		override.apply(parsed)
	}

	if value, ok := os.LookupEnv(EnvInterfaces); ok {
		cfg.InterfaceNames = splitList(value)
	}

	if value, ok := os.LookupEnv(EnvSecureOn); ok {
		cfg.SecureOn = []byte(value)
	}

	if value, ok := os.LookupEnv(EnvLogLevel); ok {
		cfg.Logging.Level = value
	}

	if value, ok := os.LookupEnv(EnvLogFormat); ok {
		cfg.Logging.Format = value
	}

	return nil
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))

	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}

	return out
}

func (f *fileConfig) toConfig() (*Config, error) {
	if f.Version != 0 && f.Version != supportedVersion {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedVersion, f.Version)
	}

	if len(f.Rules) > 0 && len(f.Server.Rules) > 0 {
		return nil, ErrRulesConflict
	}

	actions, err := buildActions(f.Actions)
	if err != nil {
		return nil, err
	}

	names, err := buildInterfaceNames(f.Server.Interfaces)
	if err != nil {
		return nil, err
	}

	global, err := buildRules(globalRules(f), nil, false, actions)
	if err != nil {
		return nil, err
	}

	scoped, err := buildInterfaceRules(f.Server.Interfaces, actions)
	if err != nil {
		return nil, err
	}

	return &Config{
		InterfaceNames:       names,
		DryRun:               f.Security.DryRun,
		AllowReservedActions: f.Security.AllowReservedPortActions,
		ReservedPorts:        f.Security.ReservedPorts,
		SecureOn:             secureOnBytes(f.Security.SecureOn),
		Actions:              actions,
		Logging:              Logging{Level: f.Logging.Level, Format: f.Logging.Format},
		Rules:                append(global, scoped...),
	}, nil
}

func globalRules(f *fileConfig) []ruleConfig {
	if len(f.Server.Rules) > 0 {
		return f.Server.Rules
	}

	return f.Rules
}

func secureOnBytes(value string) []byte {
	if value == "" {
		return nil
	}

	return []byte(value)
}

func buildActions(entries []actionConfig) (map[wol.Action]wol.ActionDef, error) {
	actions := wol.BuiltinActions()

	for i, entry := range entries {
		if entry.Name == "" {
			return nil, fmt.Errorf("%w: index %d", ErrActionNameRequired, i)
		}

		if _, exists := actions[wol.Action(entry.Name)]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateAction, entry.Name)
		}

		actionType, err := parseActionType(entry.Type)
		if err != nil {
			return nil, fmt.Errorf("actions[%d] (%s): %w", i, entry.Name, err)
		}

		actions[wol.Action(entry.Name)] = wol.ActionDef{
			Name: wol.Action(entry.Name),
			Type: actionType,
		}
	}

	return actions, nil
}

func parseActionType(value string) (wol.ActionType, error) {
	switch wol.ActionType(value) {
	case wol.ActionTypeNoop, wol.ActionTypeShutdown, wol.ActionTypeReboot, wol.ActionTypeSleep:
		return wol.ActionType(value), nil
	default:
		return "", fmt.Errorf("%w: %q (exec and http actions land in P4)", ErrUnknownActionType, value)
	}
}

func buildInterfaceNames(entries []ifaceConfig) ([]string, error) {
	names := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))

	for i, entry := range entries {
		if seen[entry.Name] {
			return nil, fmt.Errorf("%w: %s", wol.ErrDuplicateInterface, entry.Name)
		}

		seen[entry.Name] = true

		if entry.SecureOn != "" {
			return nil, fmt.Errorf("server.interfaces[%d] (%s): %w", i, entry.Name, ErrPerInterfaceSecureOn)
		}

		names = append(names, entry.Name)
	}

	return names, nil
}

func buildInterfaceRules(entries []ifaceConfig, actions map[wol.Action]wol.ActionDef) ([]wol.Rule, error) {
	var rules []wol.Rule

	for i, entry := range entries {
		blockDryRun := entry.DryRun != nil && *entry.DryRun

		if blockDryRun && len(entry.Rules) == 0 {
			return nil, fmt.Errorf("server.interfaces[%d] (%s): %w", i, entry.Name, ErrInterfaceDryRun)
		}

		scope := wol.MACSelector{Kind: wol.MACInterface, Ifaces: []string{entry.Name}}

		built, err := buildRules(entry.Rules, &scope, blockDryRun, actions)
		if err != nil {
			return nil, fmt.Errorf("server.interfaces[%d] (%s): %w", i, entry.Name, err)
		}

		rules = append(rules, built...)
	}

	return rules, nil
}

func buildRules(entries []ruleConfig, scope *wol.MACSelector, dryRun bool, actions map[wol.Action]wol.ActionDef) ([]wol.Rule, error) {
	rules := make([]wol.Rule, 0, len(entries))

	for i, entry := range entries {
		rule, err := buildRule(entry, scope, dryRun, actions)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", i+1, err)
		}

		rules = append(rules, rule)
	}

	return rules, nil
}

func buildRule(entry ruleConfig, scope *wol.MACSelector, dryRun bool, actions map[wol.Action]wol.ActionDef) (wol.Rule, error) {
	if entry.Action == "" {
		return wol.Rule{}, ErrActionRequired
	}

	action, err := resolveAction(entry.Action, actions)
	if err != nil {
		return wol.Rule{}, err
	}

	mac, err := buildMAC(entry.Match, scope)
	if err != nil {
		return wol.Rule{}, err
	}

	ruleDryRun := dryRun
	if entry.DryRun != nil {
		ruleDryRun = *entry.DryRun
	}

	return wol.Rule{
		Match: wol.Match{
			Ports:    entry.Match.Ports,
			MAC:      mac,
			Content:  buildContent(entry.Match.Content),
			SrcCIDRs: entry.Match.SrcCIDRs,
		},
		Action: action,
		DryRun: ruleDryRun,
	}, nil
}

func resolveAction(name string, actions map[wol.Action]wol.ActionDef) (wol.Action, error) {
	if _, ok := actions[wol.Action(name)]; ok {
		return wol.Action(name), nil
	}

	parsed, err := wol.ParseAction(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s", wol.ErrUnknownActionRef, name)
	}

	if _, ok := actions[parsed]; !ok {
		return "", fmt.Errorf("%w: %s", wol.ErrUnknownActionRef, name)
	}

	return parsed, nil
}

func buildMAC(match matchConfig, scope *wol.MACSelector) (wol.MACSelector, error) {
	explicit := match.MAC.Kind != "" || match.MAC.Address != "" || len(match.MAC.Interfaces) > 0

	if len(match.Interfaces) > 0 {
		if explicit {
			return wol.MACSelector{}, wol.ErrMACConflict
		}

		return wol.MACSelector{Kind: wol.MACInterface, Ifaces: match.Interfaces}, nil
	}

	if explicit {
		return wol.MACSelector{
			Kind:    wol.MACKind(match.MAC.Kind),
			Address: match.MAC.Address,
			Ifaces:  match.MAC.Interfaces,
		}, nil
	}

	if scope != nil {
		return *scope, nil
	}

	return wol.MACSelector{}, nil
}

func buildContent(content contentConfig) wol.ContentMatcher {
	return wol.ContentMatcher{
		Kind:   wol.ContentKind(content.Kind),
		Value:  content.Value,
		Hex:    content.ValueHex,
		Offset: content.Offset,
	}
}

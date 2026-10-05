package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

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
	ErrExecCommandRequired  = errors.New("exec action requires command")
	ErrExecTimeout          = errors.New("invalid exec timeout")
	ErrExecUserUnsupported  = errors.New("exec user/group privilege drop is not implemented yet")
	ErrActionParams         = errors.New("action parameters do not match its type")
	ErrHTTPListen           = errors.New("invalid server.http.listen address")
	ErrHTTPAuthType         = errors.New("unknown server.http.auth.type")
	ErrHTTPSecret           = errors.New("cannot resolve the http auth secret")
	ErrHTTPUser             = errors.New("server.http.auth.user is required for basic auth")
	ErrHTTPTLS              = errors.New("invalid server.http.tls configuration")
	ErrCooldown             = errors.New("invalid security.cooldown")
	ErrRemoteCommandID      = errors.New("invalid remote command id")
	ErrRemoteCommandType    = errors.New("unsupported remote command type")
	ErrRemoteCommandDef     = errors.New("invalid remote command definition")
	ErrRemoteArgSpec        = errors.New("invalid remote command argument spec")
	ErrRemoteAuth           = errors.New("remote command authorization is required")
	ErrRemotePorts          = errors.New("invalid security.remote_command_ports")
	ErrRemotePort           = errors.New("remote command port must not be a reserved port")
	ErrHTTPURLRequired      = errors.New("http action requires url")
)

const (
	authTypeBearer    = AuthTypeBearer
	authTypeHMAC      = "hmac"
	authTypeBasic     = AuthTypeBasic
	authTypeMTLS      = AuthTypeMTLS
	defaultHTTPListen = "127.0.0.1:8080"
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
	if err := f.validate(); err != nil {
		return nil, err
	}

	actions, err := buildActions(f.Actions)
	if err != nil {
		return nil, err
	}

	names, err := buildInterfaceNames(f.Server.Interfaces)
	if err != nil {
		return nil, err
	}

	rules, err := buildAllRules(f, actions)
	if err != nil {
		return nil, err
	}

	httpCfg, err := buildHTTP(f.Server.HTTP)
	if err != nil {
		return nil, err
	}

	cooldown, actionCooldowns, err := buildCooldowns(f.Security, actions)
	if err != nil {
		return nil, err
	}

	remote, err := buildRemoteCommands(f.Security, f.Commands)
	if err != nil {
		return nil, err
	}

	return &Config{
		InterfaceNames:       names,
		DryRun:               f.Security.DryRun,
		AllowReservedActions: f.Security.AllowReservedPortActions,
		ReservedPorts:        f.Security.ReservedPorts,
		SecureOn:             secureOnBytes(f.Security.SecureOn),
		ExecAllowlist:        f.Security.ExecAllowlist,
		URLAllowlist:         f.Security.URLAllowlist,
		Cooldown:             cooldown,
		ActionCooldowns:      actionCooldowns,
		Remote:               remote,
		Actions:              actions,
		Logging:              Logging{Level: f.Logging.Level, Format: f.Logging.Format},
		HTTP:                 httpCfg,
		Rules:                rules,
	}, nil
}

// validate rejects structurally inconsistent documents before anything is built.
func (f *fileConfig) validate() error {
	if f.Version != 0 && f.Version != supportedVersion {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, f.Version)
	}

	if len(f.Rules) > 0 && len(f.Server.Rules) > 0 {
		return ErrRulesConflict
	}

	return nil
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

		def, err := buildActionDef(entry, actionType)
		if err != nil {
			return nil, fmt.Errorf("actions[%d] (%s): %w", i, entry.Name, err)
		}

		actions[wol.Action(entry.Name)] = def
	}

	return actions, nil
}

func buildActionDef(entry actionConfig, actionType wol.ActionType) (wol.ActionDef, error) {
	def := wol.ActionDef{Name: wol.Action(entry.Name), Type: actionType}

	switch actionType {
	case wol.ActionTypeExec:
		if hasHTTPParams(entry) {
			return wol.ActionDef{}, fmt.Errorf("%w: http parameters on an exec action", ErrActionParams)
		}

		return buildExecDef(entry, def)
	case wol.ActionTypeHTTP:
		if hasActorParams(entry) {
			return wol.ActionDef{}, fmt.Errorf("%w: exec parameters on an http action", ErrActionParams)
		}

		return buildHTTPDef(entry, def)
	case wol.ActionTypeNoop, wol.ActionTypeSleep, wol.ActionTypeShutdown, wol.ActionTypeReboot:
		if hasExecParams(entry) {
			return wol.ActionDef{}, fmt.Errorf("%w: exec parameters on a %s action", ErrActionParams, actionType)
		}

		if hasHTTPParams(entry) {
			return wol.ActionDef{}, fmt.Errorf("%w: http parameters on a %s action", ErrActionParams, actionType)
		}

		return def, nil
	}

	return def, nil
}

// buildHTTPDef builds an outbound HTTP action (§18.2).
func buildHTTPDef(entry actionConfig, def wol.ActionDef) (wol.ActionDef, error) {
	if entry.URL == "" {
		return wol.ActionDef{}, ErrHTTPURLRequired
	}

	timeout, err := parseTimeout(entry.Timeout)
	if err != nil {
		return wol.ActionDef{}, err
	}

	def.HTTP = &wol.HTTPParams{
		Method:  entry.Method,
		URL:     entry.URL,
		Headers: entry.Headers,
		Body:    entry.Body,
		Timeout: timeout,
		Retries: entry.Retries,
	}

	return def, nil
}

func buildExecDef(entry actionConfig, def wol.ActionDef) (wol.ActionDef, error) {
	if len(entry.Command) == 0 {
		return wol.ActionDef{}, ErrExecCommandRequired
	}

	if entry.User != "" || entry.Group != "" {
		return wol.ActionDef{}, ErrExecUserUnsupported
	}

	timeout, err := parseTimeout(entry.Timeout)
	if err != nil {
		return wol.ActionDef{}, err
	}

	def.Exec = &wol.ExecParams{
		Command: entry.Command,
		Timeout: timeout,
		Workdir: entry.Workdir,
		Env:     entry.Env,
		Shell:   entry.Shell,
	}

	return def, nil
}

func hasExecParams(entry actionConfig) bool {
	return len(entry.Command) > 0 || entry.Timeout != "" || entry.Workdir != "" ||
		len(entry.Env) > 0 || entry.Shell || entry.User != "" || entry.Group != ""
}

// hasActorParams reports the exec-only parameters of an entry whose timeout is shared
// with the http action type.
func hasActorParams(entry actionConfig) bool {
	return len(entry.Command) > 0 || entry.Workdir != "" || len(entry.Env) > 0 ||
		entry.Shell || entry.User != "" || entry.Group != ""
}

// hasHTTPParams reports the outbound HTTP parameters of an entry.
func hasHTTPParams(entry actionConfig) bool {
	return entry.URL != "" || entry.Method != "" || len(entry.Headers) > 0 ||
		entry.Body != "" || entry.Retries != 0
}

func parseTimeout(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}

	timeout, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %w", ErrExecTimeout, value, err)
	}

	if timeout <= 0 {
		return 0, fmt.Errorf("%w: %q", ErrExecTimeout, value)
	}

	return timeout, nil
}

// buildRemoteCommands resolves the §21 remote command channel: the whitelisted
// commands, the UDP ports allowed to carry command segments and the shared HMAC key.
func buildRemoteCommands(cfg securityConfig, entries []commandConfig) (RemoteCommands, error) {
	remote := RemoteCommands{Enabled: cfg.AllowRemoteCommands, Commands: map[string]wol.RemoteCommand{}}

	for _, entry := range entries {
		cmd, err := buildRemoteCommand(entry)
		if err != nil {
			return RemoteCommands{}, err
		}

		if _, dup := remote.Commands[cmd.ID]; dup {
			return RemoteCommands{}, fmt.Errorf("%w: duplicate id %q", ErrRemoteCommandID, cmd.ID)
		}

		remote.Commands[cmd.ID] = cmd
	}

	if !cfg.AllowRemoteCommands {
		if len(cfg.RemoteCommandPorts) > 0 {
			return RemoteCommands{}, fmt.Errorf("%w: security.allow_remote_commands is false", ErrRemotePorts)
		}

		return remote, nil
	}

	if cfg.RemoteCommandAuth.Type != authTypeHMAC {
		return RemoteCommands{}, fmt.Errorf("%w: remote_command_auth.type must be %q", ErrRemoteAuth, authTypeHMAC)
	}

	key, err := resolveSecret(cfg.RemoteCommandAuth.KeyEnv, cfg.RemoteCommandAuth.KeyFile, "remote_command_auth.key")
	if err != nil {
		return RemoteCommands{}, fmt.Errorf("%w: %w", ErrRemoteAuth, err)
	}

	if len(remote.Commands) == 0 {
		return RemoteCommands{}, fmt.Errorf("%w: commands[] is empty", ErrRemoteCommandDef)
	}

	ports, err := remoteCommandPorts(cfg.RemoteCommandPorts, cfg.ReservedPorts)
	if err != nil {
		return RemoteCommands{}, err
	}

	remote.HMACKey = []byte(key)
	remote.Ports = ports

	return remote, nil
}

// buildRemoteCommand turns one commands[] entry into its whitelisted definition,
// reusing the exec parameter parsing (timeout, workdir, env, allowlist validation).
func buildRemoteCommand(entry commandConfig) (wol.RemoteCommand, error) {
	if !wol.ValidRemoteName(entry.ID) {
		return wol.RemoteCommand{}, fmt.Errorf("%w: %q", ErrRemoteCommandID, entry.ID)
	}

	if entry.Type != "" && entry.Type != string(wol.ActionTypeExec) {
		return wol.RemoteCommand{}, fmt.Errorf("%w: %s", ErrRemoteCommandType, entry.Type)
	}

	if err := buildArgSpecsCheck(entry.ID, entry.Args); err != nil {
		return wol.RemoteCommand{}, err
	}

	def, err := buildActionDef(actionConfig{
		Name:    string(wol.RemoteAction(entry.ID)),
		Type:    string(wol.ActionTypeExec),
		Command: entry.Command,
		Timeout: entry.Timeout,
		Workdir: entry.Workdir,
		Env:     entry.Env,
	}, wol.ActionTypeExec)
	if err != nil {
		return wol.RemoteCommand{}, fmt.Errorf("commands[%s]: %w", entry.ID, err)
	}

	args, err := buildArgSpecs(entry.Args)
	if err != nil {
		return wol.RemoteCommand{}, err
	}

	return wol.RemoteCommand{ID: entry.ID, Exec: *def.Exec, Args: args}, nil
}

// buildArgSpecs compiles the declared argument specs of a remote command.
func buildArgSpecs(entries map[string]argConfig) (map[string]wol.ArgSpec, error) {
	specs := make(map[string]wol.ArgSpec, len(entries))

	for name, cfg := range entries {
		spec := wol.ArgSpec{Type: cfg.Type, Enum: cfg.Enum, Required: true}

		if cfg.Required != nil {
			spec.Required = *cfg.Required
		}

		if cfg.Pattern != "" {
			pattern, err := regexp.Compile(cfg.Pattern)
			if err != nil {
				return nil, fmt.Errorf("%w: %s: %w", ErrRemoteArgSpec, name, err)
			}

			spec.Pattern = pattern
		}

		if err := validateArgSpec(name, spec); err != nil {
			return nil, err
		}

		specs[name] = spec
	}

	return specs, nil
}

// buildArgSpecsCheck validates the argument names before the command is built.
func buildArgSpecsCheck(id string, entries map[string]argConfig) error {
	for name := range entries {
		if !wol.ValidRemoteName(name) {
			return fmt.Errorf("%w: commands[%s]: argument %q", ErrRemoteArgSpec, id, name)
		}
	}

	return nil
}

func validateArgSpec(name string, spec wol.ArgSpec) error {
	switch spec.Type {
	case "", wol.ArgTypeString, wol.ArgTypeInt, wol.ArgTypeBool:
	default:
		return fmt.Errorf("%w: %s: unknown type %q", ErrRemoteArgSpec, name, spec.Type)
	}

	for _, enumValue := range spec.Enum {
		if err := spec.Validate(enumValue); err != nil {
			return fmt.Errorf("%w: %s: enum %q: %w", ErrRemoteArgSpec, name, enumValue, err)
		}
	}

	return nil
}

// remoteCommandPorts validates the UDP ports allowed to carry command segments.
func remoteCommandPorts(ports []int, reserved []int) ([]int, error) {
	if len(ports) == 0 {
		return nil, fmt.Errorf("%w: at least one port is required", ErrRemotePorts)
	}

	if len(reserved) == 0 {
		reserved = wol.DefaultReservedPorts()
	}

	out := make([]int, 0, len(ports))

	for _, port := range ports {
		switch {
		case port <= 0 || port > 65535:
			return nil, fmt.Errorf("%w: %d", ErrRemotePorts, port)
		case slices.Contains(reserved, port):
			return nil, fmt.Errorf("%w: %d", ErrRemotePort, port)
		case slices.Contains(out, port):
			return nil, fmt.Errorf("%w: duplicate %d", ErrRemotePorts, port)
		}

		out = append(out, port)
	}

	return out, nil
}

// buildAllRules expands the global rules and every interface block into one ordered list.
func buildAllRules(f *fileConfig, actions map[wol.Action]wol.ActionDef) ([]wol.Rule, error) {
	global, err := buildRules(globalRules(f), nil, false, actions)
	if err != nil {
		return nil, err
	}

	scoped, err := buildInterfaceRules(f.Server.Interfaces, actions)
	if err != nil {
		return nil, err
	}

	return slices.Concat(global, scoped), nil
}

// buildCooldowns resolves security.cooldown and its per-action overrides.
func buildCooldowns(cfg securityConfig, actions map[wol.Action]wol.ActionDef) (time.Duration, map[wol.Action]time.Duration, error) {
	global, err := parseCooldown(cfg.Cooldown, "security.cooldown")
	if err != nil {
		return 0, nil, err
	}

	if len(cfg.Cooldowns) == 0 {
		return global, nil, nil
	}

	perAction := make(map[wol.Action]time.Duration, len(cfg.Cooldowns))

	for name, value := range cfg.Cooldowns {
		action := wol.Action(name)

		if _, ok := actions[action]; !ok {
			return 0, nil, fmt.Errorf("security.cooldowns: %w: %s", wol.ErrUnknownActionRef, name)
		}

		window, parseErr := parseCooldown(value, "security.cooldowns."+name)
		if parseErr != nil {
			return 0, nil, parseErr
		}

		if window <= 0 {
			return 0, nil, fmt.Errorf("%w: security.cooldowns.%s: must be positive", ErrCooldown, name)
		}

		perAction[action] = window
	}

	return global, perAction, nil
}

func parseCooldown(value string, field string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}

	window, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %q: %w", ErrCooldown, field, value, err)
	}

	if window < 0 {
		return 0, fmt.Errorf("%w: %s: %q", ErrCooldown, field, value)
	}

	return window, nil
}

func buildHTTP(cfg httpConfig) (HTTP, error) {
	httpCfg := HTTP{
		Enabled:      cfg.Enabled,
		Listen:       cfg.Listen,
		AuthType:     cfg.Auth.Type,
		User:         cfg.Auth.User,
		CertFile:     cfg.TLS.CertFile,
		KeyFile:      cfg.TLS.KeyFile,
		ClientCAFile: cfg.TLS.ClientCAFile,
	}

	if (httpCfg.CertFile == "") != (httpCfg.KeyFile == "") {
		return HTTP{}, fmt.Errorf("%w: cert_file and key_file must be set together", ErrHTTPTLS)
	}

	if !cfg.Enabled {
		return httpCfg, nil
	}

	if httpCfg.Listen == "" {
		httpCfg.Listen = defaultHTTPListen
	}

	if _, _, err := net.SplitHostPort(httpCfg.Listen); err != nil {
		return HTTP{}, fmt.Errorf("%w: %q: %w", ErrHTTPListen, cfg.Listen, err)
	}

	if httpCfg.AuthType == "" {
		httpCfg.AuthType = authTypeBearer
	}

	if err := resolveHTTPAuth(&httpCfg, cfg.Auth); err != nil {
		return HTTP{}, err
	}

	return httpCfg, nil
}

func resolveHTTPAuth(out *HTTP, cfg authConfig) error {
	switch out.AuthType {
	case authTypeBearer:
		token, err := resolveSecret(cfg.TokenEnv, cfg.TokenFile, "token")
		if err != nil {
			return err
		}

		out.Token = token
	case authTypeBasic:
		if cfg.User == "" {
			return ErrHTTPUser
		}

		password, err := resolveSecret(cfg.PasswordEnv, cfg.PasswordFile, "password")
		if err != nil {
			return err
		}

		out.Password = password
	case authTypeMTLS:
		if out.ClientCAFile == "" {
			return fmt.Errorf("%w: auth type mtls requires tls.client_ca_file", ErrHTTPTLS)
		}
	default:
		return fmt.Errorf("%w: %q (bearer | basic | mtls)", ErrHTTPAuthType, out.AuthType)
	}

	return nil
}

// resolveSecret reads a secret from an environment variable or a 0600 file; never from YAML.
func resolveSecret(envName string, fileName string, label string) (string, error) {
	switch {
	case envName != "" && fileName != "":
		return "", fmt.Errorf("%w: %s: set the env variable or the file, not both", ErrHTTPSecret, label)
	case envName != "":
		value := os.Getenv(envName)
		if value == "" {
			return "", fmt.Errorf("%w: %s: environment variable %s is empty", ErrHTTPSecret, label, envName)
		}

		return value, nil
	case fileName != "":
		return readSecretFile(fileName, label)
	default:
		return "", fmt.Errorf("%w: %s: no environment variable or file configured", ErrHTTPSecret, label)
	}
}

func readSecretFile(name string, label string) (string, error) {
	info, err := os.Stat(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrHTTPSecret, label, err)
	}

	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%w: %s: %s must not be group/other readable (chmod 600)", ErrHTTPSecret, label, name)
	}

	data, err := os.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrHTTPSecret, label, err)
	}

	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("%w: %s: %s is empty", ErrHTTPSecret, label, name)
	}

	return value, nil
}

func parseActionType(value string) (wol.ActionType, error) {
	switch wol.ActionType(value) {
	case wol.ActionTypeNoop, wol.ActionTypeShutdown, wol.ActionTypeReboot, wol.ActionTypeSleep,
		wol.ActionTypeExec, wol.ActionTypeHTTP:
		return wol.ActionType(value), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownActionType, value)
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

	if err := checkBlockScope(entry.Match.Interfaces, scope); err != nil {
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

// checkBlockScope rejects a rule inside a server.interfaces block that re-scopes itself to
// another interface (§8: ErrInterfaceScopeConflict).
func checkBlockScope(interfaces []string, scope *wol.MACSelector) error {
	if scope == nil || len(interfaces) == 0 {
		return nil
	}

	same := len(interfaces) == len(scope.Ifaces)

	for _, name := range interfaces {
		if !slices.Contains(scope.Ifaces, name) {
			same = false

			break
		}
	}

	if !same {
		return fmt.Errorf("%w: block %v vs match.interfaces %v", wol.ErrInterfaceScopeConflict, scope.Ifaces, interfaces)
	}

	return nil
}

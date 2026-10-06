package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
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
	ErrEnvValue            = errors.New("invalid environment override")
	ErrMissingEnvVar       = errors.New("environment variable referenced by the config is not set")
	ErrActionRequired      = errors.New("rule requires an action")
	ErrInterfaceDryRun     = errors.New("dry_run on an interface block requires block-level rules")
	ErrActionNameRequired  = errors.New("actions[] entry requires a name")
	ErrExecCommandRequired = errors.New("exec action requires command")
	ErrSendMAC             = errors.New("invalid wol.send mac")
	ErrSendBroadcast       = errors.New("invalid wol.send broadcast address")
	ErrSendPort            = errors.New("invalid wol.send port")
	ErrSendRepeat          = errors.New("invalid wol.send repeat")
	ErrSendInterval        = errors.New("invalid wol.send interval")
	ErrExecTimeout         = errors.New("invalid exec timeout")
	ErrActionParams        = errors.New("action parameters do not match its type")
	ErrHTTPListen          = errors.New("invalid server.http.listen address")
	ErrHTTPAuthType        = errors.New("unknown server.http.auth.type")
	ErrSecret              = errors.New("cannot resolve a configured secret")
	ErrHTTPUser            = errors.New("server.http.auth.user is required for basic auth")
	ErrHTTPTLS             = errors.New("invalid server.http.tls configuration")
	ErrCooldown            = errors.New("invalid security.cooldown")
	ErrRateLimit           = errors.New("invalid security.rate_limit")
	ErrRemoteCommandID     = errors.New("invalid remote command id")
	ErrRemoteCommandType   = errors.New("unsupported remote command type")
	ErrRemoteCommandDef    = errors.New("invalid remote command definition")
	ErrRemoteArgSpec       = errors.New("invalid remote command argument spec")
	ErrRemoteAuth          = errors.New("remote command authorization is required")
	ErrPacketAuth          = errors.New("packet authorization is invalid")
	ErrRawShell            = errors.New("raw shell configuration is invalid")
	// ErrLogOutput reports a logging section that names no usable destination (§18).
	ErrLogOutput             = errors.New("logging output is invalid")
	ErrRemotePorts           = errors.New("invalid security.remote_command_ports")
	ErrRemotePort            = errors.New("remote command port must not be a reserved port")
	ErrSequenceStepsRequired = errors.New("sequence action requires steps")
	ErrWatchInterval         = errors.New("invalid server.watch interval")
	ErrHTTPURLRequired       = errors.New("http action requires url")
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
	EnvLogOutput                = "SOL_LOG_OUTPUT"
	EnvLogFile                  = "SOL_LOG_FILE"
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

	// Last, so the file, the environment and the defaults are judged as one resolved value.
	if err := validateLogging(cfg.Logging); err != nil {
		return nil, err
	}

	return cfg, nil
}

// loggingFrom maps the file-level logging section onto the resolved configuration: the two types
// carry the same fields, so this is a conversion rather than a field-by-field copy.
func loggingFrom(f loggingConfig) Logging {
	return Logging(f)
}

// validateLogging checks the logging section (§18): the output names a destination, and only a
// file output carries a path. Both mistakes are start-up errors instead of a surprise at the
// first audit line.
func validateLogging(logging Logging) error {
	switch strings.ToLower(strings.TrimSpace(logging.Output)) {
	case "", loggingOutputStderr, loggingOutputStdout:
		if strings.TrimSpace(logging.File) != "" {
			return fmt.Errorf("%w: logging.file is only used with output: %s",
				ErrLogOutput, loggingOutputFile)
		}
	case loggingOutputFile:
		if strings.TrimSpace(logging.File) == "" {
			return fmt.Errorf("%w: output: %s needs logging.file", ErrLogOutput, loggingOutputFile)
		}
	default:
		return fmt.Errorf("%w: %q (want %s|%s|%s)", ErrLogOutput, logging.Output,
			loggingOutputStderr, loggingOutputStdout, loggingOutputFile)
	}

	return nil
}

// ResolvePath returns the configuration file Load would read for path: an explicit path
// wins, then $SOL_CONFIG, then the default locations; it is empty when there is no file.
// The automatic reload watches this file.
func ResolvePath(path string) string {
	return resolvePath(path)
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

// minWatch keeps the poll interval sane: watching a config file must stay cheaper than the
// work it saves.
const minWatch = time.Second

const (
	// maxSendPort is the highest usable UDP port.
	maxSendPort = 65535
	// sendDefaultBroadcast is the limited broadcast address used when wol.send sets none.
	sendDefaultBroadcast = "255.255.255.255"
	// rawShellPlaceholder stands in for the per-packet command while the raw shell settings
	// are validated at load time (§21.6).
	rawShellPlaceholder = "true"
)

// parseWatch reads server.watch. Empty and "0" disable watching; anything below minWatch is
// rejected, so a typo ("50" meaning 50ms?) fails the start-up instead of polling hot.
func parseWatch(value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}

	interval, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %w", ErrWatchInterval, value, err)
	}

	if interval == 0 {
		return 0, nil
	}

	if interval < minWatch {
		return 0, fmt.Errorf("%w: %q (minimum %s, empty disables it)", ErrWatchInterval, value, minWatch)
	}

	return interval, nil
}

func defaults() *Config {
	return &Config{Actions: wol.BuiltinActions()}
}

// expandEnv substitutes $VAR and ${VAR} using the process environment and fails loudly when a
// referenced variable is missing.
//
// Substitution is per line and skips YAML comments, because a commented-out line must never be
// able to make a variable required (the README example alone would fail to load otherwise).
// Block scalar bodies (| and >) are data even when a line in them looks like a comment, so they
// are expanded as a whole.
func expandEnv(text string) (string, error) {
	var (
		missing []string
		out     strings.Builder
		block   int // indentation of the block scalar whose body we are inside, -1 when outside
	)

	block = -1

	for line := range strings.Lines(text) {
		plain := plainData(line, block)
		block = plain.block

		out.WriteString(os.Expand(plain.data, func(key string) string {
			value, ok := os.LookupEnv(key)
			if !ok {
				missing = append(missing, key)

				return ""
			}

			return value
		}))
		out.WriteString(plain.comment)
	}

	if len(missing) > 0 {
		return "", fmt.Errorf("%w: %s", ErrMissingEnvVar, strings.Join(missing, ", "))
	}

	return out.String(), nil
}

// lineParts is a YAML line split into the part that carries data (and is therefore subject to
// substitution) and the comment that must be left alone.
type lineParts struct {
	data    string
	comment string
	block   int // indentation of the block scalar the next line belongs to, -1 when outside
}

// plainData decides which part of a line is data. A '#' opens a comment at the start of a line
// or after whitespace, unless it sits inside a quoted scalar; inside a block scalar every line
// is data.
func plainData(line string, block int) lineParts {
	indent := len(line) - len(strings.TrimLeft(line, " 	"))

	if block >= 0 {
		// Inside a block scalar: only a non-blank line that is not indented deeper than the
		// header ends it.
		if strings.TrimSpace(line) == "" || indent > block {
			return lineParts{data: line, block: block}
		}
	}

	data, comment := cutComment(line)

	return lineParts{data: data, comment: comment, block: blockAfter(data, indent)}
}

// cutComment splits a line at the '#' that starts a YAML comment.
func cutComment(line string) (string, string) {
	var quote byte

	for i := range len(line) {
		ch := line[i]

		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '\'' || ch == '"':
			quote = ch
		case ch == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '	'):
			return line[:i], line[i:]
		}
	}

	return line, ""
}

// blockAfter reports the indentation of the block scalar that starts on this line, or -1.
func blockAfter(data string, indent int) int {
	trimmed := strings.TrimRight(strings.TrimSpace(data), " 	")
	if trimmed == "" || trimmed[0] == '#' {
		return -1
	}

	// A block scalar header ends with '|' or '>' plus optional chomping/indent indicators.
	_, header, ok := strings.Cut(strings.TrimSpace(data), ":")
	if !ok {
		return -1
	}

	header = strings.TrimSpace(header)
	if strings.HasPrefix(header, "|") || strings.HasPrefix(header, ">") {
		return indent
	}

	return -1
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

	if value, ok := os.LookupEnv(EnvLogOutput); ok {
		cfg.Logging.Output = value
	}

	if value, ok := os.LookupEnv(EnvLogFile); ok {
		cfg.Logging.File = value
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

	guards, err := buildGuards(f.Security, actions)
	if err != nil {
		return nil, err
	}

	remote, err := buildRemote(f)
	if err != nil {
		return nil, err
	}

	watch, err := parseWatch(f.Server.Watch)
	if err != nil {
		return nil, err
	}

	return &Config{
		Watch:                watch,
		RateLimit:            guards.rateLimit,
		RateBurst:            guards.rateBurst,
		InterfaceNames:       names,
		DryRun:               f.Security.DryRun,
		AllowReservedActions: f.Security.AllowReservedPortActions,
		ReservedPorts:        f.Security.ReservedPorts,
		SecureOn:             secureOnBytes(f.Security.SecureOn),
		ExecAllowlist:        f.Security.ExecAllowlist,
		URLAllowlist:         f.Security.URLAllowlist,
		Cooldown:             guards.cooldown,
		ActionCooldowns:      guards.perAction,
		PacketKey:            guards.packetKey,
		PacketWindow:         guards.packetWindow,
		Remote:               remote,
		Actions:              actions,
		Logging:              loggingFrom(f.Logging),
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

	if err := rejectForeignParams(entry, actionType); err != nil {
		return wol.ActionDef{}, err
	}

	switch actionType {
	case wol.ActionTypeExec:
		return buildExecDef(entry, def)
	case wol.ActionTypeHTTP:
		return buildHTTPDef(entry, def)
	case wol.ActionTypeSequence:
		return buildSequenceDef(entry, def)
	case wol.ActionTypeSend:
		return buildSendDef(entry, def)
	case wol.ActionTypeNoop, wol.ActionTypeSleep, wol.ActionTypeShutdown, wol.ActionTypeReboot:
		return def, nil
	}

	return def, nil
}

// paramGroups is which parameter groups an action entry carries, and which ones its type may
// carry. Keeping both in one place means a new type cannot silently swallow another type's
// fields (§19.4/§19.13), and a stray "timeout" on a sequence is caught instead of ignored.
type paramGroups struct {
	exec    bool
	http    bool
	steps   bool
	send    bool
	timeout bool
}

// groups reports the parameter groups an entry carries. Timeout is tracked on its own because
// exec and http share it.
func (entry actionConfig) groups() paramGroups {
	return paramGroups{
		exec:    hasActorParams(entry),
		http:    hasHTTPParams(entry),
		steps:   len(entry.Steps) > 0,
		send:    hasSendParams(entry),
		timeout: entry.Timeout != "",
	}
}

// allowedParams reports the parameter groups an action type may carry.
func allowedParams(actionType wol.ActionType) paramGroups {
	switch actionType {
	case wol.ActionTypeExec:
		return paramGroups{exec: true, timeout: true}
	case wol.ActionTypeHTTP:
		return paramGroups{http: true, timeout: true}
	case wol.ActionTypeSequence:
		return paramGroups{steps: true}
	case wol.ActionTypeSend:
		return paramGroups{send: true}
	case wol.ActionTypeNoop, wol.ActionTypeSleep, wol.ActionTypeShutdown, wol.ActionTypeReboot:
		// The built-in power actions take no parameters at all.
		return paramGroups{}
	}

	return paramGroups{}
}

// rejectForeignParams refuses the parameters that belong to another action type.
func rejectForeignParams(entry actionConfig, actionType wol.ActionType) error {
	present, allowed := entry.groups(), allowedParams(actionType)

	for _, group := range []struct {
		name    string
		present bool
		allowed bool
	}{
		{"exec", present.exec, allowed.exec},
		{"http", present.http, allowed.http},
		{"steps", present.steps, allowed.steps},
		{"wol.send", present.send, allowed.send},
		{"timeout", present.timeout, allowed.timeout},
	} {
		if group.present && !group.allowed {
			return fmt.Errorf("%w: %s parameters on a %s action", ErrActionParams, group.name, actionType)
		}
	}

	return nil
}

// buildSendDef builds a wol.send action (§19.13): the target is fixed here, and the defaults
// (limited broadcast, port 9, one copy, 100ms apart) are applied at load time so the executor
// never has to guess.
func buildSendDef(entry actionConfig, def wol.ActionDef) (wol.ActionDef, error) {
	mac, err := parseSendMAC(entry.MAC)
	if err != nil {
		return wol.ActionDef{}, err
	}

	broadcast, err := parseSendBroadcast(entry.Broadcast)
	if err != nil {
		return wol.ActionDef{}, err
	}

	port, err := parseSendPort(entry.Port)
	if err != nil {
		return wol.ActionDef{}, err
	}

	repeat, err := parseSendRepeat(entry.Repeat)
	if err != nil {
		return wol.ActionDef{}, err
	}

	interval, err := parseSendInterval(entry.Interval)
	if err != nil {
		return wol.ActionDef{}, err
	}

	if len(entry.SecureOn) != 0 && len(entry.SecureOn) != wol.SecureOnSize {
		return wol.ActionDef{}, fmt.Errorf("%w: got %d bytes", wol.ErrSecureOnLength, len(entry.SecureOn))
	}

	def.Send = &wol.SendParams{
		MAC:       mac,
		Broadcast: broadcast,
		Port:      port,
		SecureOn:  secureOnBytes(entry.SecureOn),
		Repeat:    repeat,
		Interval:  interval,
		Sign:      entry.Sign,
	}

	return def, nil
}

// parseSendMAC reads the target MAC of a wol.send action.
func parseSendMAC(value string) (net.HardwareAddr, error) {
	mac, err := net.ParseMAC(value)
	if err != nil || len(mac) != wol.MACSize {
		return nil, fmt.Errorf("%w: %q", ErrSendMAC, value)
	}

	return mac, nil
}

// parseSendBroadcast reads the destination; empty means the limited broadcast.
func parseSendBroadcast(value string) (string, error) {
	if value == "" {
		return sendDefaultBroadcast, nil
	}

	if _, err := netip.ParseAddr(value); err != nil {
		return "", fmt.Errorf("%w: %q", ErrSendBroadcast, value)
	}

	return value, nil
}

// parseSendPort reads the destination port; zero means the WOL default.
func parseSendPort(value int) (int, error) {
	if value == 0 {
		return wol.PortDefault, nil
	}

	if value < 1 || value > maxSendPort {
		return 0, fmt.Errorf("%w: %d", ErrSendPort, value)
	}

	return value, nil
}

// parseSendRepeat reads the copy count; zero means one.
func parseSendRepeat(value int) (int, error) {
	if value == 0 {
		return 1, nil
	}

	if value < 1 || value > wol.SendMaxRepeat {
		return 0, fmt.Errorf("%w: %d (1..%d)", ErrSendRepeat, value, wol.SendMaxRepeat)
	}

	return value, nil
}

// parseSendInterval reads a wol.send interval; empty means the default gap.
func parseSendInterval(value string) (time.Duration, error) {
	if value == "" {
		return wol.SendDefaultInterval, nil
	}

	interval, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %w", ErrSendInterval, value, err)
	}

	if interval < 0 || interval > wol.SendMaxInterval {
		return 0, fmt.Errorf("%w: %s (0..%s)", ErrSendInterval, interval, wol.SendMaxInterval)
	}

	return interval, nil
}

// hasSendParams reports the wol.send parameters of an entry.
func hasSendParams(entry actionConfig) bool {
	return entry.MAC != "" || entry.Broadcast != "" || entry.Port != 0 ||
		entry.SecureOn != "" || entry.Repeat != 0 || entry.Interval != ""
}

// buildSequenceDef builds an ordered action list (§18); the steps are validated
// against the registry at startup.
func buildSequenceDef(entry actionConfig, def wol.ActionDef) (wol.ActionDef, error) {
	if len(entry.Steps) == 0 {
		return wol.ActionDef{}, ErrSequenceStepsRequired
	}

	steps := make([]wol.Action, 0, len(entry.Steps))

	for _, step := range entry.Steps {
		name := wol.Action(strings.TrimSpace(step))
		if name == "" {
			return wol.ActionDef{}, ErrSequenceStepsRequired
		}

		steps = append(steps, name)
	}

	def.Sequence = &wol.SequenceParams{Steps: steps}

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
		User:    entry.User,
		Group:   entry.Group,
	}

	return def, nil
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

// buildPacketAuth resolves security.packet_auth (§19.14). An empty block means "off": no
// packet is authenticated. A configured block must be hmac and must name a key source.
func buildPacketAuth(cfg packetAuthConfig) ([]byte, error) {
	if cfg.Type == "" && cfg.KeyEnv == "" && cfg.KeyFile == "" && cfg.Window == "" {
		return nil, nil
	}

	if cfg.Type != authTypeHMAC {
		return nil, fmt.Errorf("%w: packet_auth.type must be %q", ErrPacketAuth, authTypeHMAC)
	}

	key, err := resolveSecret(cfg.KeyEnv, cfg.KeyFile, "packet_auth.key")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPacketAuth, err)
	}

	if key == "" {
		return nil, fmt.Errorf("%w: packet_auth.key is empty", ErrPacketAuth)
	}

	return []byte(key), nil
}

// buildPacketWindow reads packet_auth.window (§19.16). A window needs the key it belongs to, and
// a zero or negative one is refused: it would read like protection while accepting every stamp.
func buildPacketWindow(cfg packetAuthConfig, key []byte) (time.Duration, error) {
	if cfg.Window == "" {
		return 0, nil
	}

	if len(key) == 0 {
		return 0, fmt.Errorf("%w: packet_auth.window requires a key", ErrPacketAuth)
	}

	window, err := time.ParseDuration(cfg.Window)
	if err != nil {
		return 0, fmt.Errorf("%w: packet_auth.window: %q: %w", ErrPacketAuth, cfg.Window, err)
	}

	if window <= 0 {
		return 0, fmt.Errorf("%w: packet_auth.window must be positive, got %s", ErrPacketAuth, cfg.Window)
	}

	return window, nil
}

// requireCommands refuses an enabled channel that would accept nothing. A raw shell-only
// deployment is the exception (§21.6): that transport carries its own commands, so an empty
// commands[] is not a mistake there.
func requireCommands(cfg securityConfig, commands map[string]wol.RemoteCommand) error {
	if len(commands) == 0 && !cfg.AllowRawShell {
		return fmt.Errorf("%w: commands[] is empty", ErrRemoteCommandDef)
	}

	return nil
}

// buildRemote resolves the §21 remote command channel together with the raw shell transport that
// rides on it (§21.6): the raw shell key is refused without the channel it belongs to.
func buildRemote(f *fileConfig) (RemoteCommands, error) {
	remote, err := buildRemoteCommands(f.Security, f.Commands)
	if err != nil {
		return RemoteCommands{}, err
	}

	rawShell, err := buildRawShell(f.Security, remote)
	if err != nil {
		return RemoteCommands{}, err
	}

	remote.RawShell = rawShell

	return remote, nil
}

// buildAuthWindow reads the replay window of one authenticated channel (§21.3). A window needs
// the key it belongs to, and a zero or negative one is refused: it would read like protection
// while accepting every stamp.
func buildAuthWindow(auth remoteAuthConfig, key []byte, label string, sentinel error) (time.Duration, error) {
	if auth.Window == "" {
		return 0, nil
	}

	if len(key) == 0 {
		return 0, fmt.Errorf("%w: %s.window requires a key", sentinel, label)
	}

	window, err := time.ParseDuration(auth.Window)
	if err != nil {
		return 0, fmt.Errorf("%w: %s.window: %q: %w", sentinel, label, auth.Window, err)
	}

	if window <= 0 {
		return 0, fmt.Errorf("%w: %s.window must be positive, got %s", sentinel, label, auth.Window)
	}

	return window, nil
}

// buildRawShell resolves security.allow_raw_shell and its companion keys (§21.6). Enabling it
// requires the remote channel to be on, a dedicated non-reserved port set and its own HMAC key;
// a companion key without allow_raw_shell is a mistake, not a no-op.
func buildRawShell(cfg securityConfig, remote RemoteCommands) (RawShell, error) {
	if !cfg.AllowRawShell {
		return RawShell{}, rejectRawShellKeys(cfg)
	}

	if !cfg.AllowRemoteCommands {
		return RawShell{}, fmt.Errorf("%w: allow_raw_shell requires security.allow_remote_commands", ErrRawShell)
	}

	if cfg.RawShellAuth.Type != authTypeHMAC {
		return RawShell{}, fmt.Errorf("%w: raw_shell_auth.type must be %q", ErrRawShell, authTypeHMAC)
	}

	key, err := resolveSecret(cfg.RawShellAuth.KeyEnv, cfg.RawShellAuth.KeyFile, "raw_shell_auth.key")
	if err != nil {
		return RawShell{}, fmt.Errorf("%w: %w", ErrRawShell, err)
	}

	ports, err := rawShellPorts(cfg, remote)
	if err != nil {
		return RawShell{}, err
	}

	srcNets, err := wol.ParseCIDRs(cfg.RawShellSrcCIDRs)
	if err != nil {
		return RawShell{}, fmt.Errorf("%w: raw_shell_src_cidrs: %w", ErrRawShell, err)
	}

	allowlist, err := compileShellAllowlist(cfg.RawShellAllowlist)
	if err != nil {
		return RawShell{}, err
	}

	window, err := buildAuthWindow(cfg.RawShellAuth, []byte(key), "raw_shell_auth", ErrRawShell)
	if err != nil {
		return RawShell{}, err
	}

	exec, err := buildRawShellExec(cfg)
	if err != nil {
		return RawShell{}, fmt.Errorf("%w: %w", ErrRawShell, err)
	}

	return RawShell{
		Enabled:   true,
		Window:    window,
		Ports:     ports,
		Key:       []byte(key),
		SrcNets:   srcNets,
		Allowlist: allowlist,
		Exec:      exec,
	}, nil
}

// rejectRawShellKeys reports the companion keys of an opt-in that is off. Half-configured
// settings are a mistake: silently ignoring them would leave a port bound and a key read for a
// channel the operator believes is disabled.
func rejectRawShellKeys(cfg securityConfig) error {
	configured := len(cfg.RawShellPorts) > 0 || cfg.RawShellAuth.Type != "" ||
		cfg.RawShellAuth.KeyEnv != "" || cfg.RawShellAuth.KeyFile != "" ||
		len(cfg.RawShellAllowlist) > 0 || len(cfg.RawShellSrcCIDRs) > 0 || cfg.RawShellAuth.Window != ""

	if configured {
		return fmt.Errorf("%w: raw_shell_* keys are set without security.allow_raw_shell", ErrRawShell)
	}

	return nil
}

// rawShellPorts resolves the dedicated port set: at least one port, none reserved and none
// shared with the remote command channel, so a packet's meaning never depends on which of the
// two channels won the race.
func rawShellPorts(cfg securityConfig, remote RemoteCommands) ([]int, error) {
	ports, err := remoteCommandPorts(cfg.RawShellPorts, cfg.ReservedPorts)
	if err != nil {
		return nil, fmt.Errorf("%w: raw_shell_ports: %w", ErrRawShell, err)
	}

	for _, port := range ports {
		if slices.Contains(remote.Ports, port) {
			return nil, fmt.Errorf("%w: port %d is also security.remote_command_ports", ErrRawShell, port)
		}
	}

	return ports, nil
}

// compileShellAllowlist turns the raw shell allowlist into full-match patterns: an entry is
// anchored at both ends, so "^/usr/local/bin/mark\\.sh$" behaves as written and a bare
// "systemctl" matches only that exact command line.
func compileShellAllowlist(entries []string) ([]*regexp.Regexp, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	compiled := make([]*regexp.Regexp, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry) == "" {
			return nil, fmt.Errorf("%w: empty allowlist entry", ErrRawShell)
		}

		pattern, err := regexp.Compile("^(?:" + entry + ")$")
		if err != nil {
			return nil, fmt.Errorf("%w: %q: %w", ErrRawShell, entry, err)
		}

		compiled = append(compiled, pattern)
	}

	return compiled, nil
}

// buildRawShellExec reuses the exec parameter parsing for the settings that apply to every raw
// shell command. The command itself is per packet, so a placeholder is used here and replaced at
// execution time; shell mode is always on, which is the whole point of the channel.
func buildRawShellExec(cfg securityConfig) (wol.ExecParams, error) {
	def, err := buildExecDef(actionConfig{
		Name:    string(wol.RawShellAction),
		Type:    string(wol.ActionTypeExec),
		Command: []string{rawShellPlaceholder},
		Timeout: cfg.RawShellTimeout,
		Shell:   true,
		User:    cfg.RawShellUser,
		Group:   cfg.RawShellGroup,
	}, wol.ActionDef{Name: wol.RawShellAction, Type: wol.ActionTypeExec})
	if err != nil {
		return wol.ExecParams{}, err
	}

	return *def.Exec, nil
}

// remoteChannelKey resolves the command channel's HMAC key and its optional replay window. They
// are read together because a window is meaningless without the key it binds (§21.3).
func remoteChannelKey(cfg securityConfig) (string, time.Duration, error) {
	key, err := resolveSecret(cfg.RemoteCommandAuth.KeyEnv, cfg.RemoteCommandAuth.KeyFile, "remote_command_auth.key")
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrRemoteAuth, err)
	}

	window, err := buildAuthWindow(cfg.RemoteCommandAuth, []byte(key), "remote_command_auth", ErrRemoteAuth)
	if err != nil {
		return "", 0, err
	}

	return key, window, nil
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

	key, window, err := remoteChannelKey(cfg)
	if err != nil {
		return RemoteCommands{}, err
	}

	if err := requireCommands(cfg, remote.Commands); err != nil {
		return RemoteCommands{}, err
	}

	remote.Window = window

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
		User:    entry.User,
		Group:   entry.Group,
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
	global, err := buildRules(globalRules(f), nil, ruleDefaults{}, actions)
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

// guardsConfig is the resolved set of execution guardrails: the per-action cooldowns (§19.6)
// and the global token bucket (§19.12).
type guardsConfig struct {
	cooldown     time.Duration
	perAction    map[wol.Action]time.Duration
	rateLimit    float64
	rateBurst    int
	packetKey    []byte
	packetWindow time.Duration
}

// buildGuards resolves every guardrail in one step, so the caller stays short.
func buildGuards(cfg securityConfig, actions map[wol.Action]wol.ActionDef) (guardsConfig, error) {
	cooldown, perAction, err := buildCooldowns(cfg, actions)
	if err != nil {
		return guardsConfig{}, err
	}

	rateLimit, rateBurst, err := buildRateLimit(cfg)
	if err != nil {
		return guardsConfig{}, err
	}

	packetKey, err := buildPacketAuth(cfg.PacketAuth)
	if err != nil {
		return guardsConfig{}, err
	}

	packetWindow, err := buildPacketWindow(cfg.PacketAuth, packetKey)
	if err != nil {
		return guardsConfig{}, err
	}

	return guardsConfig{
		cooldown:     cooldown,
		perAction:    perAction,
		rateLimit:    rateLimit,
		rateBurst:    rateBurst,
		packetKey:    packetKey,
		packetWindow: packetWindow,
	}, nil
}

// buildRateLimit resolves security.rate_limit and its bucket size. An empty or zero rate
// disables the guard; a burst without a rate is a configuration mistake, not a no-op.
func buildRateLimit(cfg securityConfig) (float64, int, error) {
	value := strings.TrimSpace(cfg.RateLimit)
	if value == "" || value == "0" {
		if cfg.RateBurst != 0 {
			return 0, 0, fmt.Errorf("%w: security.rate_burst is set without a security.rate_limit", ErrRateLimit)
		}

		return 0, 0, nil
	}

	rate, err := parseRate(value)
	if err != nil {
		return 0, 0, err
	}

	if cfg.RateBurst < 0 {
		return 0, 0, fmt.Errorf("%w: security.rate_burst: %d: must not be negative", ErrRateLimit, cfg.RateBurst)
	}

	return rate, cfg.RateBurst, nil
}

// parseRate reads a "<n>/s|m|h" rate, or a bare "<n>" meaning per second.
func parseRate(value string) (float64, error) {
	number, unit, hasUnit := strings.Cut(value, "/")

	count, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
	if err != nil || count <= 0 {
		return 0, fmt.Errorf("%w: %q: want a positive number, e.g. 10/s, 600/m or 3600/h", ErrRateLimit, value)
	}

	if !hasUnit {
		return count, nil
	}

	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "s", "sec", "second":
		return count, nil
	case "m", "min", "minute":
		return count / time.Minute.Seconds(), nil
	case "h", "hour":
		return count / time.Hour.Seconds(), nil
	default:
		return 0, fmt.Errorf("%w: %q: unit must be s, m or h", ErrRateLimit, value)
	}
}

// parseCooldown reads a security.cooldown value.
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
		return "", fmt.Errorf("%w: %s: set the env variable or the file, not both", ErrSecret, label)
	case envName != "":
		value := os.Getenv(envName)
		if value == "" {
			return "", fmt.Errorf("%w: %s: environment variable %s is empty", ErrSecret, label, envName)
		}

		return value, nil
	case fileName != "":
		return readSecretFile(fileName, label)
	default:
		return "", fmt.Errorf("%w: %s: no environment variable or file configured", ErrSecret, label)
	}
}

func readSecretFile(name string, label string) (string, error) {
	info, err := os.Stat(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrSecret, label, err)
	}

	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%w: %s: %s must not be group/other readable (chmod 600)", ErrSecret, label, name)
	}

	data, err := os.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrSecret, label, err)
	}

	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("%w: %s: %s is empty", ErrSecret, label, name)
	}

	return value, nil
}

func parseActionType(value string) (wol.ActionType, error) {
	switch wol.ActionType(value) {
	case wol.ActionTypeNoop, wol.ActionTypeShutdown, wol.ActionTypeReboot, wol.ActionTypeSleep,
		wol.ActionTypeExec, wol.ActionTypeHTTP, wol.ActionTypeSequence, wol.ActionTypeSend:
		return wol.ActionType(value), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownActionType, value)
	}
}

func buildInterfaceNames(entries []ifaceConfig) ([]string, error) {
	names := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))

	for _, entry := range entries {
		if seen[entry.Name] {
			return nil, fmt.Errorf("%w: %s", wol.ErrDuplicateInterface, entry.Name)
		}

		seen[entry.Name] = true

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

		built, err := buildRules(entry.Rules, &scope, ruleDefaults{dryRun: blockDryRun, secureOn: entry.SecureOn}, actions)
		if err != nil {
			return nil, fmt.Errorf("server.interfaces[%d] (%s): %w", i, entry.Name, err)
		}

		rules = append(rules, built...)
	}

	return rules, nil
}

// ruleDefaults are the values a scope hands down to the rules it holds: an interface block can
// set both, the global rule list sets neither.
type ruleDefaults struct {
	dryRun   bool
	secureOn *string
}

func buildRules(
	entries []ruleConfig,
	scope *wol.MACSelector,
	defaults ruleDefaults,
	actions map[wol.Action]wol.ActionDef,
) ([]wol.Rule, error) {
	rules := make([]wol.Rule, 0, len(entries))

	for i, entry := range entries {
		rule, err := buildRule(entry, scope, defaults, actions)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", i+1, err)
		}

		rules = append(rules, rule)
	}

	return rules, nil
}

func buildRule(entry ruleConfig, scope *wol.MACSelector, defaults ruleDefaults, actions map[wol.Action]wol.ActionDef) (wol.Rule, error) {
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

	if err := checkMatchAuth(entry.Match.Auth); err != nil {
		return wol.Rule{}, err
	}

	mac, err := buildMAC(entry.Match, scope)
	if err != nil {
		return wol.Rule{}, err
	}

	ruleDryRun := defaults.dryRun
	if entry.DryRun != nil {
		ruleDryRun = *entry.DryRun
	}

	secureOn, err := buildSecureOn(entry.Match.SecureOn, defaults.secureOn)
	if err != nil {
		return wol.Rule{}, err
	}

	return wol.Rule{
		Match: wol.Match{
			Ports:    entry.Match.Ports,
			MAC:      mac,
			Content:  buildContent(entry.Match.Content),
			SrcCIDRs: entry.Match.SrcCIDRs,
			Auth:     wol.AuthKind(entry.Match.Auth),
			SecureOn: secureOn,
		},
		Action: action,
		DryRun: ruleDryRun,
	}, nil
}

// buildSecureOn resolves the password a rule requires: its own when given, else the one its block
// declares, else nothing - which means "inherit security.secure_on" in the policy. An explicitly
// empty value is not the same as leaving it out: it requires a packet without a password, which is
// how a block opts out of the global default (§19.18).
func buildSecureOn(rule *string, block *string) ([]byte, error) {
	value := rule
	if value == nil {
		value = block
	}

	if value == nil {
		return nil, nil
	}

	if *value == "" {
		return []byte{}, nil
	}

	if len(*value) != wol.SecureOnSize {
		return nil, fmt.Errorf("%w: got %d bytes", wol.ErrSecureOnLength, len(*value))
	}

	return []byte(*value), nil
}

// checkMatchAuth rejects an unknown match.auth value while the file is being read; the policy
// would catch it too, but a configuration mistake should fail at load time.
func checkMatchAuth(value string) error {
	switch wol.AuthKind(value) {
	case "", wol.AuthHMAC:
		return nil
	}

	return fmt.Errorf("%w: %s", wol.ErrUnknownAuthKind, value)
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

package exec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

var (
	ErrEmptyCommand         = errors.New("no command configured")
	ErrCommandNotFound      = errors.New("command not found")
	ErrCommandNotExecutable = errors.New("command is not executable")
	ErrCommandNotAllowed    = errors.New("command is outside security.exec_allowlist")
)

// DefaultTimeout bounds an exec action when the configuration leaves the timeout unset.
const DefaultTimeout = 10 * time.Second

// Executor runs custom commands (action type exec).
type Executor struct {
	allowlist []string
}

func NewExecutor(allowlist []string) *Executor {
	return &Executor{allowlist: allowlist}
}

// Validate checks an exec action at startup: a command must be present and resolvable,
// with parseable argument templates, and must live inside the allowlist when one is
// configured.
func (e *Executor) Validate(def wol.ActionDef) error {
	params := def.Exec
	if params == nil || len(params.Command) == 0 {
		return ErrEmptyCommand
	}

	if err := parseTemplates(params.Command); err != nil {
		return err
	}

	if err := validateCredential(params); err != nil {
		return err
	}

	if params.Shell {
		slog.Warn("exec action runs through a shell: arguments are no longer argv-safe",
			"action", string(def.Name),
			"command", params.Command,
		)

		return nil
	}

	_, err := e.resolve(params.Command[0])

	return err
}

// validateCredential resolves a configured privilege drop at startup so that an
// unknown user/group, or a drop the process cannot perform, fails fast instead of
// at trigger time.
func validateCredential(params *wol.ExecParams) error {
	cred, err := resolveCredential(params.User, params.Group)
	if err != nil {
		return err
	}

	if cred.isZero() {
		return nil
	}

	return requirePrivilege()
}

// dropLabel describes the configured privilege drop for audit logs.
func dropLabel(params *wol.ExecParams) string {
	switch {
	case params.User != "" && params.Group != "":
		return params.User + ":" + params.Group
	case params.User != "":
		return params.User
	case params.Group != "":
		return ":" + params.Group
	default:
		return ""
	}
}

// Execute runs the configured command with the whitelisted event values interpolated.
func (e *Executor) Execute(ctx context.Context, def wol.ActionDef, ev wol.Event) error {
	params := def.Exec
	if params == nil || len(params.Command) == 0 {
		return fmt.Errorf("%w: action %s", ErrEmptyCommand, def.Name)
	}

	argv, err := interpolate(params.Command, def.Name, ev)
	if err != nil {
		return fmt.Errorf("action %s: %w", def.Name, err)
	}

	timeout := params.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd, err := e.command(runCtx, params, argv)
	if err != nil {
		return fmt.Errorf("action %s: %w", def.Name, err)
	}

	cred, err := resolveCredential(params.User, params.Group)
	if err != nil {
		return fmt.Errorf("action %s: %w", def.Name, err)
	}

	applyCredential(cmd, cred)

	cmd.Dir = params.Workdir
	cmd.Env = append(os.Environ(), params.Env...)

	started := time.Now()
	output, runErr := cmd.CombinedOutput()

	attrs := []any{
		"action", string(def.Name),
		"command", argv,
		"src", ipString(ev.SrcIP),
		"interface", ev.Interface,
		"duration", time.Since(started).String(),
		"exit_code", exitCode(runErr),
		"output", strings.TrimSpace(string(output)),
	}

	if label := dropLabel(params); label != "" {
		attrs = append(attrs, "run_as", label)
	}

	slog.Info("exec action finished", attrs...)

	if runErr != nil {
		return fmt.Errorf("action %s: %w", def.Name, runErr)
	}

	return nil
}

func (e *Executor) command(ctx context.Context, params *wol.ExecParams, argv []string) (*osexec.Cmd, error) {
	if params.Shell {
		return shellCommand(ctx, strings.Join(argv, " ")), nil
	}

	resolved, err := e.resolve(argv[0])
	if err != nil {
		return nil, err
	}

	// The command was resolved to an absolute path and validated at startup.
	return osexec.CommandContext(ctx, resolved, argv[1:]...), nil
}

// resolve finds the executable, honouring security.exec_allowlist when it is set.
func (e *Executor) resolve(name string) (string, error) {
	path := name

	if filepath.IsAbs(name) {
		if err := checkExecutable(name); err != nil {
			return "", err
		}
	} else {
		found, err := osexec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf("%w: %s", ErrCommandNotFound, name)
		}

		path = found
	}

	if len(e.allowlist) == 0 {
		return path, nil
	}

	return e.checkAllowlist(path)
}

func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrCommandNotFound, path)
	}

	if info.IsDir() {
		return fmt.Errorf("%w: %s is a directory", ErrCommandNotFound, path)
	}

	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%w: %s", ErrCommandNotExecutable, path)
	}

	return nil
}

func (e *Executor) checkAllowlist(path string) (string, error) {
	clean := filepath.Clean(path)

	for _, dir := range e.allowlist {
		root := filepath.Clean(dir)
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return path, nil
		}
	}

	return "", fmt.Errorf("%w: %s", ErrCommandNotAllowed, path)
}

// Vars and the interpolation helpers live in the domain package so that every
// action type renders the same whitelisted values (§4.3).
func interpolate(args []string, action wol.Action, ev wol.Event) ([]string, error) {
	return wol.EventVars(action, ev).InterpolateAll(args)
}

// parseTemplates parses the argv templates at startup so that syntax errors fail fast
// instead of at trigger time.
func parseTemplates(args []string) error {
	return wol.ParseTemplates(args)
}

func shellCommand(ctx context.Context, line string) *osexec.Cmd {
	if runtime.GOOS == "windows" {
		return osexec.CommandContext(ctx, "cmd", "/C", line)
	}

	return osexec.CommandContext(ctx, "/bin/sh", "-c", line)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}

	var exitErr *osexec.ExitError

	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}

	return -1
}

func ipString(ip net.IP) string {
	if ip == nil {
		return ""
	}

	return ip.String()
}

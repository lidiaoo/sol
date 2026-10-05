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
	"text/template"
	"time"

	"github.com/bavix/sol/internal/domain/wol"
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
// and must live inside the allowlist when one is configured.
func (e *Executor) Validate(def wol.ActionDef) error {
	params := def.Exec
	if params == nil || len(params.Command) == 0 {
		return ErrEmptyCommand
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

	cmd.Dir = params.Workdir
	cmd.Env = append(os.Environ(), params.Env...)

	started := time.Now()
	output, runErr := cmd.CombinedOutput()

	slog.Info("exec action finished",
		"action", string(def.Name),
		"command", argv,
		"src", ipString(ev.SrcIP),
		"interface", ev.Interface,
		"duration", time.Since(started).String(),
		"exit_code", exitCode(runErr),
		"output", strings.TrimSpace(string(output)),
	)

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

// Vars are the values an exec command may interpolate; anything else is rejected (§4.3).
type Vars struct {
	Action    string
	SrcIP     string
	SrcPort   int
	DstPort   int
	Interface string
	MAC       string
	Time      string
}

func interpolate(args []string, action wol.Action, ev wol.Event) ([]string, error) {
	values := Vars{
		Action:    string(action),
		SrcIP:     ipString(ev.SrcIP),
		SrcPort:   ev.SrcPort,
		DstPort:   ev.DstPort,
		Interface: ev.Interface,
		MAC:       ev.TargetMAC.String(),
		Time:      time.Now().Format(time.RFC3339),
	}

	out := make([]string, 0, len(args))

	for _, arg := range args {
		tmpl, err := template.New("arg").Option("missingkey=error").Parse(arg)
		if err != nil {
			return nil, fmt.Errorf("invalid template %q: %w", arg, err)
		}

		var buf strings.Builder

		if execErr := tmpl.Execute(&buf, values); execErr != nil {
			return nil, fmt.Errorf("interpolate %q: %w", arg, execErr)
		}

		out = append(out, buf.String())
	}

	return out, nil
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

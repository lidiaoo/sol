package system

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/bavix/sol/internal/domain/wol"
)

const (
	osLinux   = "linux"
	osDarwin  = "darwin"
	osWindows = "windows"
)

var (
	ErrUnsupportedOS     = errors.New("unsupported operating system")
	ErrUnsupportedAction = errors.New("unsupported power action")
)

// PowerController executes the built-in power actions.
type PowerController struct{}

func NewPowerController() *PowerController {
	return &PowerController{}
}

func (p *PowerController) Execute(ctx context.Context, def wol.ActionDef, _ wol.Event) error {
	switch def.Type {
	case wol.ActionTypeNoop:
		return nil
	case wol.ActionTypeShutdown:
		return p.shutdown(ctx)
	case wol.ActionTypeReboot:
		return p.reboot(ctx)
	case wol.ActionTypeSleep:
		return p.sleep(ctx)
	case wol.ActionTypeExec:
		// exec actions go to their own executor; reaching here means the registry miswired it.
		return fmt.Errorf("%w: %s", ErrUnsupportedAction, def.Type)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedAction, def.Type)
	}
}

func (p *PowerController) shutdown(ctx context.Context) error {
	switch runtime.GOOS {
	case osWindows:
		return execCmd(ctx, "shutdown", "-s", "-t", "0", "-f")
	case osLinux, osDarwin:
		return execCmd(ctx, "shutdown", "-h", "now")
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedOS, runtime.GOOS)
	}
}

func (p *PowerController) reboot(ctx context.Context) error {
	switch runtime.GOOS {
	case osWindows:
		return execCmd(ctx, "shutdown", "-r", "-t", "0", "-f")
	case osLinux, osDarwin:
		return execCmd(ctx, "shutdown", "-r", "now")
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedOS, runtime.GOOS)
	}
}

func (p *PowerController) sleep(ctx context.Context) error {
	switch runtime.GOOS {
	case osWindows:
		return execCmd(ctx, "rundll32", "powrprof.dll,SetSuspendState", "0,1,0")
	case osDarwin:
		return execCmd(ctx, "pmset", "sleepnow")
	case osLinux:
		return execCmd(ctx, "systemctl", "suspend")
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedOS, runtime.GOOS)
	}
}

func execCmd(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

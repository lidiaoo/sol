package exec_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
	"github.com/bavix/sol/internal/infra/exec"
)

func echoDef(args ...string) wol.ActionDef {
	return wol.ActionDef{
		Name: "echo-test",
		Type: wol.ActionTypeExec,
		Exec: &wol.ExecParams{Command: args},
	}
}

func TestExecuteRunsArgv(t *testing.T) {
	t.Parallel()

	out := filepath.Join(t.TempDir(), "out.txt")

	executor := exec.NewExecutor(nil)

	def := echoDef("sh", "-c", "printf hello > $1", "sh", out)
	require.NoError(t, executor.Execute(context.Background(), def, wol.Event{}))

	content, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "hello", string(content))
}

func TestExecuteFailsOnNonZeroExit(t *testing.T) {
	t.Parallel()

	executor := exec.NewExecutor(nil)

	err := executor.Execute(context.Background(), echoDef("sh", "-c", "exit 3"), wol.Event{})
	require.Error(t, err)

	var exitErr interface{ ExitCode() int }

	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 3, exitErr.ExitCode())
}

func TestExecuteTimesOut(t *testing.T) {
	t.Parallel()

	executor := exec.NewExecutor(nil)

	def := echoDef("sh", "-c", "sleep 5")
	def.Exec.Timeout = 100 * time.Millisecond

	started := time.Now()
	err := executor.Execute(context.Background(), def, wol.Event{})

	require.Error(t, err)
	require.Less(t, time.Since(started), 3*time.Second)
}

func TestExecuteMissingCommand(t *testing.T) {
	t.Parallel()

	executor := exec.NewExecutor(nil)

	err := executor.Execute(context.Background(), echoDef("sol-no-such-binary"), wol.Event{})
	require.ErrorIs(t, err, exec.ErrCommandNotFound)
}

func TestExecuteEmptyCommand(t *testing.T) {
	t.Parallel()

	executor := exec.NewExecutor(nil)

	err := executor.Execute(context.Background(), wol.ActionDef{Name: "bad", Type: wol.ActionTypeExec}, wol.Event{})
	require.ErrorIs(t, err, exec.ErrEmptyCommand)
}

func TestExecuteInterpolatesWhitelistedVars(t *testing.T) {
	t.Parallel()

	out := filepath.Join(t.TempDir(), "vars.txt")

	executor := exec.NewExecutor(nil)

	def := echoDef("sh", "-c", "printf '%s|%s|%s|%s' $1 $2 $3 $4 > $5",
		"sh", "{{.Action}}", "{{.SrcIP}}", "{{.Interface}}", "{{.DstPort}}", out)

	ev := wol.Event{
		SrcIP:     []byte{10, 0, 0, 5},
		DstPort:   10,
		Interface: "eth0",
		TargetMAC: []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF},
	}

	require.NoError(t, executor.Execute(context.Background(), def, ev))

	content, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "echo-test|10.0.0.5|eth0|10", string(content))
}

func TestExecuteShellEscapeHatch(t *testing.T) {
	t.Parallel()

	out := filepath.Join(t.TempDir(), "shell.txt")

	executor := exec.NewExecutor(nil)

	def := wol.ActionDef{
		Name: "shell-test",
		Type: wol.ActionTypeExec,
		Exec: &wol.ExecParams{
			Command: []string{"printf", "joined", "args", ">", out},
			Shell:   true,
		},
	}

	require.NoError(t, executor.Execute(context.Background(), def, wol.Event{}))

	content, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "joined", string(content))
}

func TestExecuteEnvAndWorkdir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")

	executor := exec.NewExecutor(nil)

	def := echoDef("sh", "-c", "printf '%s' \"$SOL_TEST\" > env.txt")
	def.Exec.Workdir = dir
	def.Exec.Env = []string{"SOL_TEST=from-config"}

	require.NoError(t, executor.Execute(context.Background(), def, wol.Event{}))

	content, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "from-config", string(content))
}

func TestValidate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	script := filepath.Join(dir, "job.sh")
	//nolint:gosec // the test needs an executable script
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n"), 0o700))

	t.Run("absolute path inside allowlist", func(t *testing.T) {
		t.Parallel()

		executor := exec.NewExecutor([]string{dir})
		require.NoError(t, executor.Validate(echoDef(script)))
	})

	t.Run("absolute path outside allowlist", func(t *testing.T) {
		t.Parallel()

		executor := exec.NewExecutor([]string{filepath.Join(dir, "other")})
		require.ErrorIs(t, executor.Validate(echoDef(script)), exec.ErrCommandNotAllowed)
	})

	t.Run("path lookup for bare names", func(t *testing.T) {
		t.Parallel()

		executor := exec.NewExecutor(nil)
		require.NoError(t, executor.Validate(echoDef("sh")))
		require.ErrorIs(t, executor.Validate(echoDef("sol-no-such-binary")), exec.ErrCommandNotFound)
	})

	t.Run("non executable file", func(t *testing.T) {
		t.Parallel()

		plain := filepath.Join(dir, "plain.txt")
		require.NoError(t, os.WriteFile(plain, []byte("x"), 0o600))

		executor := exec.NewExecutor(nil)
		require.ErrorIs(t, executor.Validate(echoDef(plain)), exec.ErrCommandNotExecutable)
	})

	t.Run("empty command", func(t *testing.T) {
		t.Parallel()

		executor := exec.NewExecutor(nil)
		require.ErrorIs(t, executor.Validate(wol.ActionDef{Name: "x", Type: wol.ActionTypeExec}), exec.ErrEmptyCommand)
	})
}

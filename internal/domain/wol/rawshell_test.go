package wol_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

// fullMatch compiles an allowlist entry the way the loader does.
func fullMatch(entry string) *regexp.Regexp {
	return regexp.MustCompile("^(?:" + entry + ")$")
}

func TestValidateRawShellCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		allowlist []*regexp.Regexp
		command   string
		match     error
	}{
		{name: "no allowlist accepts anything", command: "anything at all"},
		{name: "allowlist hit", allowlist: []*regexp.Regexp{fullMatch(`echo ok`)}, command: "echo ok"},
		{
			name:      "allowlist miss",
			allowlist: []*regexp.Regexp{fullMatch(`echo ok`)},
			command:   "echo ok; id",
			match:     wol.ErrRawShellNotAllowed,
		},
		{
			name:      "entry does not leak into a longer command",
			allowlist: []*regexp.Regexp{fullMatch(`echo`)},
			command:   "xecho",
			match:     wol.ErrRawShellNotAllowed,
		},
		{name: "empty command", command: "   ", match: wol.ErrRawShellEmpty},
		{
			name:    "command too long",
			command: strings.Repeat("a", wol.MaxRawShellCommand+1),
			match:   wol.ErrRawShellTooLong,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := wol.ValidateRawShellCommand(tc.allowlist, tc.command)
			if tc.match == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.match)
		})
	}
}

func TestRawShellActionName(t *testing.T) {
	t.Parallel()

	// Guards, counters and the audit log key on this name, so it is part of the contract.
	require.Equal(t, wol.RawShellAction, wol.Action("raw:shell"))
	require.Equal(t, "raw:shell", string(wol.RawShellAction))
	// No rule may reference it: the command comes from the packet, not from the configuration.
	require.False(t, wol.RawShellAction.Valid())
}

func TestDispatchDefRunsAnUnregisteredDefinition(t *testing.T) {
	t.Parallel()

	executor := &stubExecutor{}
	registry := wol.NewRegistry()
	registry.Register(executor, wol.ActionTypeExec)

	def := wol.ActionDef{
		Name: wol.RawShellAction,
		Type: wol.ActionTypeExec,
		Exec: &wol.ExecParams{Command: []string{"true"}, Shell: true},
	}

	require.NoError(t, registry.DispatchDef(t.Context(), def, wol.Event{}))
	require.Equal(t, def, executor.def)

	require.ErrorIs(t,
		registry.DispatchDef(t.Context(), wol.ActionDef{Type: wol.ActionTypeSend}, wol.Event{}),
		wol.ErrNoExecutor)
}

type stubExecutor struct {
	def wol.ActionDef
}

func (s *stubExecutor) Execute(_ context.Context, def wol.ActionDef, _ wol.Event) error {
	s.def = def

	return nil
}

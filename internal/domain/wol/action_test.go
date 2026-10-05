package wol_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

type recordingExecutor struct {
	calls int
	def   wol.ActionDef
}

func (r *recordingExecutor) Execute(_ context.Context, def wol.ActionDef, _ wol.Event) error {
	r.calls++
	r.def = def

	return nil
}

func TestParseActionSleep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected wol.Action
	}{
		{input: "sleep", expected: wol.ActionSleep},
		{input: "Sleep", expected: wol.ActionSleep},
		{input: "suspend", expected: wol.ActionSleep},
		{input: "power.sleep", expected: wol.ActionSleep},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()

			got, err := wol.ParseAction(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestBuiltinActions(t *testing.T) {
	t.Parallel()

	actions := wol.BuiltinActions()
	require.Len(t, actions, 4)

	tests := []struct {
		name string
		typ  wol.ActionType
	}{
		{name: "noop", typ: wol.ActionTypeNoop},
		{name: "power.sleep", typ: wol.ActionTypeSleep},
		{name: "power.shutdown", typ: wol.ActionTypeShutdown},
		{name: "power.reboot", typ: wol.ActionTypeReboot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			def, ok := actions[wol.Action(tt.name)]
			require.True(t, ok)
			require.Equal(t, wol.Action(tt.name), def.Name)
			require.Equal(t, tt.typ, def.Type)
		})
	}
}

func TestRegistryDispatch(t *testing.T) {
	t.Parallel()

	for _, action := range []wol.Action{wol.ActionShutdown, wol.ActionSleep, wol.ActionReboot} {
		t.Run(string(action), func(t *testing.T) {
			t.Parallel()

			executor := &recordingExecutor{}
			registry := wol.NewRegistry()
			registry.Register(executor, wol.ActionTypeShutdown, wol.ActionTypeSleep, wol.ActionTypeReboot)

			require.NoError(t, registry.Dispatch(context.Background(), action, wol.Event{}))
			require.Equal(t, 1, executor.calls)
			require.Equal(t, action, executor.def.Name)
		})
	}
}

func TestRegistryDispatchUnknownAction(t *testing.T) {
	t.Parallel()

	registry := wol.NewRegistry()

	err := registry.Dispatch(context.Background(), wol.Action("power.teleport"), wol.Event{})
	require.ErrorIs(t, err, wol.ErrUnknownActionRef)
}

func TestRegistryDispatchMissingExecutor(t *testing.T) {
	t.Parallel()

	registry := wol.NewRegistry()

	err := registry.Dispatch(context.Background(), wol.ActionShutdown, wol.Event{})
	require.ErrorIs(t, err, wol.ErrNoExecutor)
}

func TestRegistryAction(t *testing.T) {
	t.Parallel()

	registry := wol.NewRegistry()

	def, ok := registry.Action(wol.ActionReboot)
	require.True(t, ok)
	require.Equal(t, wol.ActionTypeReboot, def.Type)

	_, ok = registry.Action(wol.Action("nope"))
	require.False(t, ok)
}

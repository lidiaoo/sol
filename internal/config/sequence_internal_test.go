package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

func TestLoadSequenceAction(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
version: 1
actions:
  - name: notify
    type: exec
    command: [/usr/bin/true]
  - name: shutdown-then-notify
    type: sequence
    steps: [power.shutdown, notify]
rules:
  - { match: { ports: [10041] }, action: shutdown-then-notify }
`))
	require.NoError(t, err)

	combo := cfg.Actions["shutdown-then-notify"]
	require.Equal(t, wol.ActionTypeSequence, combo.Type)
	require.Equal(t, []wol.Action{wol.ActionShutdown, "notify"}, combo.Sequence.Steps,
		"steps keep the configured order")
}

func TestLoadSequenceErrors(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		match error
	}{
		{
			name:  "no steps",
			body:  "version: 1\nactions:\n  - { name: combo, type: sequence }\n",
			match: ErrSequenceStepsRequired,
		},
		{
			name:  "blank step",
			body:  "version: 1\nactions:\n  - { name: combo, type: sequence, steps: ['  '] }\n",
			match: ErrSequenceStepsRequired,
		},
		{
			name:  "exec parameters on a sequence",
			body:  "version: 1\nactions:\n  - { name: combo, type: sequence, steps: [noop], command: [/usr/bin/true] }\n",
			match: ErrActionParams,
		},
		{
			name:  "steps on a built-in action",
			body:  "version: 1\nactions:\n  - { name: combo, type: noop, steps: [noop] }\n",
			match: ErrActionParams,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			require.ErrorIs(t, err, tc.match)
		})
	}
}

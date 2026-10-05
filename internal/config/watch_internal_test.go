package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadWatchInterval(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
version: 1
server:
  watch: 5s
rules:
  - { match: { ports: [10041] }, action: noop }
`))
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, cfg.Watch)

	// Empty and "0" both mean "do not watch".
	for _, value := range []string{"", "0", "0s"} {
		cfg, err = Load(writeConfig(t, `
version: 1
server:
  watch: "`+value+`"
rules:
  - { match: { ports: [10041] }, action: noop }
`))
		require.NoError(t, err)
		require.Zero(t, cfg.Watch, "watch %q must disable the watcher", value)
	}
}

func TestLoadWatchIntervalRejectsFastPolling(t *testing.T) {
	for _, value := range []string{"500ms", "nonsense", "-1s"} {
		_, err := Load(writeConfig(t, `
version: 1
server:
  watch: `+value+`
rules:
  - { match: { ports: [10041] }, action: noop }
`))
		require.ErrorIs(t, err, ErrWatchInterval, "watch %q must fail the start-up", value)
	}
}

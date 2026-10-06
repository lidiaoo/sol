package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

// The config layer only carries the names; resolving them (and refusing a drop the
// process cannot perform) is the exec executor's startup job.
func TestLoadExecPrivilegeDrop(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
version: 1
actions:
  - name: lock
    type: exec
    command: [/usr/bin/loginctl, lock-session]
    user: nobody
    group: nogroup
rules:
  - { match: { ports: [10041] }, action: lock }
`))
	require.NoError(t, err)

	lock := cfg.Actions["lock"]
	require.Equal(t, wol.ActionTypeExec, lock.Type)
	require.Equal(t, "nobody", lock.Exec.User)
	require.Equal(t, "nogroup", lock.Exec.Group)
}

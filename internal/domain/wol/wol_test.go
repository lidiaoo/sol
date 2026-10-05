package wol_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

func TestParseAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected wol.Action
	}{
		{input: "shutdown", expected: wol.ActionShutdown},
		{input: "Shutdown", expected: wol.ActionShutdown},
		{input: "s", expected: wol.ActionShutdown},
		{input: "power.shutdown", expected: wol.ActionShutdown},
		{input: "reboot", expected: wol.ActionReboot},
		{input: "Reboot", expected: wol.ActionReboot},
		{input: "r", expected: wol.ActionReboot},
		{input: "power.reboot", expected: wol.ActionReboot},
		{input: "noop", expected: wol.ActionNoop},
		{input: " n ", expected: wol.ActionNoop},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()

			got, err := wol.ParseAction(tt.input)
			require.NoError(t, err)
			require.Equal(t, tt.expected, got)
		})
	}

	t.Run("invalid poweroff", func(t *testing.T) {
		t.Parallel()

		_, err := wol.ParseAction("poweroff")
		require.ErrorIs(t, err, wol.ErrUnknownAction)
	})

	t.Run("invalid empty", func(t *testing.T) {
		t.Parallel()

		_, err := wol.ParseAction("")
		require.ErrorIs(t, err, wol.ErrUnknownAction)
	})
}

func TestActionValid(t *testing.T) {
	t.Parallel()

	require.True(t, wol.ActionNoop.Valid())
	require.True(t, wol.ActionShutdown.Valid())
	require.True(t, wol.ActionReboot.Valid())
	require.False(t, wol.Action("exec").Valid())
	require.False(t, wol.Action("").Valid())
}

func TestDefaultReservedPorts(t *testing.T) {
	t.Parallel()

	require.Equal(t, []int{wol.PortEcho, wol.PortDefault}, wol.DefaultReservedPorts())
}

func TestBuildMagicPacket(t *testing.T) {
	t.Parallel()

	mac := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	pkt := wol.BuildMagicPacket(mac)

	require.Len(t, pkt, wol.PacketLenPlain)

	for i, b := range pkt[:6] {
		require.Equal(t, byte(0xFF), b, "header byte %d", i)
	}
}

func TestContainsMagicPacket(t *testing.T) {
	t.Parallel()

	mac := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	magic := wol.BuildMagicPacket(mac)

	tests := []struct {
		name string
		got  []byte
		want bool
	}{
		{"exact", magic, true},
		{"with prefix", append([]byte{0x00, 0x01}, magic...), true},
		{"with suffix", append(magic, 0x03, 0x04), true},
		{"wrong mac", wol.BuildMagicPacket([]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66}), false},
		{"too short", []byte{0xFF, 0xFF, 0xFF}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, wol.ContainsMagicPacket(tt.got, magic))
		})
	}
}

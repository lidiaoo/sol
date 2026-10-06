package system

import "testing"

func TestHasNetBindServiceReadsTheCapabilitySet(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   bool
	}{
		{
			name:   "no capabilities at all",
			status: "Name:\tsol\nCapEff:\t0000000000000000\n",
		},
		{
			name:   "CAP_NET_BIND_SERVICE alone (bit 10)",
			status: "CapEff:\t0000000000000400\n",
			want:   true,
		},
		{
			name:   "CAP_NET_BIND_SERVICE among others, as a systemd unit grants it",
			status: "Name:\tsol\nCapEff:\t0000000000000420\nCapBnd:\t0000000000000420\n",
			want:   true,
		},
		{
			name:   "the neighbouring bit does not count",
			status: "CapEff:\t0000000000000200\n",
		},
		{
			name:   "a status dump without the line",
			status: "Name:\tsol\n",
		},
		{
			name:   "an unparseable value is not a licence to warn about permission",
			status: "CapEff:\tnot-hex\n",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := hasNetBindService(testCase.status); got != testCase.want {
				t.Fatalf("hasNetBindService() = %v, want %v", got, testCase.want)
			}
		})
	}
}

// CanBindPrivilegedPorts must be safe to call anywhere: on Windows it answers yes without looking,
// on other platforms it inspects the process it is part of.
func TestCanBindPrivilegedPortsIsCallable(t *testing.T) {
	if got := CanBindPrivilegedPorts(); got != true && got != false {
		t.Fatalf("CanBindPrivilegedPorts() = %v, want a boolean", got)
	}
}

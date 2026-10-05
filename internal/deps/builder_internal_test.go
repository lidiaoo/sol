package deps

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/domain/wol"
)

func TestReplayCounters(t *testing.T) {
	t.Parallel()

	var counters replayCounters
	require.Zero(t, counters.total())
	require.Nil(t, counters.reasons(), "an untouched counter keeps the status view free of zeroes")

	counters.add(wol.ReplayStale)
	counters.add(wol.ReplaySeen)
	counters.add(wol.ReplaySeen)
	counters.add(wol.ReplayFull)
	counters.add("a reason from nowhere") // must not panic, and must not be counted

	require.Equal(t, uint64(4), counters.total())
	require.Equal(t, map[string]uint64{
		wol.ReplayStale: 1,
		wol.ReplaySeen:  2,
		wol.ReplayFull:  1,
	}, counters.reasons())
}

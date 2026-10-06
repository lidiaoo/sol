package deps

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/app"
	"github.com/lidiaoo/sol/internal/infra/httpapi"
)

func TestRemoteCommandErrorMapsTheGuardrails(t *testing.T) {
	t.Parallel()

	// A remote command refused by a guardrail has to come back as 429 through the control plane.
	// Before this mapping existed, a cooldown on POST /v1/commands/{id} fell through to the
	// default and answered 500, which reads like a bug in sol rather than a guardrail doing its
	// job.
	cases := []struct {
		name     string
		err      error
		expected error
	}{
		{"cooldown", app.ErrActionSuppressed, httpapi.ErrSuppressed},
		{"rate limit", app.ErrActionRateLimited, httpapi.ErrRateLimited},
		{"in flight", app.ErrActionInFlight, httpapi.ErrInFlight},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			wrapped := fmt.Errorf("command rejected: %w", tc.err)

			got := remoteCommandError(wrapped, "backup")
			require.ErrorIs(t, got, tc.expected)
			require.ErrorIs(t, got, tc.err, "the reason survives the mapping")
		})
	}

	// Anything else keeps its own identity: an unknown command is still a 404, not a guardrail.
	got := remoteCommandError(app.ErrRemoteUnknownCommand, "nope")
	require.ErrorIs(t, got, httpapi.ErrCommandNotFound)
	require.NotErrorIs(t, got, httpapi.ErrInFlight)
}

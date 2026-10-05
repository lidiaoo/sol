package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildRateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		field     securityConfig
		wantRate  float64
		wantBurst int
	}{
		{name: "unset", field: securityConfig{}},
		{name: "empty string", field: securityConfig{RateLimit: ""}},
		{name: "zero", field: securityConfig{RateLimit: "0"}},
		{name: "spaces", field: securityConfig{RateLimit: "  "}},
		{name: "per second", field: securityConfig{RateLimit: "10/s"}, wantRate: 10},
		{name: "bare number is per second", field: securityConfig{RateLimit: "7"}, wantRate: 7},
		{name: "per minute", field: securityConfig{RateLimit: "600/m"}, wantRate: 10},
		{name: "per hour", field: securityConfig{RateLimit: "3600/h"}, wantRate: 1},
		{name: "long units", field: securityConfig{RateLimit: "120 / minute"}, wantRate: 2},
		{name: "fractional", field: securityConfig{RateLimit: "0.5/s"}, wantRate: 0.5},
		{name: "burst kept", field: securityConfig{RateLimit: "10/s", RateBurst: 20}, wantRate: 10, wantBurst: 20},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rate, burst, err := buildRateLimit(tc.field)
			require.NoError(t, err)
			require.InDelta(t, tc.wantRate, rate, 1e-9)
			require.Equal(t, tc.wantBurst, burst)
		})
	}
}

func TestBuildRateLimitRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		field securityConfig
	}{
		{name: "not a number", field: securityConfig{RateLimit: "fast"}},
		{name: "zero per second", field: securityConfig{RateLimit: "0/s"}},
		{name: "negative", field: securityConfig{RateLimit: "-5"}},
		{name: "unknown unit", field: securityConfig{RateLimit: "10/fortnight"}},
		{name: "missing unit", field: securityConfig{RateLimit: "10/"}},
		{name: "negative burst", field: securityConfig{RateLimit: "10/s", RateBurst: -1}},
		{name: "burst without rate", field: securityConfig{RateBurst: 10}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := buildRateLimit(tc.field)
			require.ErrorIs(t, err, ErrRateLimit)
		})
	}
}

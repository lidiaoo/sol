package logging_test

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bavix/sol/internal/infra/logging"
)

func TestParseLevel(t *testing.T) {
	t.Parallel()

	tests := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"INFO":    slog.LevelInfo,
		"debug":   slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}

	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			t.Parallel()

			got, err := logging.ParseLevel(input)
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
}

func TestParseLevelUnknown(t *testing.T) {
	t.Parallel()

	_, err := logging.ParseLevel("verbose")
	require.ErrorIs(t, err, logging.ErrUnknownLevel)
}

func TestNewTextFormatFiltersByLevel(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	logger, err := logging.New("warn", "", &buf)
	require.NoError(t, err)

	logger.Info("hidden")
	logger.Warn("shown")

	out := buf.String()
	require.NotContains(t, out, "hidden")
	require.Contains(t, out, "shown")
}

func TestNewJSONFormat(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	logger, err := logging.New("debug", logging.FormatJSON, &buf)
	require.NoError(t, err)
	logger.Info("hello", "port", 9)

	out := buf.String()
	require.True(t, strings.HasPrefix(out, "{"))
	require.Contains(t, out, `"msg":"hello"`)
	require.Contains(t, out, `"port":9`)
}

func TestNewUnknownFormat(t *testing.T) {
	t.Parallel()

	_, err := logging.New("info", "xml", io.Discard)
	require.ErrorIs(t, err, logging.ErrUnknownFormat)
}

func TestSetupInstallsDefaultLogger(t *testing.T) {
	logger, err := logging.Setup("debug", logging.FormatText)
	require.NoError(t, err)
	require.Equal(t, logger, slog.Default())
}

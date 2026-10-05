package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

var (
	ErrUnknownLevel  = errors.New("unknown log level")
	ErrUnknownFormat = errors.New("unknown log format")
)

const (
	FormatText = "text"
	FormatJSON = "json"

	// DefaultLevel and DefaultFormat apply when the configuration leaves them empty.
	DefaultLevel  = "info"
	DefaultFormat = FormatText
)

// Setup builds a logger from the configured level and format, installs it as the
// process-wide default and returns it.
func Setup(level string, format string) (*slog.Logger, error) {
	logger, err := New(level, format, os.Stderr)
	if err != nil {
		return nil, err
	}

	slog.SetDefault(logger)

	return logger, nil
}

// New builds a logger writing to out. An empty level or format selects the default.
func New(level string, format string, out io.Writer) (*slog.Logger, error) {
	parsedLevel, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}

	handler, err := newHandler(format, parsedLevel, out)
	if err != nil {
		return nil, err
	}

	return slog.New(handler), nil
}

// ParseLevel maps a configured level name onto a slog level.
func ParseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", DefaultLevel:
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("%w: %q (want debug|info|warn|error)", ErrUnknownLevel, value)
	}
}

func newHandler(format string, level slog.Level, out io.Writer) (slog.Handler, error) {
	opts := &slog.HandlerOptions{Level: level}

	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", FormatText:
		return slog.NewTextHandler(out, opts), nil
	case FormatJSON:
		return slog.NewJSONHandler(out, opts), nil
	default:
		return nil, fmt.Errorf("%w: %q (want text|json)", ErrUnknownFormat, format)
	}
}

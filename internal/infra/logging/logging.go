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
	ErrUnknownLevel   = errors.New("unknown log level")
	ErrUnknownFormat  = errors.New("unknown log format")
	ErrUnknownOutput  = errors.New("unknown log output")
	ErrLogFileMissing = errors.New("logging.output: file needs logging.file")
)

const (
	FormatText = "text"
	FormatJSON = "json"

	// OutputStderr is the default destination: the process' stderr, so a service manager
	// (systemd, journald) owns the rotation.
	OutputStderr = "stderr"
	// OutputStdout sends the audit log to standard output instead.
	OutputStdout = "stdout"
	// OutputFile sends the audit log to the file named by the configuration.
	OutputFile = "file"

	// DefaultLevel, DefaultFormat and DefaultOutput apply when the configuration leaves them
	// empty.
	DefaultLevel  = "info"
	DefaultFormat = FormatText
	DefaultOutput = OutputStderr

	// logFileMode keeps the audit log out of reach of other local accounts: it names source
	// addresses, the action that ran and, for exec and raw shell, the command line.
	logFileMode = 0o600
)

// Setup builds the process logger from the configured level, format and output, installs it as
// the process-wide default and returns it. A file output stays open for the life of the process:
// the handler writes through without buffering, so a killed process loses nothing.
func Setup(level string, format string, output string, file string) (*slog.Logger, error) {
	out, err := OpenOutput(output, file)
	if err != nil {
		return nil, err
	}

	logger, err := New(level, format, out)
	if err != nil {
		return nil, err
	}

	slog.SetDefault(logger)

	return logger, nil
}

// OpenOutput resolves the configured output name into the writer the logger writes to. The
// configuration layer refuses a file output without a path as well; checking here too keeps the
// invariant true for any caller, not just the loader.
func OpenOutput(output string, file string) (io.Writer, error) {
	switch strings.ToLower(strings.TrimSpace(output)) {
	case "", OutputStderr:
		return os.Stderr, nil
	case OutputStdout:
		return os.Stdout, nil
	case OutputFile:
		if strings.TrimSpace(file) == "" {
			return nil, fmt.Errorf("%w: %q", ErrLogFileMissing, OutputFile)
		}

		handle, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, logFileMode)
		if err != nil {
			return nil, fmt.Errorf("cannot open the log file: %w", err)
		}

		return handle, nil
	default:
		return nil, fmt.Errorf("%w: %q (want stderr|stdout|file)", ErrUnknownOutput, output)
	}
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

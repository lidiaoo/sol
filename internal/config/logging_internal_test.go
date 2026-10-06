package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/infra/logging"
)

// TestLoadLoggingOutput covers the destination rules of §18: the output names a real destination,
// and a path belongs to the file output only. Both mistakes would otherwise surface at the first
// audit line instead of at start-up.
func TestLoadLoggingOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantOutput string
		wantFile   string
		match      error
	}{
		{
			name: "nothing configured means stderr",
			body: "version: 1\n",
		},
		{
			name:       "stdout",
			body:       "version: 1\nlogging: { output: stdout }\n",
			wantOutput: loggingOutputStdout,
		},
		{
			name:       "a file with a path",
			body:       "version: 1\nlogging: { output: file, file: /tmp/sol-audit.log }\n",
			wantOutput: loggingOutputFile,
			wantFile:   "/tmp/sol-audit.log",
		},
		{
			name:  "an unknown output",
			body:  "version: 1\nlogging: { output: syslog }\n",
			match: ErrLogOutput,
		},
		{
			name:  "a file output without a path",
			body:  "version: 1\nlogging: { output: file }\n",
			match: ErrLogOutput,
		},
		{
			name:  "a path without the file output",
			body:  "version: 1\nlogging: { file: /tmp/sol-audit.log }\n",
			match: ErrLogOutput,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Load(writeConfig(t, tc.body))

			if tc.match != nil {
				require.ErrorIs(t, err, tc.match)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.wantOutput, cfg.Logging.Output)
			require.Equal(t, tc.wantFile, cfg.Logging.File)
		})
	}
}

// TestLoadLoggingOutputFromTheEnvironment checks that the environment is validated the same way as
// the file: a typo in SOL_LOG_OUTPUT must not quietly fall back to stderr.
func TestLoadLoggingOutputFromTheEnvironment(t *testing.T) {
	// No t.Parallel: t.Setenv forbids it.
	t.Setenv("SOL_LOG_OUTPUT", "syslog")

	_, err := Load(writeConfig(t, "version: 1\n"))
	require.ErrorIs(t, err, ErrLogOutput)

	t.Setenv("SOL_LOG_OUTPUT", "file")
	t.Setenv("SOL_LOG_FILE", "/tmp/sol-env-audit.log")

	cfg, err := Load(writeConfig(t, "version: 1\n"))
	require.NoError(t, err)
	require.Equal(t, loggingOutputFile, cfg.Logging.Output)
	require.Equal(t, "/tmp/sol-env-audit.log", cfg.Logging.File)
}

// TestLoggingOutputNamesAgreeEverywhere guards the duplication: config does not import infra, so
// the three names live in config, in infra/logging and in the schema's enum. All three have to
// agree, or a configuration the loader accepts is refused by the logger (or an editor would
// underline a value that works).
func TestLoggingOutputNamesAgreeEverywhere(t *testing.T) {
	t.Parallel()

	require.Equal(t, logging.OutputStderr, loggingOutputStderr)
	require.Equal(t, logging.OutputStdout, loggingOutputStdout)
	require.Equal(t, logging.OutputFile, loggingOutputFile)

	enums, ok := schemaNodeAt(t, loadSchema(t), "/properties/logging/properties/output")["enum"].([]any)
	require.True(t, ok, "the schema must constrain logging.output to an enum")

	require.ElementsMatch(t, []any{"", logging.OutputStderr, logging.OutputStdout, logging.OutputFile}, enums)
}

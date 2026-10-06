package cmd

import (
	"log"

	"github.com/spf13/cobra"

	"github.com/bavix/sol/internal/buildinfo"
)

// buildLabel renders the version cobra prints for --version, naming the commit when the build
// carries one so the output can be pasted into a bug report as is.
func buildLabel() string {
	revision := buildinfo.Revision()
	if revision == "" {
		return buildinfo.Version()
	}

	return buildinfo.Version() + " (" + revision + ")"
}

var rootCmd = &cobra.Command{
	Use:     "sol",
	Short:   "Shutdown-on-LAN service",
	Long:    "sol is a service that listens for Wake-on-LAN magic packets and shuts down the system when received.",
	Version: buildLabel(),
}

// Execute executes the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func init() {
	// Add any global flags here if needed
}

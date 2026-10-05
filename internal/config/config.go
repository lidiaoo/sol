package config

import "github.com/bavix/sol/internal/domain/wol"

// Config holds the application configuration.
type Config struct {
	InterfaceNames       []string
	DryRun               bool
	AllowReservedActions bool
	Rules                []wol.Rule
}

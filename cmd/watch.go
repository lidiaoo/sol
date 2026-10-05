package cmd

import (
	"context"
	"log/slog"
	"os"
	"time"
)

// watchConfigFile reloads the configuration whenever the file changes on disk. It polls
// instead of pulling in an OS watcher, so sol keeps its dependency-free surface; the price
// is one stat per interval and one read when the stamp moves.
//
// A failed reload keeps the running configuration (the reloader logs the reason), and a file
// that disappears mid-flight is treated as a change like any other. The interval itself is
// not re-read from a reloaded configuration: changing server.watch needs a restart.
func watchConfigFile(ctx context.Context, path string, interval time.Duration, reload func(context.Context) error) {
	if path == "" || interval <= 0 {
		return
	}

	// The baseline is taken synchronously: a file that does not exist yet (a config that
	// flags/defaults provided, an installation that adds /etc/sol/sol.yaml later) must
	// count as a change once it appears, instead of being adopted as the baseline.
	stamp := stampOf(path)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current := stampOf(path)
				if current == stamp {
					continue
				}

				stamp = current

				slog.Info("configuration file changed", "path", path, "interval", interval)

				if err := reload(ctx); err != nil {
					slog.Error("automatic reload failed", "path", path, "error", err)
				}
			}
		}
	}()
}

// fileStamp is the change detector: size and modification time (nanosecond resolution on
// every filesystem sol supports). A missing or unreadable file has a zero stamp, so creating
// it later counts as a change.
type fileStamp struct {
	mod  time.Time
	size int64
}

func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}

	return fileStamp{mod: info.ModTime(), size: info.Size()}
}

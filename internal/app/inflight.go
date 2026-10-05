package app

import (
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/bavix/sol/internal/domain/wol"
)

// inflight deduplicates concurrent runs of the same action: while one is in flight, another
// trigger that resolves to the same thing is suppressed instead of running it a second time
// (§19.12.1). The cooldown guard bounds *repeats* after a run; this one bounds *overlap* during a
// run, which is the case a long action (a shutdown, an exec with a five minute timeout) leaves
// open. It is not a merge: the duplicate is refused with a reason, because waiting for a run of
// unknown length inside the control plane would pin a request to it.
type inflight struct {
	mu     sync.Mutex
	active map[string]struct{}
}

func newInflight() *inflight {
	return &inflight{active: make(map[string]struct{})}
}

// begin marks a run as in flight and returns the release the caller must call when it finishes.
// A false return means the same run is already going: nothing was marked, so nothing may be
// released, and the caller must not execute.
func (f *inflight) begin(key string) (func(), bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, running := f.active[key]; running {
		return nil, false
	}

	f.active[key] = struct{}{}

	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()

		delete(f.active, key)
	}, true
}

// running counts the runs in flight; the status view reports it.
func (f *inflight) running() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.active)
}

// runKey identifies what a trigger would actually run: the action name plus the inputs that make
// two invocations of it different (the validated remote arguments, or the raw shell command).
// Two triggers that resolve to the same thing are duplicates; two that differ are not, so
// `remote:backup target=home` never suppresses `remote:backup target=work`.
func runKey(action string, ev wol.Event, extra string) string {
	parts := []string{action}

	keys := slices.Sorted(maps.Keys(ev.Args))
	for _, key := range keys {
		parts = append(parts, key+"="+ev.Args[key])
	}

	if extra != "" {
		parts = append(parts, extra)
	}

	return strings.Join(parts, "\x00")
}

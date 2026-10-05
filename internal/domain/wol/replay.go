package wol

import (
	"sync"
	"time"
)

// Replay reasons reported when a packet carries a valid tag but cannot be accepted.
const (
	// ReplayStale reports a stamp outside the configured window.
	ReplayStale = "stale"
	// ReplaySeen reports a tag that was already accepted inside the window.
	ReplaySeen = "replay"
	// ReplayFull reports a cache that is full of unexpired tags: new packets are refused rather
	// than evicting one, because evicting would reopen the replay window it is there to close.
	ReplayFull = "replay cache full"
)

// maxSeenTags bounds the replay cache. The window is what keeps it small: entries expire with
// their stamp, so the bound only matters while a flood of distinct packets is arriving.
const maxSeenTags = 4096

// pruneInterval limits how often expired entries are collected.
const pruneInterval = time.Second

// ReplayGuard remembers the tags accepted inside the window and refuses them a second time. A
// window of zero disables it, which leaves the plain §19.14 behaviour (a valid tag is enough).
// The same guard protects the remote command channel's stamped segments (§21.3).
type ReplayGuard struct {
	mu       sync.Mutex
	seen     map[[PacketSignatureLen]byte]int64
	window   time.Duration
	nextGC   time.Time
	onReject func(reason string)
}

// NewReplayGuard builds a guard for a window; a window of zero or less returns nil, which means
// "no replay protection" and is safe to call on: a nil guard is never constructed at all, so
// callers test for nil rather than for a flag.
func NewReplayGuard(window time.Duration, onReject func(reason string)) *ReplayGuard {
	if window <= 0 {
		return nil
	}

	return &ReplayGuard{
		seen:     make(map[[PacketSignatureLen]byte]int64),
		window:   window,
		onReject: onReject,
	}
}

// Accept reports whether a stamped payload may be processed: the stamp has to be fresh, and the
// tag must not have been seen inside the window. The remote command channel (§21.3) and
// authenticated packets (§19.16) share it.
func (g *ReplayGuard) Accept(tag []byte, stamp int64, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.collect(now)

	if age := now.Unix() - stamp; age > int64(g.window.Seconds()) || -age > int64(g.window.Seconds()) {
		g.reject(ReplayStale)

		return false
	}

	var key [PacketSignatureLen]byte
	copy(key[:], tag)

	if _, seen := g.seen[key]; seen {
		g.reject(ReplaySeen)

		return false
	}

	if len(g.seen) >= maxSeenTags {
		g.reject(ReplayFull)

		return false
	}

	// The entry has to outlive the stamp's own window, or a tag could be forgotten while it is
	// still replayable.
	g.seen[key] = stamp + int64(g.window.Seconds())

	return true
}

// collect drops the entries whose stamp can no longer be replayed.
func (g *ReplayGuard) collect(now time.Time) {
	if now.Before(g.nextGC) {
		return
	}

	g.nextGC = now.Add(pruneInterval)

	for key, expiry := range g.seen {
		if expiry <= now.Unix() {
			delete(g.seen, key)
		}
	}
}

func (g *ReplayGuard) reject(reason string) {
	if g.onReject != nil {
		g.onReject(reason)
	}
}

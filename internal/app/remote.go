package app

import (
	"errors"
	"fmt"
	"time"

	"github.com/lidiaoo/sol/internal/domain/wol"
)

var (
	// ErrRemoteUnknownCommand reports a command id that is not whitelisted.
	ErrRemoteUnknownCommand = errors.New("unknown remote command")
	// ErrRemoteDisabled reports a remote command attempt while the channel is off.
	ErrRemoteDisabled = errors.New("remote commands are disabled")
	// ErrRemoteReplay reports a command segment refused as stale or as a replay.
	ErrRemoteReplay = errors.New("remote command is stale or a replay")
)

// remoteRunner implements the whitelisted remote command channel (§21): it maps a
// packet's command segment onto a registered "remote:<id>" action and validates the
// sent arguments against the command's declared specs.
type remoteRunner struct {
	key      []byte
	guard    *wol.ReplayGuard
	now      func() time.Time
	ports    map[int]bool
	commands map[string]wol.RemoteCommand
}

// RemoteSettings configures the whitelisted remote command channel (§21).
type RemoteSettings struct {
	Commands map[string]wol.RemoteCommand
	Ports    []int
	Key      []byte
	// Window, when positive, turns on replay protection (§21.3).
	Window time.Duration
	// OnReject reports a refused attempt with its reason; nil means "log nothing extra".
	OnReject func(reason string)
}

// newRemoteRunner builds the guard; without ports the channel stays disabled and the
// runner is nil (accepts always reports false).
func newRemoteRunner(settings RemoteSettings) *remoteRunner {
	if len(settings.Ports) == 0 {
		return nil
	}

	runner := &remoteRunner{
		key:      settings.Key,
		guard:    wol.NewReplayGuard(settings.Window, settings.OnReject),
		now:      time.Now,
		ports:    make(map[int]bool, len(settings.Ports)),
		commands: settings.Commands,
	}

	for _, port := range settings.Ports {
		runner.ports[port] = true
	}

	return runner
}

// split verifies the command segment, and refuses a replayed one when the channel has a window.
func (r *remoteRunner) split(prefix []byte, content []byte) ([]byte, error) {
	if r.guard == nil {
		return wol.SplitRemoteContent(r.key, prefix, content)
	}

	segment, stamp, tag, err := wol.SplitTimestampedRemoteContent(r.key, prefix, content)
	if err != nil {
		return nil, err
	}

	if !r.guard.Accept(tag, stamp, r.now()) {
		return nil, ErrRemoteReplay
	}

	return segment, nil
}

// accepts reports whether the port may carry remote command segments.
func (r *remoteRunner) accepts(port int) bool {
	return r != nil && r.ports[port]
}

// resolve turns a packet's content region into a whitelisted command plus its
// validated arguments; the signature is checked against the packet prefix.
func (r *remoteRunner) resolve(prefix []byte, content []byte) (wol.RemoteCommand, map[string]string, error) {
	segment, err := r.split(prefix, content)
	if err != nil {
		return wol.RemoteCommand{}, nil, err
	}

	id, args, err := wol.ParseRemoteSegment(segment)
	if err != nil {
		return wol.RemoteCommand{}, nil, err
	}

	cmd, err := r.command(id, args)
	if err != nil {
		return wol.RemoteCommand{}, nil, err
	}

	return cmd, args, nil
}

// manual validates a manually triggered (authenticated HTTP) invocation.
func (r *remoteRunner) manual(id string, args map[string]string) (wol.RemoteCommand, error) {
	if r == nil {
		return wol.RemoteCommand{}, ErrRemoteDisabled
	}

	return r.command(id, args)
}

// command validates a command id and its arguments against the whitelist.
func (r *remoteRunner) command(id string, args map[string]string) (wol.RemoteCommand, error) {
	cmd, known := r.commands[id]
	if !known {
		return wol.RemoteCommand{}, fmt.Errorf("%w: %s", ErrRemoteUnknownCommand, id)
	}

	if err := wol.ValidateRemoteArgs(cmd, args); err != nil {
		return wol.RemoteCommand{}, err
	}

	return cmd, nil
}

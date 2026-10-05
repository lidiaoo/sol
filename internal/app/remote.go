package app

import (
	"errors"
	"fmt"

	"github.com/bavix/sol/internal/domain/wol"
)

var (
	// ErrRemoteUnknownCommand reports a command id that is not whitelisted.
	ErrRemoteUnknownCommand = errors.New("unknown remote command")
	// ErrRemoteDisabled reports a remote command attempt while the channel is off.
	ErrRemoteDisabled = errors.New("remote commands are disabled")
)

// remoteRunner implements the whitelisted remote command channel (§21): it maps a
// packet's command segment onto a registered "remote:<id>" action and validates the
// sent arguments against the command's declared specs.
type remoteRunner struct {
	key      []byte
	ports    map[int]bool
	commands map[string]wol.RemoteCommand
}

// newRemoteRunner builds the guard; without ports the channel stays disabled and the
// runner is nil (accepts always reports false).
func newRemoteRunner(commands map[string]wol.RemoteCommand, ports []int, key []byte) *remoteRunner {
	if len(ports) == 0 {
		return nil
	}

	runner := &remoteRunner{
		key:      key,
		ports:    make(map[int]bool, len(ports)),
		commands: commands,
	}

	for _, port := range ports {
		runner.ports[port] = true
	}

	return runner
}

// accepts reports whether the port may carry remote command segments.
func (r *remoteRunner) accepts(port int) bool {
	return r != nil && r.ports[port]
}

// resolve turns a packet's content region into a whitelisted command plus its
// validated arguments; the signature is checked against the packet prefix.
func (r *remoteRunner) resolve(prefix []byte, content []byte) (wol.RemoteCommand, map[string]string, error) {
	segment, err := wol.SplitRemoteContent(r.key, prefix, content)
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

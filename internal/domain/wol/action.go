package wol

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrUnknownAction = errors.New("unknown action: must be shutdown, reboot or noop")
	ErrInvalidRule   = errors.New("invalid rule format: expected port:action")
)

// Action is a named action reference.
type Action string

const (
	ActionNoop     Action = "noop"
	ActionShutdown Action = "power.shutdown"
	ActionReboot   Action = "power.reboot"
)

// Valid reports whether the action is a built-in action.
func (a Action) Valid() bool {
	switch a {
	case ActionNoop, ActionShutdown, ActionReboot:
		return true
	default:
		return false
	}
}

// ParseAction parses a user supplied action name.
func ParseAction(s string) (Action, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "shutdown", "s", "power.shutdown":
		return ActionShutdown, nil
	case "reboot", "r", "power.reboot":
		return ActionReboot, nil
	case "noop", "n", "none":
		return ActionNoop, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownAction, s)
	}
}

// PowerController executes power actions.
type PowerController interface {
	Execute(ctx context.Context, action Action) error
}

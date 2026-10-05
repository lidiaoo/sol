package wol

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"
)

var (
	ErrUnknownAction = errors.New("unknown action: must be noop, sleep, shutdown or reboot")
	ErrInvalidRule   = errors.New("invalid rule format: expected port:action")
	ErrNoExecutor    = errors.New("no executor registered for action type")
)

// Action is a named action reference.
type Action string

const (
	ActionNoop     Action = "noop"
	ActionSleep    Action = "power.sleep"
	ActionShutdown Action = "power.shutdown"
	ActionReboot   Action = "power.reboot"
)

// ActionType classifies what an action does; executors register per type.
type ActionType string

const (
	ActionTypeNoop     ActionType = "noop"
	ActionTypeSleep    ActionType = "power.sleep"
	ActionTypeShutdown ActionType = "power.shutdown"
	ActionTypeReboot   ActionType = "power.reboot"
	ActionTypeExec     ActionType = "exec"
)

// ExecParams describes a custom command action (type: exec).
type ExecParams struct {
	// Command is the argv executed directly; with Shell set it is joined and handed to a shell.
	Command []string
	// Timeout bounds the execution; zero selects the executor default.
	Timeout time.Duration
	// Workdir is the working directory; empty means inherit the parent's.
	Workdir string
	// Env holds extra "KEY=value" entries appended to the inherited environment.
	Env []string
	// Shell runs Command through a shell instead of exec'ing argv (escape hatch, off by default).
	Shell bool
}

// ActionDef is a named action together with its type and parameters.
type ActionDef struct {
	Name Action
	Type ActionType
	// Exec carries the parameters of an exec action (nil for the built-in power actions).
	Exec *ExecParams
}

// BuiltinActions returns the built-in action definitions.
func BuiltinActions() map[Action]ActionDef {
	return map[Action]ActionDef{
		ActionNoop:     {Name: ActionNoop, Type: ActionTypeNoop},
		ActionSleep:    {Name: ActionSleep, Type: ActionTypeSleep},
		ActionShutdown: {Name: ActionShutdown, Type: ActionTypeShutdown},
		ActionReboot:   {Name: ActionReboot, Type: ActionTypeReboot},
	}
}

// Valid reports whether the action is a built-in action name.
func (a Action) Valid() bool {
	switch a {
	case ActionNoop, ActionSleep, ActionShutdown, ActionReboot:
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
	case "sleep", "suspend", "power.sleep":
		return ActionSleep, nil
	case "noop", "n", "none":
		return ActionNoop, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownAction, s)
	}
}

// Executor performs an action of a given type.
type Executor interface {
	Execute(ctx context.Context, def ActionDef, ev Event) error
}

// Registry maps action names to definitions and action types to executors.
type Registry struct {
	actions   map[Action]ActionDef
	executors map[ActionType]Executor
}

// NewRegistry returns a registry seeded with the built-in actions.
func NewRegistry() *Registry {
	return &Registry{
		actions:   BuiltinActions(),
		executors: make(map[ActionType]Executor),
	}
}

// RegisterAction adds or replaces an action definition.
func (r *Registry) RegisterAction(def ActionDef) {
	r.actions[def.Name] = def
}

// Register binds one executor to one or more action types.
func (r *Registry) Register(executor Executor, types ...ActionType) {
	for _, actionType := range types {
		r.executors[actionType] = executor
	}
}

// Actions returns a copy of the known action definitions.
func (r *Registry) Actions() map[Action]ActionDef {
	return maps.Clone(r.actions)
}

// Action returns the definition registered under name.
func (r *Registry) Action(name Action) (ActionDef, bool) {
	def, ok := r.actions[name]

	return def, ok
}

// Dispatch executes the action named by its definition.
func (r *Registry) Dispatch(ctx context.Context, name Action, ev Event) error {
	def, ok := r.actions[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownActionRef, name)
	}

	executor, ok := r.executors[def.Type]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoExecutor, def.Type)
	}

	return executor.Execute(ctx, def, ev)
}

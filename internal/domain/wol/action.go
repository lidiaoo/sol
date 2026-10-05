package wol

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
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
	ActionTypeHTTP     ActionType = "http"
	ActionTypeSequence ActionType = "sequence"
	ActionTypeSend     ActionType = "wol.send"
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
	// User and Group drop privileges before exec'ing the command; empty values keep
	// the identity of the sol process. Only honored on platforms with setuid support.
	User  string
	Group string
	// Shell runs Command through a shell instead of exec'ing argv (escape hatch, off by default).
	Shell bool
}

// HTTPParams describes an outbound HTTP action (type: http, §18.2).
type HTTPParams struct {
	// Method is the HTTP method; empty means POST.
	Method string
	// URL is the destination; it may interpolate the whitelisted event values.
	URL string
	// Headers are extra request headers; values may interpolate the same values.
	Headers map[string]string
	// Body is the request body template; empty sends no body.
	Body string
	// Timeout bounds one attempt; zero selects the executor default.
	Timeout time.Duration
	// Retries is the number of extra attempts after a failure.
	Retries int
}

// SequenceParams describes a sequence action: an ordered list of other actions.
type SequenceParams struct {
	// Steps are the actions to run, in order. Every step runs even when an earlier
	// one fails (a broken notification must not block a shutdown).
	Steps []Action
}

// SendParams describes a wol.send action: it wakes another machine by transmitting a magic
// packet (§19.13). The target MAC is fixed in the configuration; nothing from the triggering
// packet is interpolated, so a broadcast cannot choose whom to wake.
type SendParams struct {
	// MAC is the target NIC, exactly 6 bytes.
	MAC net.HardwareAddr
	// Broadcast is the destination IPv4 address: a subnet broadcast (192.168.0.255) or a
	// unicast address. Empty means the limited broadcast 255.255.255.255.
	Broadcast string
	// Port is the destination UDP port; the WOL default is 9.
	Port int
	// SecureOn is the optional password the target expects after the magic packet.
	SecureOn []byte
	// Repeat is how many copies to send (a single packet may be lost).
	Repeat int
	// Interval is the gap between two copies.
	Interval time.Duration
}

// ActionDef is a named action together with its type and parameters.
type ActionDef struct {
	Name Action
	Type ActionType
	// Exec carries the parameters of an exec action (nil for the built-in power actions).
	Exec *ExecParams
	// HTTP carries the parameters of an outbound HTTP action.
	HTTP *HTTPParams
	// Sequence carries the steps of a sequence action.
	Sequence *SequenceParams
	// Send carries the parameters of a wol.send action.
	Send *SendParams
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

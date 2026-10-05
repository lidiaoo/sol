// Package sequence runs an ordered list of actions as one action.
package sequence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bavix/sol/internal/domain/wol"
)

var (
	// ErrStepsRequired reports a sequence without any step.
	ErrStepsRequired = errors.New("sequence action requires at least one step")
	// ErrNestedSequence reports a step that is itself a sequence. Nesting is refused
	// on purpose: it makes cycles and deep fan-out possible for no real gain.
	ErrNestedSequence = errors.New("sequence steps may not be sequences")
	// ErrUnknownStep reports a step that no configured or built-in action matches.
	ErrUnknownStep = errors.New("unknown sequence step")
)

// Executor runs the steps of a sequence action through the same registry, so every
// step keeps its own executor, allowlist and audit trail.
type Executor struct {
	registry *wol.Registry
}

func NewExecutor(registry *wol.Registry) *Executor {
	return &Executor{registry: registry}
}

// Validate checks the steps at startup: at least one, all known, none of them another
// sequence (which also rules out cycles and self-reference).
func (e *Executor) Validate(def wol.ActionDef) error {
	params := def.Sequence
	if params == nil || len(params.Steps) == 0 {
		return ErrStepsRequired
	}

	for _, step := range params.Steps {
		target, ok := e.registry.Action(step)
		if !ok {
			return fmt.Errorf("%w: %s", ErrUnknownStep, step)
		}

		if target.Type == wol.ActionTypeSequence {
			return fmt.Errorf("%w: %s", ErrNestedSequence, step)
		}
	}

	return nil
}

// Execute runs every step in order and joins the failures: one broken step must not
// skip the ones behind it (notifying and shutting down are independent duties).
func (e *Executor) Execute(ctx context.Context, def wol.ActionDef, ev wol.Event) error {
	params := def.Sequence
	if params == nil || len(params.Steps) == 0 {
		return fmt.Errorf("%w: action %s", ErrStepsRequired, def.Name)
	}

	failures := make([]error, 0, len(params.Steps))

	for _, step := range params.Steps {
		started := time.Now()

		err := e.registry.Dispatch(ctx, step, ev)

		slog.Info("sequence step finished",
			"sequence", string(def.Name),
			"action", string(step),
			"duration", time.Since(started).String(),
			"failed", err != nil,
		)

		if err != nil {
			failures = append(failures, fmt.Errorf("step %s: %w", step, err))
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("action %s: %w", def.Name, errors.Join(failures...))
	}

	return nil
}

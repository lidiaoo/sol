package sequence_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lidiaoo/sol/internal/domain/wol"
	"github.com/lidiaoo/sol/internal/infra/sequence"
)

// recorder is a stub executor that records the actions it ran.
type recorder struct {
	ran    *[]string
	failed map[wol.Action]bool
}

// errStepFailed makes the stub executor's failing step observable.
var errStepFailed = errors.New("step failed")

func (r *recorder) Execute(_ context.Context, def wol.ActionDef, _ wol.Event) error {
	*r.ran = append(*r.ran, string(def.Name))

	if r.failed[def.Name] {
		return errStepFailed
	}

	return nil
}

func seqRegistry(ran *[]string, failed map[wol.Action]bool) *wol.Registry {
	registry := wol.NewRegistry()
	registry.RegisterAction(wol.ActionDef{Name: "lock", Type: wol.ActionTypeExec})
	registry.Register(
		&recorder{ran: ran, failed: failed},
		wol.ActionTypeExec, wol.ActionTypeNoop, wol.ActionTypeSleep,
		wol.ActionTypeShutdown, wol.ActionTypeReboot,
	)
	registry.Register(sequence.NewExecutor(registry), wol.ActionTypeSequence)

	return registry
}

func sequenceDef(name wol.Action, steps ...wol.Action) wol.ActionDef {
	return wol.ActionDef{Name: name, Type: wol.ActionTypeSequence, Sequence: &wol.SequenceParams{Steps: steps}}
}

func TestSequenceRunsEveryStepInOrder(t *testing.T) {
	t.Parallel()

	ran := []string{}
	registry := seqRegistry(&ran, nil)
	def := sequenceDef("combo", wol.ActionSleep, "lock", wol.ActionShutdown)

	require.NoError(t, sequence.NewExecutor(registry).Validate(def))
	require.NoError(t, sequence.NewExecutor(registry).Execute(context.Background(), def, wol.Event{}))
	require.Equal(t, []string{"power.sleep", "lock", "power.shutdown"}, ran)
}

func TestSequenceKeepsGoingAfterAFailure(t *testing.T) {
	t.Parallel()

	ran := []string{}
	registry := seqRegistry(&ran, map[wol.Action]bool{"lock": true})
	def := sequenceDef("combo", wol.ActionSleep, "lock", wol.ActionShutdown)

	err := sequence.NewExecutor(registry).Execute(context.Background(), def, wol.Event{})

	require.Error(t, err)
	require.ErrorContains(t, err, "step lock: step failed")
	require.Equal(t, []string{"power.sleep", "lock", "power.shutdown"}, ran,
		"a broken notification must not skip the shutdown behind it")
}

func TestSequenceValidate(t *testing.T) {
	t.Parallel()

	ran := []string{}
	registry := seqRegistry(&ran, nil)
	registry.RegisterAction(wol.ActionDef{
		Name:     "nested",
		Type:     wol.ActionTypeSequence,
		Sequence: &wol.SequenceParams{Steps: []wol.Action{"lock"}},
	})

	executor := sequence.NewExecutor(registry)

	require.ErrorIs(t, executor.Validate(sequenceDef("empty")), sequence.ErrStepsRequired)
	require.ErrorIs(t, executor.Validate(sequenceDef("unknown", "nope")), sequence.ErrUnknownStep)
	require.ErrorIs(t, executor.Validate(sequenceDef("nest", "nested")), sequence.ErrNestedSequence)
	require.NoError(t, executor.Validate(sequenceDef("ok", "lock", wol.ActionNoop)))
}

func TestSequenceExecuteRejectsAnEmptyDefinition(t *testing.T) {
	t.Parallel()

	created := []string{}
	executor := sequence.NewExecutor(seqRegistry(&created, nil))

	err := executor.Execute(context.Background(), wol.ActionDef{Name: "empty", Type: wol.ActionTypeSequence}, wol.Event{})

	require.ErrorIs(t, err, sequence.ErrStepsRequired)
	require.Empty(t, created)
}

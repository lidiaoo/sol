package system

import (
	"context"

	"github.com/bavix/sol/internal/domain/wol"
)

// NoopExecutor does nothing; reserved ports and the "noop" action use it.
type NoopExecutor struct{}

func NewNoopExecutor() *NoopExecutor {
	return &NoopExecutor{}
}

func (n *NoopExecutor) Execute(_ context.Context, _ wol.ActionDef, _ wol.Event) error {
	return nil
}

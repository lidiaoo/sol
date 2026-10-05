package deps

import (
	"fmt"
	"sync"

	"github.com/bavix/sol/internal/app"
	"github.com/bavix/sol/internal/config"
	"github.com/bavix/sol/internal/domain/wol"
	"github.com/bavix/sol/internal/infra/network"
	"github.com/bavix/sol/internal/infra/system"
)

type Builder struct {
	cfg *config.Config

	resolverOnce sync.Once
	resolver     app.InterfaceResolver

	factoryOnce sync.Once
	factory     app.PacketListenerFactory

	registryOnce sync.Once
	registry     *wol.Registry

	listenOnce sync.Once
	listen     *app.ListenService
}

func NewBuilder(cfg *config.Config) *Builder {
	return &Builder{cfg: cfg}
}

func (b *Builder) InterfaceResolver() app.InterfaceResolver { //nolint:ireturn
	b.resolverOnce.Do(func() {
		b.resolver = network.NewInterfaceResolver()
	})

	return b.resolver
}

func (b *Builder) PacketListenerFactory() app.PacketListenerFactory { //nolint:ireturn
	b.factoryOnce.Do(func() {
		b.factory = network.NewUDPListenerFactory()
	})

	return b.factory
}

// Registry builds the action registry with the built-in executors.
func (b *Builder) Registry() *wol.Registry {
	b.registryOnce.Do(func() {
		registry := wol.NewRegistry()
		registry.Register(system.NewNoopExecutor(), wol.ActionTypeNoop)
		registry.Register(
			system.NewPowerController(),
			wol.ActionTypeShutdown,
			wol.ActionTypeReboot,
			wol.ActionTypeSleep,
		)

		b.registry = registry
	})

	return b.registry
}

func (b *Builder) BuildListenService() (*app.ListenService, error) {
	var buildErr error

	b.listenOnce.Do(func() {
		ifaces, err := b.InterfaceResolver().Select(b.cfg.InterfaceNames)
		if err != nil {
			buildErr = fmt.Errorf("failed to select interfaces: %w", err)

			return
		}

		registry := b.Registry()

		policy, err := wol.NewRoutingPolicy(b.cfg.Rules, ifaces, wol.PolicyOptions{
			AllowReserved: b.cfg.AllowReservedActions,
			Actions:       registry.Actions(),
		})
		if err != nil {
			buildErr = err

			return
		}

		b.listen = app.NewListenService(
			b.PacketListenerFactory(),
			registry,
			policy,
			ifaces,
			b.cfg.DryRun,
		)
	})

	return b.listen, buildErr
}

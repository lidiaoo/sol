package deps

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bavix/sol/internal/app"
	"github.com/bavix/sol/internal/config"
	"github.com/bavix/sol/internal/domain/wol"
	"github.com/bavix/sol/internal/infra/exec"
	"github.com/bavix/sol/internal/infra/httpapi"
	"github.com/bavix/sol/internal/infra/network"
	"github.com/bavix/sol/internal/infra/outbound"
	"github.com/bavix/sol/internal/infra/system"
)

var (
	errUnknownAuthType = errors.New("unknown http auth type")
	errNoCertificates  = errors.New("http tls: no certificates found")
)

type Builder struct {
	cfg *config.Config

	resolverOnce sync.Once
	resolver     app.InterfaceResolver

	factoryOnce sync.Once
	factory     app.PacketListenerFactory

	registryOnce sync.Once
	registry     *wol.Registry

	execOnce sync.Once
	executor *exec.Executor

	outboundOnce sync.Once
	outbound     *outbound.Executor

	listenOnce sync.Once
	listen     *app.ListenService

	httpOnce   sync.Once
	httpServer *httpapi.Server
	httpErr    error

	// reloader rebuilds and applies the configuration on POST /v1/reload and SIGHUP.
	reloader func(ctx context.Context) error
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

// ExecExecutor returns the custom-command executor, configured with the allowlist.
func (b *Builder) ExecExecutor() *exec.Executor {
	b.execOnce.Do(func() {
		b.executor = exec.NewExecutor(b.cfg.ExecAllowlist)
	})

	return b.executor
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
		registry.Register(b.ExecExecutor(), wol.ActionTypeExec)
		registry.Register(b.HTTPExecutor(), wol.ActionTypeHTTP)

		// Named actions from the configuration (built-in names carry identical definitions).
		for _, def := range b.cfg.Actions {
			registry.RegisterAction(def)
		}

		// Whitelisted remote commands (§21) are ordinary exec actions named
		// "remote:<id>", so cooldowns, audit logging and manual triggers apply.
		for _, cmd := range b.cfg.Remote.Commands {
			params := cmd.Exec

			registry.RegisterAction(wol.ActionDef{
				Name: cmd.Action(),
				Type: wol.ActionTypeExec,
				Exec: &params,
			})
		}

		b.registry = registry
	})

	return b.registry
}

func (b *Builder) BuildListenService() (*app.ListenService, error) {
	var buildErr error

	b.listenOnce.Do(func() {
		registry, policy, ifaces, err := b.buildRuntime()
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
		).WithCooldowns(b.cfg.Cooldown, cooldownWindows(b.cfg.ActionCooldowns)).
			WithRemoteCommands(b.cfg.Remote.Commands, b.cfg.Remote.Ports, b.cfg.Remote.HMACKey)
	})

	return b.listen, buildErr
}

// WithReloader installs the callback that rebuilds the configuration; it backs both
// POST /v1/reload and the SIGHUP handler.
func (b *Builder) WithReloader(fn func(ctx context.Context) error) *Builder {
	b.reloader = fn

	return b
}

// ReloadOptions assembles the runtime pieces of the current configuration for a
// running listener (see ListenService.Reload).
func (b *Builder) ReloadOptions() (app.ReloadOptions, error) {
	registry, policy, _, err := b.buildRuntime()
	if err != nil {
		return app.ReloadOptions{}, err
	}

	return app.ReloadOptions{
		Policy:      policy,
		Registry:    registry,
		DryRun:      b.cfg.DryRun,
		Cooldown:    b.cfg.Cooldown,
		Cooldowns:   cooldownWindows(b.cfg.ActionCooldowns),
		Commands:    b.cfg.Remote.Commands,
		RemotePorts: b.cfg.Remote.Ports,
		RemoteKey:   b.cfg.Remote.HMACKey,
	}, nil
}

// buildRuntime validates the configuration and assembles the rule set, the action
// registry and the interfaces it resolves to.

// cooldownWindows converts the per-action cooldown overrides into plain strings.
func cooldownWindows(perAction map[wol.Action]time.Duration) map[string]time.Duration {
	windows := make(map[string]time.Duration, len(perAction))

	for action, window := range perAction {
		windows[string(action)] = window
	}

	return windows
}

// BuildHTTPServer builds the optional control plane; it returns nil when disabled.
func (b *Builder) BuildHTTPServer() (*httpapi.Server, error) {
	b.httpOnce.Do(func() {
		if !b.cfg.HTTP.Enabled {
			return
		}

		auth, err := httpAuth(b.cfg.HTTP)
		if err != nil {
			b.httpErr = err

			return
		}

		tlsConfig, err := httpTLS(b.cfg.HTTP)
		if err != nil {
			b.httpErr = err

			return
		}

		listenSvc, err := b.BuildListenService()
		if err != nil {
			b.httpErr = err

			return
		}

		b.httpServer = httpapi.New(
			httpapi.Config{Listen: b.cfg.HTTP.Listen, Auth: auth, TLS: tlsConfig},
			httpapi.Deps{
				Status:     b.statusFunc(listenSvc),
				Rules:      listenSvc.Rules,
				Interfaces: listenSvc.Interfaces,
				Dispatch: func(ctx context.Context, action wol.Action) error {
					err := listenSvc.Dispatch(ctx, action, wol.Event{})
					if errors.Is(err, app.ErrActionSuppressed) {
						return fmt.Errorf("%w: %s", httpapi.ErrSuppressed, action)
					}

					return err
				},
				RunCommand: func(ctx context.Context, id string, args map[string]string) error {
					return remoteCommandError(listenSvc.RunRemoteCommand(ctx, id, args), id)
				},
				Reload: b.reloadFunc(),
			},
		)
	})

	return b.httpServer, b.httpErr
}

// remoteCommandError translates the app/domain remote-command failures into the
// httpapi sentinels the control plane maps onto status codes.
func remoteCommandError(err error, id string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, app.ErrRemoteUnknownCommand):
		return fmt.Errorf("%w: %s", httpapi.ErrCommandNotFound, id)
	case errors.Is(err, app.ErrRemoteDisabled), errors.Is(err, wol.ErrRemoteSignature):
		return fmt.Errorf("%w: %w", httpapi.ErrCommandForbidden, err)
	case remoteArgError(err):
		return fmt.Errorf("%w: %w", httpapi.ErrCommandArgs, err)
	default:
		return err
	}
}

// remoteArgError reports whether err is one of the remote argument validation failures.
func remoteArgError(err error) bool {
	targets := []error{
		wol.ErrRemoteSegmentFormat,
		wol.ErrRemoteSegmentTooLong,
		wol.ErrRemoteUnknownArg,
		wol.ErrRemoteMissingArg,
		wol.ErrRemoteArgType,
		wol.ErrRemoteArgValue,
	}

	for _, target := range targets {
		if errors.Is(err, target) {
			return true
		}
	}

	return false
}

// HTTPExecutor returns the outbound HTTP executor, restricted by security.url_allowlist.
func (b *Builder) HTTPExecutor() *outbound.Executor {
	b.outboundOnce.Do(func() {
		b.outbound = outbound.NewExecutor(b.cfg.URLAllowlist)
	})

	return b.outbound
}

// buildRuntime validates the configuration and assembles the rule set, the action
// registry and the interfaces it resolves to.
func (b *Builder) buildRuntime() (*wol.Registry, *wol.RoutingPolicy, []wol.IfaceInfo, error) {
	ifaces, err := b.InterfaceResolver().Select(b.cfg.InterfaceNames)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to select interfaces: %w", err)
	}

	registry := b.Registry()

	if err := b.validateActions(registry); err != nil {
		return nil, nil, nil, err
	}

	policy, err := wol.NewRoutingPolicy(b.cfg.Rules, ifaces, wol.PolicyOptions{
		ReservedPorts: b.cfg.ReservedPorts,
		AllowReserved: b.cfg.AllowReservedActions,
		SecureOn:      b.cfg.SecureOn,
		Actions:       registry.Actions(),
		ExtraPorts:    b.cfg.Remote.Ports,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	return registry, policy, ifaces, nil
}

// reloadFunc translates the reload failures into the httpapi sentinels the control
// plane maps onto status codes; it is nil when no reloader was installed.
func (b *Builder) reloadFunc() func(ctx context.Context) error {
	if b.reloader == nil {
		return nil
	}

	return func(ctx context.Context) error {
		err := b.reloader(ctx)
		if errors.Is(err, app.ErrReloadRestartRequired) {
			return fmt.Errorf("%w: %w", httpapi.ErrRestartRequired, err)
		}

		return err
	}
}

// validateActions statically checks every configured exec/http action at startup.
func (b *Builder) validateActions(registry *wol.Registry) error {
	executor := b.ExecExecutor()
	outboundExecutor := b.HTTPExecutor()

	for _, def := range registry.Actions() {
		switch def.Type {
		case wol.ActionTypeExec:
			if err := executor.Validate(def); err != nil {
				return fmt.Errorf("exec action %s: %w", def.Name, err)
			}
		case wol.ActionTypeHTTP:
			if err := outboundExecutor.Validate(def); err != nil {
				return fmt.Errorf("http action %s: %w", def.Name, err)
			}
		case wol.ActionTypeNoop, wol.ActionTypeSleep, wol.ActionTypeShutdown, wol.ActionTypeReboot:
			// built-in power actions carry no parameters to validate
		}
	}

	return nil
}

func (b *Builder) statusFunc(svc *app.ListenService) func() httpapi.Status {
	return func() httpapi.Status {
		stats := svc.Stats()
		uptime := time.Since(stats.StartedAt)

		names := make([]string, 0, len(svc.Interfaces()))
		for _, iface := range svc.Interfaces() {
			names = append(names, iface.Name)
		}

		status := httpapi.Status{
			Uptime:      uptime.Truncate(time.Second).String(),
			UptimeSecs:  uptime.Seconds(),
			Packets:     stats.Packets,
			Matched:     stats.Matched,
			Suppressed:  stats.Suppressed,
			Actions:     stats.Actions,
			Rules:       len(svc.Rules()),
			Interfaces:  names,
			DryRun:      b.cfg.DryRun,
			AuthType:    b.cfg.HTTP.AuthType,
			HTTPAddress: b.cfg.HTTP.Listen,
		}

		if stats.LastEvent != nil {
			status.LastEvent = &httpapi.Event{
				Time:      stats.LastEvent.Time,
				Src:       stats.LastEvent.Src,
				Port:      stats.LastEvent.Port,
				Interface: stats.LastEvent.Interface,
				TargetMAC: stats.LastEvent.TargetMAC,
				Action:    stats.LastEvent.Action,
				DryRun:    stats.LastEvent.DryRun,
			}
		}

		return status
	}
}

func httpAuth(cfg config.HTTP) (httpapi.Authenticator, error) {
	switch cfg.AuthType {
	case config.AuthTypeBearer:
		return httpapi.BearerAuth(cfg.Token), nil
	case config.AuthTypeBasic:
		return httpapi.BasicAuth(cfg.User, cfg.Password), nil
	case config.AuthTypeMTLS:
		return httpapi.MTLSAuth(), nil
	default:
		return nil, fmt.Errorf("%w: %q", errUnknownAuthType, cfg.AuthType)
	}
}

func httpTLS(cfg config.HTTP) (*tls.Config, error) {
	if cfg.CertFile == "" && cfg.ClientCAFile == "" {
		//nolint:nilnil // nil means the control plane serves plain HTTP.
		return nil, nil
	}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}

	if cfg.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("http tls: %w", err)
		}

		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	if cfg.ClientCAFile != "" {
		pool, err := loadCertPool(cfg.ClientCAFile)
		if err != nil {
			return nil, err
		}

		tlsConfig.ClientCAs = pool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return tlsConfig, nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("http tls: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%w in %s", errNoCertificates, path)
	}

	return pool, nil
}

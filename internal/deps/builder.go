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

	listenOnce sync.Once
	listen     *app.ListenService

	httpOnce   sync.Once
	httpServer *httpapi.Server
	httpErr    error
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

		// Named actions from the configuration (built-in names carry identical definitions).
		for _, def := range b.cfg.Actions {
			registry.RegisterAction(def)
		}

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

		if err := b.validateExecActions(registry); err != nil {
			buildErr = err

			return
		}

		policy, err := wol.NewRoutingPolicy(b.cfg.Rules, ifaces, wol.PolicyOptions{
			ReservedPorts: b.cfg.ReservedPorts,
			AllowReserved: b.cfg.AllowReservedActions,
			SecureOn:      b.cfg.SecureOn,
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
		).WithCooldowns(b.cfg.Cooldown, cooldownWindows(b.cfg.ActionCooldowns))
	})

	return b.listen, buildErr
}

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
			},
		)
	})

	return b.httpServer, b.httpErr
}

// validateExecActions statically checks every configured exec action at startup.
func (b *Builder) validateExecActions(registry *wol.Registry) error {
	executor := b.ExecExecutor()

	for _, def := range registry.Actions() {
		if def.Type != wol.ActionTypeExec {
			continue
		}

		if err := executor.Validate(def); err != nil {
			return fmt.Errorf("exec action %s: %w", def.Name, err)
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

package deps

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bavix/sol/internal/app"
	"github.com/bavix/sol/internal/buildinfo"
	"github.com/bavix/sol/internal/config"
	"github.com/bavix/sol/internal/domain/wol"
	"github.com/bavix/sol/internal/infra/exec"
	"github.com/bavix/sol/internal/infra/httpapi"
	"github.com/bavix/sol/internal/infra/network"
	"github.com/bavix/sol/internal/infra/outbound"
	"github.com/bavix/sol/internal/infra/sequence"
	"github.com/bavix/sol/internal/infra/system"
	"github.com/bavix/sol/internal/infra/wolsend"
)

var (
	errUnknownAuthType = errors.New("unknown http auth type")
	errNoCertificates  = errors.New("http tls: no certificates found")
)

type Builder struct {
	cfg *config.Config

	// rejections counts the authenticated packets refused for replay reasons (§19.16). The
	// policy reports them through OnAuthRejected, so they live beside the policy rather than in
	// the listener, and both /v1/status and /metrics read them from here.
	rejections replayCounters

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

	// sequencer runs sequence steps through the registry; it is built with the registry
	// itself because the two reference each other.
	sequencer  *sequence.Executor
	sender     *wolsend.Executor
	senderOnce sync.Once

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
		registry.Register(b.Sender(), wol.ActionTypeSend)

		// The sequence executor dispatches through this very registry, so it is built
		// here instead of in a helper that would re-enter this sync.Once.
		b.sequencer = sequence.NewExecutor(registry)
		registry.Register(b.sequencer, wol.ActionTypeSequence)

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
			WithRateLimit(b.cfg.RateLimit, b.cfg.RateBurst).
			WithRemoteCommands(app.RemoteSettings{
				Commands: b.cfg.Remote.Commands,
				Ports:    b.cfg.Remote.Ports,
				Key:      b.cfg.Remote.HMACKey,
				Window:   b.cfg.Remote.Window,
				OnReject: func(reason string) { b.reportRefusal(reason, "command") },
			}).
			WithRawShell(rawShellSettings(b.cfg.Remote.RawShell, func(reason string) {
				b.reportRefusal(reason, "raw_shell")
			}))

		b.warnRawShell()
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
	registry, policy, ifaces, err := b.buildRuntime()
	if err != nil {
		return app.ReloadOptions{}, err
	}

	return app.ReloadOptions{
		Policy:       policy,
		Registry:     registry,
		Ifaces:       ifaces,
		DryRun:       b.cfg.DryRun,
		Cooldown:     b.cfg.Cooldown,
		Cooldowns:    cooldownWindows(b.cfg.ActionCooldowns),
		RateLimit:    b.cfg.RateLimit,
		RateBurst:    b.cfg.RateBurst,
		Commands:     b.cfg.Remote.Commands,
		RemotePorts:  b.cfg.Remote.Ports,
		RemoteKey:    b.cfg.Remote.HMACKey,
		RemoteWindow: b.cfg.Remote.Window,
	}, nil
}

// rawShellSettings converts the resolved raw shell configuration into the form the listen
// service takes.
func rawShellSettings(cfg config.RawShell, onReject func(string)) app.RawShellSettings {
	return app.RawShellSettings{
		Enabled:   cfg.Enabled,
		Ports:     cfg.Ports,
		Key:       cfg.Key,
		SrcNets:   cfg.SrcNets,
		Allowlist: cfg.Allowlist,
		Exec:      cfg.Exec,
		Window:    cfg.Window,
		OnReject:  onReject,
	}
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
				Dispatch:   dispatchFunc(listenSvc),
				RunShell: func(ctx context.Context, command string, srcIP net.IP) error {
					return shellError(listenSvc.RunRawShell(ctx, app.RemoteShellRequest{
						Command: command,
						SrcIP:   srcIP,
					}))
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

// dispatchFunc triggers a named action from the control plane and translates the guardrail
// refusals into the sentinels the HTTP layer answers 429 with.
func dispatchFunc(service *app.ListenService) func(context.Context, wol.Action) error {
	return func(ctx context.Context, action wol.Action) error {
		err := service.Dispatch(ctx, action, wol.Event{})

		switch {
		case errors.Is(err, app.ErrActionSuppressed):
			return fmt.Errorf("%w: %s", httpapi.ErrSuppressed, action)
		case errors.Is(err, app.ErrActionRateLimited):
			return fmt.Errorf("%w: %s", httpapi.ErrRateLimited, action)
		case errors.Is(err, app.ErrActionInFlight):
			return fmt.Errorf("%w: %s", httpapi.ErrInFlight, action)
		default:
			return err
		}
	}
}

// shellError translates a raw shell failure into the httpapi sentinels the control plane maps
// onto status codes (§21.6).
func shellError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, app.ErrRawShellDisabled):
		return fmt.Errorf("%w: %w", httpapi.ErrShellDisabled, err)
	case errors.Is(err, app.ErrRawShellSource), errors.Is(err, wol.ErrRawShellNotAllowed),
		errors.Is(err, wol.ErrRawShellEmpty), errors.Is(err, wol.ErrRawShellTooLong):
		return fmt.Errorf("%w: %w", httpapi.ErrShellForbidden, err)
	case errors.Is(err, app.ErrActionSuppressed):
		return fmt.Errorf("%w: %w", httpapi.ErrSuppressed, err)
	case errors.Is(err, app.ErrActionRateLimited):
		return fmt.Errorf("%w: %w", httpapi.ErrRateLimited, err)
	case errors.Is(err, app.ErrActionInFlight):
		return fmt.Errorf("%w: %w", httpapi.ErrInFlight, err)
	default:
		return err
	}
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
	case errors.Is(err, app.ErrActionSuppressed):
		return fmt.Errorf("%w: %w", httpapi.ErrSuppressed, err)
	case errors.Is(err, app.ErrActionRateLimited):
		return fmt.Errorf("%w: %w", httpapi.ErrRateLimited, err)
	case errors.Is(err, app.ErrActionInFlight):
		return fmt.Errorf("%w: %w", httpapi.ErrInFlight, err)
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

// SequenceExecutor returns the executor that runs sequence steps through the registry.
func (b *Builder) SequenceExecutor() *sequence.Executor {
	b.Registry()

	return b.sequencer
}

// Sender returns the executor that transmits Wake-on-LAN magic packets (§19.13). It is
// stateless, so one instance serves every wol.send action.
func (b *Builder) Sender() *wolsend.Executor {
	b.senderOnce.Do(func() {
		b.sender = wolsend.NewExecutor(
			wolsend.WithPacketKey(b.cfg.PacketKey),
			wolsend.WithPacketWindow(b.cfg.PacketWindow),
		)
	})

	return b.sender
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
		ExtraPorts:    append(slices.Clone(b.cfg.Remote.Ports), b.cfg.Remote.RawShell.Ports...),
		PacketKey:     b.cfg.PacketKey,
		PacketWindow:  b.cfg.PacketWindow,
		OnAuthRejected: func(reason string) {
			b.reportRefusal(reason, "packet")
		},
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
		if errors.Is(err, app.ErrReloadBind) {
			return fmt.Errorf("%w: %w", httpapi.ErrRestartRequired, err)
		}

		return err
	}
}

// validateActions statically checks every configured exec/http action at startup, and
// the outbound allowlist itself so a malformed entry fails the start-up even when no http
// action is configured yet.
func (b *Builder) validateActions(registry *wol.Registry) error {
	if err := outbound.ValidateAllowlist(b.cfg.URLAllowlist); err != nil {
		return err
	}

	// One validator per type, so a new action type is a single line here.
	validators := map[wol.ActionType]actionValidator{
		wol.ActionTypeExec:     {"exec action", b.ExecExecutor().Validate},
		wol.ActionTypeHTTP:     {"http action", b.HTTPExecutor().Validate},
		wol.ActionTypeSequence: {"sequence action", b.SequenceExecutor().Validate},
		wol.ActionTypeSend:     {"wol.send action", b.Sender().Validate},
	}

	for _, def := range registry.Actions() {
		validator, known := validators[def.Type]
		if !known {
			// The built-in power actions carry no parameters to validate.
			continue
		}

		if err := validator.check(def); err != nil {
			return fmt.Errorf("%s %s: %w", validator.label, def.Name, err)
		}
	}

	return b.validateRawShell()
}

// validateRawShell checks the execution settings of the raw shell channel (§21.6) at start-up:
// the timeout, and the privilege drop when one is configured. The command itself arrives with
// the packet, so only the settings can be checked here.
func (b *Builder) validateRawShell() error {
	raw := b.cfg.Remote.RawShell
	if !raw.Enabled {
		return nil
	}

	def := wol.ActionDef{Name: wol.RawShellAction, Type: wol.ActionTypeExec, Exec: &raw.Exec}
	if err := b.ExecExecutor().Validate(def); err != nil {
		return fmt.Errorf("security.allow_raw_shell: %w", err)
	}

	return nil
}

// actionValidator pairs the error label of a type with its start-up check.
type actionValidator struct {
	label string
	check func(wol.ActionDef) error
}

// replayCounters counts the rejected authenticated packets per reason. The three reasons are
// the ones the policy can report, so a new one shows up as a compile-time reminder here.
type replayCounters struct {
	stale  atomic.Uint64
	seen   atomic.Uint64
	unsent atomic.Uint64
}

// reportRefusal records one refused authenticated payload: the counter for the status view, and
// a log line naming the reason and the channel it came from (a packet or a command segment).
func (b *Builder) reportRefusal(reason string, channel string) {
	b.rejections.add(reason)
	slog.Warn("authenticated packet refused", "reason", reason, "channel", channel)
}

func (c *replayCounters) add(reason string) {
	switch reason {
	case wol.ReplayStale:
		c.stale.Add(1)
	case wol.ReplaySeen:
		c.seen.Add(1)
	case wol.ReplayFull:
		c.unsent.Add(1)
	}
}

// total is what /metrics needs: one counter for "the protection refused a packet".
func (c *replayCounters) total() uint64 {
	return c.stale.Load() + c.seen.Load() + c.unsent.Load()
}

// reasons is the per-reason breakdown; it stays nil while nothing has been refused, so the
// status view does not carry a map of zeroes.
func (c *replayCounters) reasons() map[string]uint64 {
	if c.total() == 0 {
		return nil
	}

	return map[string]uint64{
		wol.ReplayStale: c.stale.Load(),
		wol.ReplaySeen:  c.seen.Load(),
		wol.ReplayFull:  c.unsent.Load(),
	}
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
			Version:       buildinfo.Version(),
			Revision:      buildinfo.Revision(),
			Uptime:        uptime.Truncate(time.Second).String(),
			UptimeSecs:    uptime.Seconds(),
			Packets:       stats.Packets,
			Matched:       stats.Matched,
			Suppressed:    stats.Suppressed,
			RateLimited:   stats.RateLimited,
			Inflight:      stats.Inflight,
			Replayed:      b.rejections.total(),
			ReplayReasons: b.rejections.reasons(),
			Actions:       stats.Actions,
			Rules:         len(svc.Rules()),
			Interfaces:    names,
			DryRun:        b.cfg.DryRun,
			AuthType:      b.cfg.HTTP.AuthType,
			HTTPAddress:   b.cfg.HTTP.Listen,
		}

		if rate, burst := svc.RateLimit(); rate > 0 {
			status.RateLimit = &httpapi.RateLimitView{PerSecond: rate, Burst: burst}
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

// warnRawShell prints the §21.6 warning: an enabled raw shell means any sender holding the key
// can run shell commands on this machine.
func (b *Builder) warnRawShell() {
	raw := b.cfg.Remote.RawShell
	if !raw.Enabled {
		return
	}

	slog.Warn("raw shell enabled: remote senders can run shell commands",
		"ports", raw.Ports,
		"src_cidrs", len(raw.SrcNets),
		"allowlist_entries", len(raw.Allowlist),
		"user", raw.Exec.User,
		"group", raw.Exec.Group,
	)
}

package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/bavix/sol/internal/domain/wol"
)

var (
	// ErrRawShellDisabled reports a raw shell attempt while the channel is off.
	ErrRawShellDisabled = errors.New("raw shell is disabled")
	// ErrRawShellSource reports a sender outside raw_shell_src_cidrs.
	ErrRawShellSource = errors.New("raw shell sender is outside raw_shell_src_cidrs")
)

// RawShellSettings is the resolved raw shell configuration handed to the listener (§21.6).
type RawShellSettings struct {
	Enabled   bool
	Ports     []int
	Key       []byte
	SrcNets   []*net.IPNet
	Allowlist []*regexp.Regexp
	Exec      wol.ExecParams
	// Window, when positive, turns on replay protection for the channel (§21.3): commands then
	// carry a stamp, and each tag is accepted once.
	Window time.Duration
	// OnReject reports a refused attempt with its reason; nil means "log nothing extra".
	OnReject func(reason string)
}

// rawShellRunner carries the raw shell transport of §21.6: a dedicated port set, its own HMAC
// key, the optional source networks and allowlist, and the execution settings. It is nil unless
// the operator enabled the channel; accepts then always reports false.
type rawShellRunner struct {
	key       []byte
	guard     *wol.ReplayGuard
	now       func() time.Time
	ports     map[int]bool
	srcNets   []*net.IPNet
	allowlist []*regexp.Regexp
	exec      wol.ExecParams
}

func newRawShellRunner(enabled bool, ports []int, key []byte, srcNets []*net.IPNet,
	allowlist []*regexp.Regexp, exec wol.ExecParams, window time.Duration, onReject func(string),
) *rawShellRunner {
	if !enabled || len(ports) == 0 {
		return nil
	}

	runner := &rawShellRunner{
		key:       key,
		guard:     wol.NewReplayGuard(window, onReject),
		now:       time.Now,
		ports:     make(map[int]bool, len(ports)),
		srcNets:   srcNets,
		allowlist: allowlist,
		exec:      exec,
	}

	for _, port := range ports {
		runner.ports[port] = true
	}

	return runner
}

// split verifies the command segment, and refuses a replayed one when the channel has a window.
func (r *rawShellRunner) split(prefix []byte, content []byte) ([]byte, error) {
	if r.guard == nil {
		return wol.SplitRemoteContent(r.key, prefix, content)
	}

	segment, stamp, tag, err := wol.SplitTimestampedRemoteContent(r.key, prefix, content)
	if err != nil {
		return nil, err
	}

	if !r.guard.Accept(tag, stamp, r.now()) {
		return nil, ErrRemoteReplay
	}

	return segment, nil
}

// accepts reports whether the port may carry a raw shell command.
func (r *rawShellRunner) accepts(port int) bool {
	return r != nil && r.ports[port]
}

// command turns a packet's content region into the shell command to run: the signature is
// verified against the packet prefix and the command is checked against the allowlist.
func (r *rawShellRunner) command(prefix []byte, content []byte) (string, error) {
	segment, err := r.split(prefix, content)
	if err != nil {
		return "", err
	}

	command := string(segment)
	if err := wol.ValidateRawShellCommand(r.allowlist, command); err != nil {
		return "", err
	}

	return command, nil
}

// allowed reports whether a sender may use the channel at all.
func (r *rawShellRunner) allowed(src net.IP) bool {
	if len(r.srcNets) == 0 {
		return true
	}

	for _, entry := range r.srcNets {
		if entry.Contains(src) {
			return true
		}
	}

	return false
}

// params fills the per-packet command into the configured execution settings.
func (r *rawShellRunner) params(command string) *wol.ExecParams {
	params := r.exec
	params.Command = []string{command}
	params.Shell = true

	return &params
}

// handleRawShell consumes a raw shell packet (§21.6) and reports whether it was handled. Such a
// packet is never routed to the rules, so a malformed command cannot trigger a destructive
// action, and every refusal is logged with its reason.
func (s *ListenService) handleRawShell(ctx context.Context, rt routingSnapshot, pkt packet, ev wol.Event) bool {
	runner := rt.rawShell

	parsed, ok := rt.policy.ParsePacket(pkt.payload)
	if !ok || len(parsed.Content) == 0 {
		return false
	}

	prefix := pkt.payload[:len(pkt.payload)-len(parsed.Content)]

	command, err := runner.command(prefix, parsed.Content)
	if err != nil {
		slog.Warn("raw shell rejected",
			"src", addrString(pkt.src),
			"port", pkt.port,
			"error", err,
		)

		return true
	}

	if !runner.allowed(sourceIP(pkt.src)) {
		slog.Warn("raw shell rejected",
			"src", addrString(pkt.src),
			"port", pkt.port,
			"error", ErrRawShellSource,
		)

		return true
	}

	ev.Interface = rt.policy.InterfaceForMAC(parsed.MAC)
	ev.TargetMAC = parsed.MAC
	ev.Args = nil

	s.matched.Add(1)

	if err := s.runRawShell(ctx, rt, pkt, ev, command); err != nil {
		// A guardrail refused it: allowAction already counted and logged which one, so the packet
		// path only has to drop it. The HTTP transport is the one that needs the reason back.
		slog.Debug("raw shell command dropped", "error", err)
	}

	return true
}

// runRawShell applies the guardrails and runs one remote shell command with the same dry-run,
// audit and counter treatment as a routed action. The command itself is logged, which is the
// point of the channel: an operator has to be able to see what ran.
func (s *ListenService) runRawShell(ctx context.Context, rt routingSnapshot, pkt packet, ev wol.Event, command string) error {
	logOnly := rt.dryRun
	trigger := ternary(logOnly, "DRY-RUN", string(wol.RawShellAction))

	s.recordEvent(EventRecord{
		Time:      time.Now(),
		Src:       addrString(pkt.src),
		Port:      pkt.port,
		Interface: ev.Interface,
		TargetMAC: ev.TargetMAC.String(),
		Action:    string(wol.RawShellAction),
		DryRun:    logOnly,
	})

	slog.Info("raw shell command received",
		"src", addrString(pkt.src),
		"port", pkt.port,
		"interface", ev.Interface,
		"action", string(wol.RawShellAction),
		"command", command,
		"trigger", trigger,
	)

	if logOnly {
		return nil
	}

	release, err := s.allowAction(rt, wol.RawShellAction, runKey(string(wol.RawShellAction), ev, command))
	if err != nil {
		// allowAction already counted and logged which guard stopped it; the caller decides what
		// the refusal means - the packet path drops it, the HTTP transport answers 429.
		return err
	}

	defer release()

	def := wol.ActionDef{Name: wol.RawShellAction, Type: wol.ActionTypeExec, Exec: rt.rawShell.params(command)}

	if err := rt.registry.DispatchDef(ctx, def, ev); err != nil {
		slog.Error("action failed", "action", string(wol.RawShellAction), "error", err)

		return err
	}

	s.recordAction(string(wol.RawShellAction))

	return nil
}

// RunRawShell runs a shell command that arrived over the authenticated HTTP transport
// (POST /v1/exec). It reports an error the control plane maps onto a status code.
func (s *ListenService) RunRawShell(ctx context.Context, request RemoteShellRequest) error {
	rt := s.snapshot()

	if rt.rawShell == nil {
		return ErrRawShellDisabled
	}

	command := strings.TrimSpace(request.Command)
	if err := wol.ValidateRawShellCommand(rt.rawShell.allowlist, command); err != nil {
		slog.Warn("raw shell rejected", "transport", "http", "error", err)

		return err
	}

	if !rt.rawShell.allowed(request.SrcIP) {
		slog.Warn("raw shell rejected", "transport", "http", "error", ErrRawShellSource)

		return ErrRawShellSource
	}

	s.matched.Add(1)

	return s.runRawShell(ctx, rt, packet{src: addrFromIP(request.SrcIP)}, wol.Event{SrcIP: request.SrcIP}, command)
}

// RemoteShellRequest is one POST /v1/exec body after decoding.
type RemoteShellRequest struct {
	Command string
	SrcIP   net.IP
}

// sourceIP returns the sender's IP, or nil when the packet carries none.
func sourceIP(addr *net.UDPAddr) net.IP {
	if addr == nil {
		return nil
	}

	return addr.IP
}

func addrFromIP(ip net.IP) *net.UDPAddr {
	if ip == nil {
		return nil
	}

	return &net.UDPAddr{IP: ip}
}

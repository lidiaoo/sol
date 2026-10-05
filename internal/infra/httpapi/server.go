// Package httpapi implements the optional HTTP control plane (§18.1).
//
// It is deliberately stdlib-only: net/http with method patterns, no framework.
package httpapi

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bavix/sol/internal/domain/wol"
)

const (
	readHeaderTimeout = 10 * time.Second
	writeTimeout      = 30 * time.Second
	actionTimeout     = 30 * time.Second
)

// ErrSuppressed reports that an action was rate-limited by its cooldown.
var ErrSuppressed = errors.New("action suppressed by cooldown")

// ErrRateLimited reports that an action was dropped by the global rate limit.
var ErrRateLimited = errors.New("action suppressed by rate limit")

// ErrRestartRequired reports a reload that cannot be applied to the running process
// (for example a changed listening port set) and needs a restart instead.
var ErrRestartRequired = errors.New("restart required")

// Event is the most recent matched packet, as exposed by /v1/status.
type Event struct {
	Time      time.Time `json:"time"`
	Src       string    `json:"src"`
	Port      int       `json:"port"`
	Interface string    `json:"interface,omitempty"`
	TargetMAC string    `json:"target_mac,omitempty"`
	Action    string    `json:"action"`
	DryRun    bool      `json:"dry_run"`
}

// RateLimitView reports the live global token bucket.
type RateLimitView struct {
	PerSecond float64 `json:"per_second"`
	Burst     int     `json:"burst"`
}

// Status is the /v1/status payload.
type Status struct {
	Uptime      string            `json:"uptime"`
	UptimeSecs  float64           `json:"uptime_seconds"`
	Packets     uint64            `json:"packets"`
	Matched     uint64            `json:"matched"`
	Suppressed  uint64            `json:"suppressed"`
	RateLimited uint64            `json:"rate_limited"`
	RateLimit   *RateLimitView    `json:"rate_limit,omitempty"`
	Actions     map[string]uint64 `json:"actions"`
	Rules       int               `json:"rules"`
	Interfaces  []string          `json:"interfaces"`
	LastEvent   *Event            `json:"last_event,omitempty"`
	DryRun      bool              `json:"dry_run"`
	AuthType    string            `json:"auth_type"`
	HTTPAddress string            `json:"http_listen"`
}

// Deps are the read-only views and callbacks the control plane needs.
type Deps struct {
	Status     func() Status
	Rules      func() []wol.Rule
	Interfaces func() []wol.IfaceInfo
	// Dispatch triggers a named action; it must return wol.ErrUnknownActionRef for unknown names.
	Dispatch func(ctx context.Context, action wol.Action) error
	// RunCommand invokes a whitelisted remote command (§21); it must report
	// ErrCommandNotFound, ErrCommandArgs or ErrCommandForbidden for the usual failures.
	RunCommand func(ctx context.Context, id string, args map[string]string) error
	// Reload rebuilds the configuration from disk and applies it to the running
	// listener; it must report ErrRestartRequired when the change cannot be applied
	// without restarting. A nil callback keeps POST /v1/reload answering 501.
	Reload func(ctx context.Context) error
}

var (
	// ErrCommandNotFound reports a command id that is not whitelisted.
	ErrCommandNotFound = errors.New("unknown remote command")
	// ErrCommandArgs reports rejected remote command arguments.
	ErrCommandArgs = errors.New("invalid remote command arguments")
	// ErrCommandForbidden reports a remote command refused by policy (disabled, bad signature).
	ErrCommandForbidden = errors.New("remote command forbidden")
)

// Authenticator validates an incoming request.
type Authenticator interface {
	Authenticate(r *http.Request) bool
	// Challenge is the WWW-Authenticate value sent with a 401.
	Challenge() string
	// Type names the auth scheme for logs and status output.
	Type() string
}

// Config configures the control-plane server.
type Config struct {
	Listen string
	// Auth is required; the caller must never serve /v1 without one.
	Auth Authenticator
	// TLS is optional; when set the server serves HTTPS with these settings.
	TLS *tls.Config
}

// Server is the HTTP control plane.
type Server struct {
	cfg    Config
	deps   Deps
	mux    *http.ServeMux
	server *http.Server
}

func New(cfg Config, deps Deps) *Server {
	srv := &Server{cfg: cfg, deps: deps, mux: http.NewServeMux()}
	srv.routes()

	srv.server = &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.mux,
		TLSConfig:         cfg.TLS,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
	}

	return srv
}

// Handler exposes the mux (used by tests).
func (s *Server) Handler() http.Handler {
	return s.mux
}

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		defer cancel()

		_ = s.server.Shutdown(shutdownCtx)
	}()

	scheme := "http"
	if s.cfg.TLS != nil {
		scheme = "https"
	}

	slog.Info("http control plane listening", "scheme", scheme, "listen", s.cfg.Listen, "auth", s.cfg.Auth.Type())

	var err error
	if s.cfg.TLS != nil {
		// Certificates and client CAs live in TLSConfig, so the file arguments stay empty.
		err = s.server.ListenAndServeTLS("", "")
	} else {
		err = s.server.ListenAndServe()
	}

	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return err
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.Handle("GET /v1/status", s.guard(s.handleStatus))
	s.mux.Handle("GET /v1/rules", s.guard(s.handleRules))
	s.mux.Handle("GET /v1/interfaces", s.guard(s.handleInterfaces))
	s.mux.Handle("POST /v1/actions/{name}", s.guard(s.handleAction))
	s.mux.Handle("POST /v1/commands/{id}", s.guard(s.handleCommand))
	s.mux.Handle("POST /v1/reload", s.guard(s.handleReload))
	s.mux.Handle("GET /metrics", s.guard(s.handleMetrics))
}

// guard enforces authentication on every sensitive endpoint.
func (s *Server) guard(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Auth != nil && !s.cfg.Auth.Authenticate(r) {
			//nolint:gosec // slog escapes control characters; neither value is a credential
			slog.Warn("http auth rejected", "remote", r.RemoteAddr, "path", r.URL.Path)
			w.Header().Set("WWW-Authenticate", s.cfg.Auth.Challenge())
			writeError(w, http.StatusUnauthorized, "unauthorized")

			return
		}

		next(w, r)
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.Status())
}

func (s *Server) handleRules(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"rules": ruleViews(s.deps.Rules())})
}

func (s *Server) handleInterfaces(w http.ResponseWriter, _ *http.Request) {
	views := make([]ifaceView, 0, len(s.deps.Interfaces()))

	for _, iface := range s.deps.Interfaces() {
		views = append(views, ifaceView{
			Name: iface.Name,
			MAC:  iface.MAC.String(),
			IPv4: ipv4String(iface),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"interfaces": views})
}

// ipv4String renders an interface's IPv4 address, or "-" when it has none.
func ipv4String(iface wol.IfaceInfo) string {
	if iface.IPv4() == nil {
		return "-"
	}

	return iface.IPv4().String()
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	name := wol.Action(r.PathValue("name"))

	if s.deps.Dispatch == nil {
		writeError(w, http.StatusServiceUnavailable, "action dispatch is unavailable")

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()

	if err := s.deps.Dispatch(ctx, name); err != nil {
		//nolint:gosec // slog escapes control characters; neither value is a credential
		slog.Warn("http action failed", "remote", r.RemoteAddr, "action", string(name), "error", err)

		status := http.StatusInternalServerError

		switch {
		case errors.Is(err, wol.ErrUnknownActionRef):
			status = http.StatusNotFound
		case errors.Is(err, ErrSuppressed):
			status = http.StatusTooManyRequests
		case errors.Is(err, ErrRateLimited):
			status = http.StatusTooManyRequests
		}

		writeError(w, status, err.Error())

		return
	}

	//nolint:gosec // slog escapes control characters; remote is a socket address
	slog.Info("http action triggered", "remote", r.RemoteAddr, "action", string(name))
	writeJSON(w, http.StatusAccepted, map[string]string{"action": string(name), "status": "triggered"})
}

// maxCommandBody caps the JSON body of a remote command invocation.
const maxCommandBody = 4 << 10

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.deps.RunCommand == nil {
		writeError(w, http.StatusServiceUnavailable, "remote commands are unavailable")

		return
	}

	args, err := decodeArgs(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
	defer cancel()

	if err := s.deps.RunCommand(ctx, id, args); err != nil {
		//nolint:gosec // slog escapes control characters; neither value is a credential
		slog.Warn("http remote command failed", "remote", r.RemoteAddr, "command", id, "error", err)

		writeError(w, commandStatus(err), err.Error())

		return
	}

	//nolint:gosec // slog escapes control characters; remote is a socket address
	slog.Info("http remote command triggered", "remote", r.RemoteAddr, "command", id)
	writeJSON(w, http.StatusAccepted, map[string]string{"command": id, "status": "triggered"})
}

// decodeArgs parses the JSON string map carrying a remote command's arguments.
func decodeArgs(w http.ResponseWriter, r *http.Request) (map[string]string, error) {
	args := make(map[string]string)

	body := http.MaxBytesReader(w, r.Body, maxCommandBody)

	if err := json.NewDecoder(body).Decode(&args); err != nil {
		if errors.Is(err, io.EOF) {
			return args, nil
		}

		return nil, fmt.Errorf("invalid argument object: %w", err)
	}

	return args, nil
}

// commandStatus maps a remote command failure onto an HTTP status.
func commandStatus(err error) int {
	switch {
	case errors.Is(err, ErrCommandNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrCommandArgs):
		return http.StatusBadRequest
	case errors.Is(err, ErrCommandForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrSuppressed):
		return http.StatusTooManyRequests
	case errors.Is(err, ErrRateLimited):
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	//nolint:gosec // slog escapes control characters; remote is a socket address
	slog.Info("http reload requested", "remote", r.RemoteAddr)

	if s.deps.Reload == nil {
		writeError(w, http.StatusNotImplemented, "hot reload is not available in this build")

		return
	}

	if err := s.deps.Reload(r.Context()); err != nil {
		slog.Error("reload failed", "error", err)

		writeError(w, reloadStatus(err), err.Error())

		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"reloaded": true})
}

// reloadStatus maps a reload failure onto a status code: a change that needs a restart
// is a conflict, anything else (unreadable or invalid configuration) is a bad request.
func reloadStatus(err error) int {
	if errors.Is(err, ErrRestartRequired) {
		return http.StatusConflict
	}

	return http.StatusBadRequest
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	st := s.deps.Status()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	fmt.Fprintf(w, "# TYPE sol_packets_total counter\nsol_packets_total %d\n", st.Packets)
	fmt.Fprintf(w, "# TYPE sol_matched_total counter\nsol_matched_total %d\n", st.Matched)
	fmt.Fprintf(w, "# TYPE sol_suppressed_total counter\nsol_suppressed_total %d\n", st.Suppressed)
	fmt.Fprintf(w, "# TYPE sol_rate_limited_total counter\nsol_rate_limited_total %d\n", st.RateLimited)
	fmt.Fprintf(w, "# TYPE sol_rules gauge\nsol_rules %d\n", st.Rules)
	fmt.Fprintf(w, "# TYPE sol_uptime_seconds gauge\nsol_uptime_seconds %.3f\n", st.UptimeSecs)
	fmt.Fprintf(w, "# TYPE sol_actions_total counter\n")

	for _, name := range slices.Sorted(maps.Keys(st.Actions)) {
		fmt.Fprintf(w, "sol_actions_total{action=%q} %d\n", name, st.Actions[name])
	}
}

type ifaceView struct {
	Name string `json:"name"`
	MAC  string `json:"mac"`
	IPv4 string `json:"ipv4"`
}

type ruleView struct {
	Ports    []int    `json:"ports,omitempty"`
	MAC      string   `json:"mac,omitempty"`
	Ifaces   []string `json:"interfaces,omitempty"`
	Content  string   `json:"content,omitempty"`
	SrcCIDRs []string `json:"src_cidrs,omitempty"`
	Action   string   `json:"action"`
	DryRun   bool     `json:"dry_run"`
}

func ruleViews(rules []wol.Rule) []ruleView {
	views := make([]ruleView, 0, len(rules))

	for _, rule := range rules {
		views = append(views, ruleView{
			Ports:    rule.Match.Ports,
			MAC:      macLabel(rule.Match.MAC),
			Ifaces:   rule.Match.MAC.Ifaces,
			Content:  contentLabel(rule.Match.Content),
			SrcCIDRs: rule.Match.SrcCIDRs,
			Action:   string(rule.Action),
			DryRun:   rule.DryRun,
		})
	}

	return views
}

func macLabel(sel wol.MACSelector) string {
	switch sel.Kind {
	case wol.MACInterface:
		return "interface"
	case wol.MACExplicit:
		return sel.Address
	case wol.MACAny:
		return "any"
	case wol.MACSelf, "":
		// An unset kind means the default: match this host's own MACs.
		return "self"
	default:
		return "unknown"
	}
}

func contentLabel(matcher wol.ContentMatcher) string {
	switch matcher.Kind {
	case wol.ContentAny:
		return "any"
	case wol.ContentSuffix, wol.ContentPrefix:
		value := matcher.Value
		if value == "" {
			value = "0x" + matcher.Hex
		}

		return string(matcher.Kind) + ":" + value
	case wol.ContentNone, "":
		// An unset kind means the default: a plain 102-byte magic packet.
		return "none"
	default:
		return "unknown"
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Warn("http response failed", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

type bearerAuth struct {
	token string
}

// BearerAuth validates "Authorization: Bearer <token>".
func BearerAuth(token string) Authenticator {
	return bearerAuth{token: token}
}

func (b bearerAuth) Authenticate(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	prefix := "Bearer "

	if !strings.HasPrefix(header, prefix) {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, prefix)), []byte(b.token)) == 1
}

func (b bearerAuth) Challenge() string {
	return `Bearer realm="sol"`
}

func (b bearerAuth) Type() string {
	return "bearer"
}

type basicAuth struct {
	user     string
	password string
}

// BasicAuth validates "Authorization: Basic <base64(user:password)>".
func BasicAuth(user string, password string) Authenticator {
	return basicAuth{user: user, password: password}
}

func (b basicAuth) Authenticate(r *http.Request) bool {
	user, password, ok := r.BasicAuth()
	if !ok {
		return false
	}

	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(b.user)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(password), []byte(b.password)) == 1

	return userOK && passOK
}

func (b basicAuth) Challenge() string {
	return `Basic realm="sol"`
}

func (b basicAuth) Type() string {
	return "basic"
}

type mtlsAuth struct{}

// MTLSAuth trusts the TLS layer: the client certificate was already verified.
func MTLSAuth() Authenticator {
	return mtlsAuth{}
}

func (mtlsAuth) Authenticate(_ *http.Request) bool {
	return true
}

func (mtlsAuth) Challenge() string {
	return ""
}

func (mtlsAuth) Type() string {
	return "mtls"
}

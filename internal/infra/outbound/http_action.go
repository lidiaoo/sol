// Package outbound implements the outbound HTTP action (type: http, §18.2).
//
// The executor is deliberately conservative: the destination must be allowlisted
// when security.url_allowlist is set, redirects are never followed, TLS is verified,
// each attempt is bounded by a timeout, and request headers (which may carry tokens)
// are never logged.
package outbound

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bavix/sol/internal/domain/wol"
)

var (
	// ErrEmptyURL reports a missing destination.
	ErrEmptyURL = errors.New("http action requires url")
	// ErrInvalidURL reports a malformed destination or an unsupported scheme.
	ErrInvalidURL = errors.New("invalid http action url")
	// ErrURLNotAllowed reports a destination outside security.url_allowlist.
	ErrURLNotAllowed = errors.New("http action url is not in security.url_allowlist")
	// ErrMethod reports an unsupported HTTP method.
	ErrMethod = errors.New("unsupported http action method")
	// ErrRetries reports a retries value outside its bounds.
	ErrRetries = errors.New("invalid http action retries")
	// ErrTimeout reports a timeout outside its bounds.
	ErrTimeout = errors.New("invalid http action timeout")
	// ErrRequest reports a transport level failure.
	ErrRequest = errors.New("http action request failed")
	// ErrStatus reports a response outside the 2xx range.
	ErrStatus = errors.New("http action returned a non-success status")
	// ErrProxy reports a proxy URL the transport cannot use.
	ErrProxy = errors.New("invalid http action proxy")
)

const (
	defaultTimeout = 5 * time.Second
	maxTimeout     = time.Minute
	maxRetries     = 5
	maxBodyBytes   = 64 << 10
	backoffUnit    = 200 * time.Millisecond
)

// Executor performs outbound HTTP requests, optionally restricted to an allowlist.
type Executor struct {
	client *http.Client
	// allowlist holds the parsed entries of security.url_allowlist (§19.8), empty when
	// the operator did not configure one.
	allowlist []allowEntry
	// allowlistErr keeps a malformed entry: Validate reports it at startup, so a typo
	// cannot silently turn into a wider (or narrower) allowlist.
	allowlistErr error
	// proxyMu guards the per-proxy clients. They are built once per distinct proxy and
	// reused, so an action that names a proxy does not pay for a connection pool per
	// packet, and the set stays bounded by the configuration.
	proxyMu sync.Mutex
	proxies map[string]*http.Client
}

// NewExecutor builds the executor; an empty allowlist permits any http(s) host.
func NewExecutor(allowlist []string) *Executor {
	entries, err := parseAllowlist(allowlist)

	return &Executor{
		client: &http.Client{
			// Redirects are not followed: a 3xx could point outside the allowlist.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		allowlist:    entries,
		allowlistErr: err,
		proxies:      map[string]*http.Client{},
	}
}

// parseProxy validates an operator supplied proxy URL. Go's transport speaks http, https and
// socks5 by itself, so no dependency is needed; anything else, or a URL without a host, is a
// start-up error rather than a silent direct connection.
func parseProxy(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProxy, err)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("%w: %q (want http://, https:// or socks5://)", ErrProxy, raw)
	}

	if parsed.Host == "" {
		return nil, fmt.Errorf("%w: %q has no host", ErrProxy, raw)
	}

	return parsed, nil
}

// Validate checks an http action at startup: present destination with a literal
// scheme and host, a supported method, bounded timeout/retries, parseable templates
// and, when configured, an allowlisted destination.
func (e *Executor) Validate(def wol.ActionDef) error {
	if e.allowlistErr != nil {
		return e.allowlistErr
	}

	params := def.HTTP
	if params == nil || strings.TrimSpace(params.URL) == "" {
		return ErrEmptyURL
	}

	// One check per rule, so a start-up error names exactly one thing to fix.
	for _, check := range []error{
		checkMethod(params.Method),
		checkRetries(params.Retries),
		checkTimeout(params.Timeout),
		checkTemplates(params),
		checkProxy(params.Proxy),
	} {
		if check != nil {
			return check
		}
	}

	return e.validateDestination(params.URL)
}

// checkRetries bounds the retry count.
func checkRetries(retries int) error {
	if retries < 0 || retries > maxRetries {
		return fmt.Errorf("%w: %d (max %d)", ErrRetries, retries, maxRetries)
	}

	return nil
}

// checkTimeout bounds one attempt.
func checkTimeout(timeout time.Duration) error {
	if timeout < 0 || timeout > maxTimeout {
		return fmt.Errorf("%w: %s (max %s)", ErrTimeout, timeout, maxTimeout)
	}

	return nil
}

// checkTemplates parses every value that may interpolate event data.
func checkTemplates(params *wol.HTTPParams) error {
	templates := slices.Concat([]string{params.URL, params.Body}, headerValues(params.Headers))

	return wol.ParseTemplates(templates)
}

// checkProxy refuses a proxy the transport cannot use.
func checkProxy(proxy string) error {
	if strings.TrimSpace(proxy) == "" {
		return nil
	}

	_, err := parseProxy(proxy)

	return err
}

// Execute performs the request, interpolating the whitelisted event values, retrying
// transient failures and auditing the outcome without ever logging the headers.
func (e *Executor) Execute(ctx context.Context, def wol.ActionDef, ev wol.Event) error {
	params := def.HTTP
	if params == nil {
		return ErrEmptyURL
	}

	request, err := interpolateRequest(wol.EventVars(def.Name, ev), params)
	if err != nil {
		return err
	}

	// The proxy is resolved once, before the first attempt: an unusable one is an error
	// the operator should see at start-up, not a retry loop.
	client, err := e.clientFor(params.Proxy)
	if err != nil {
		return err
	}

	timeout := params.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	var lastErr error

	for attempt := range params.Retries + 1 {
		if attempt > 0 {
			if waitErr := sleep(ctx, backoff(attempt)); waitErr != nil {
				return waitErr
			}

			slog.Warn("http action retrying",
				"action", string(def.Name),
				"attempt", attempt+1,
				"error", lastErr,
			)
		}

		lastErr = e.attempt(ctx, def, client, request, timeout)
		if lastErr == nil {
			return nil
		}

		if !retryable(lastErr) {
			break
		}
	}

	return lastErr
}

// clientFor returns the client an action should use: the shared one, which honours the
// environment like Go's default transport does, unless the action names its own proxy.
func (e *Executor) clientFor(proxy string) (*http.Client, error) {
	if strings.TrimSpace(proxy) == "" {
		return e.client, nil
	}

	e.proxyMu.Lock()
	defer e.proxyMu.Unlock()

	if client, ok := e.proxies[proxy]; ok {
		return client, nil
	}

	parsed, err := parseProxy(proxy)
	if err != nil {
		return nil, err
	}

	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}

	transport := base.Clone()
	transport.Proxy = http.ProxyURL(parsed)

	client := &http.Client{
		Transport: transport,
		// Redirects stay unfollowed here too: a 3xx could leave the allowlist.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	e.proxies[proxy] = client

	return client, nil
}

// request is one interpolated HTTP request.
type request struct {
	url     string
	method  string
	headers map[string]string
	body    string
}

// attempt performs one request and audits the outcome.
func (e *Executor) attempt(
	ctx context.Context,
	def wol.ActionDef,
	client *http.Client,
	req request,
	timeout time.Duration,
) error {
	if err := e.checkAllowlist(req.url); err != nil {
		return err
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var payload io.Reader

	if req.body != "" {
		payload = strings.NewReader(req.body)
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, req.method, req.url, payload)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	for key, value := range req.headers {
		httpReq.Header.Set(key, value)
	}

	started := time.Now()

	resp, err := client.Do(httpReq)
	if err != nil {
		slog.Error("http action failed",
			"action", string(def.Name),
			"method", req.method,
			"url", req.url,
			"duration", time.Since(started).String(),
			"error", err,
		)

		return fmt.Errorf("%w: %w", ErrRequest, err)
	}

	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))

	slog.Info("http action finished",
		"action", string(def.Name),
		"method", req.method,
		"url", req.url,
		"status", resp.StatusCode,
		"duration", time.Since(started).String(),
	)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &statusError{code: resp.StatusCode}
	}

	return nil
}

// interpolateRequest renders the destination, method, headers and body of one request.
func interpolateRequest(vars wol.Vars, params *wol.HTTPParams) (request, error) {
	target, err := vars.Interpolate(params.URL)
	if err != nil {
		return request{}, err
	}

	body, err := vars.Interpolate(params.Body)
	if err != nil {
		return request{}, err
	}

	headers, err := interpolateHeaders(vars, params.Headers)
	if err != nil {
		return request{}, err
	}

	return request{url: target, method: method(params.Method), headers: headers, body: body}, nil
}

// interpolateHeaders renders the header values with the same whitelisted values.
func interpolateHeaders(vars wol.Vars, headers map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(headers))

	for key, value := range headers {
		rendered, err := vars.Interpolate(value)
		if err != nil {
			return nil, err
		}

		out[key] = rendered
	}

	return out, nil
}

// headerValues returns the header values, for startup template parsing.
func headerValues(headers map[string]string) []string {
	if len(headers) == 0 {
		return nil
	}

	out := make([]string, 0, len(headers))
	for _, value := range headers {
		out = append(out, value)
	}

	return out
}

// staticURLPrefix returns the part of the URL before any interpolation, so that the
// scheme and host can be validated even when the path is templated.
func staticURLPrefix(raw string) (string, error) {
	prefix, _, _ := strings.Cut(raw, "{{")

	return checkURL(prefix)
}

// checkURL validates one concrete URL.
func checkURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%w: scheme %q", ErrInvalidURL, parsed.Scheme)
	}

	if parsed.Host == "" {
		return "", fmt.Errorf("%w: missing host", ErrInvalidURL)
	}

	return raw, nil
}

// validateDestination checks the literal part of the destination against the allowlist:
// the scheme and host must be spelled out, so they can be compared before the request.
func (e *Executor) validateDestination(raw string) error {
	prefix, err := staticURLPrefix(raw)
	if err != nil {
		return err
	}

	return e.checkAllowlist(prefix)
}

// checkAllowlist enforces security.url_allowlist against a concrete URL: at startup the
// URL is still a literal prefix (templates unresolved), per attempt it is the final URL.
func (e *Executor) checkAllowlist(raw string) error {
	if e.allowlistErr != nil {
		return e.allowlistErr
	}

	if len(e.allowlist) == 0 {
		return nil
	}

	full, err := checkURL(raw)
	if err != nil {
		return err
	}

	target, err := url.Parse(full)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	for _, allowed := range e.allowlist {
		if allowed.match(full, target) {
			return nil
		}
	}

	return fmt.Errorf("%w: %s", ErrURLNotAllowed, full)
}

// checkMethod rejects unsupported methods; the empty method means POST.
func checkMethod(value string) error {
	if value == "" {
		return nil
	}

	switch strings.ToUpper(value) {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead:
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrMethod, value)
	}
}

func method(value string) string {
	if value == "" {
		return http.MethodPost
	}

	return strings.ToUpper(value)
}

// retryable reports whether another attempt makes sense (transport failures, 429 and 5xx).
func retryable(err error) bool {
	if errors.Is(err, ErrRequest) {
		return true
	}

	var statusErr *statusError

	if errors.As(err, &statusErr) {
		return statusErr.code == http.StatusTooManyRequests || statusErr.code >= 500
	}

	return false
}

// backoff returns the delay before the given attempt (1-based).
func backoff(attempt int) time.Duration {
	return time.Duration(attempt) * backoffUnit
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// statusError carries a response status code so that retries can classify it.
type statusError struct {
	code int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s: %d", ErrStatus, e.code)
}

func (e *statusError) Unwrap() error {
	return ErrStatus
}

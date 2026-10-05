package outbound

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ErrAllowlistEntry reports a malformed security.url_allowlist entry; it is raised at
// startup, never per request.
var ErrAllowlistEntry = errors.New("invalid url_allowlist entry")

// The three entry forms of security.url_allowlist (§19.8):
//
//	https://hooks.example.com/          prefix: same scheme+host, path under the prefix
//	=https://api.example.com/v1/notify  exact: the whole URL must be equal
//	~^https://[a-z]+\.example\.com/     regex: the whole URL must match
//
// Plain entries used to be matched with strings.HasPrefix, so "https://hooks.example.com"
// also allowed "https://hooks.example.comevil.com". Host and path are now compared with
// boundary rules instead.
const (
	exactForm = "="
	regexForm = "~"
)

// allowEntry is one parsed security.url_allowlist entry.
type allowEntry struct {
	exact   string
	pattern *regexp.Regexp
	prefix  *url.URL
}

// ValidateAllowlist reports a malformed security.url_allowlist entry as a startup error.
// It lets the assembler fail fast even when no http action is configured yet.
func ValidateAllowlist(raw []string) error {
	_, err := parseAllowlist(raw)

	return err
}

// parseAllowlist parses every configured entry, so a typo fails the start-up instead of
// silently widening or narrowing the allowlist.
func parseAllowlist(raw []string) ([]allowEntry, error) {
	entries := make([]allowEntry, 0, len(raw))

	for _, item := range raw {
		parsed, err := parseAllowEntry(item)
		if err != nil {
			return nil, err
		}

		entries = append(entries, parsed)
	}

	return entries, nil
}

func parseAllowEntry(item string) (allowEntry, error) {
	value := strings.TrimSpace(item)
	if value == "" {
		return allowEntry{}, fmt.Errorf("%w: empty entry", ErrAllowlistEntry)
	}

	if rest, ok := strings.CutPrefix(value, exactForm); ok {
		full, err := checkURL(rest)
		if err != nil {
			return allowEntry{}, fmt.Errorf("%w: %q: %w", ErrAllowlistEntry, item, err)
		}

		return allowEntry{exact: full}, nil
	}

	if rest, ok := strings.CutPrefix(value, regexForm); ok {
		pattern, err := regexp.Compile(rest)
		if err != nil {
			return allowEntry{}, fmt.Errorf("%w: %q: %w", ErrAllowlistEntry, item, err)
		}

		return allowEntry{pattern: pattern}, nil
	}

	full, err := checkURL(value)
	if err != nil {
		return allowEntry{}, fmt.Errorf("%w: %q: %w", ErrAllowlistEntry, item, err)
	}

	parsed, err := url.Parse(full)
	if err != nil {
		return allowEntry{}, fmt.Errorf("%w: %q: %w", ErrAllowlistEntry, item, err)
	}

	return allowEntry{prefix: parsed}, nil
}

// match reports whether the concrete URL satisfies the entry. full is the URL as
// interpolated for this attempt, target is the same URL parsed.
func (e allowEntry) match(full string, target *url.URL) bool {
	switch {
	case e.pattern != nil:
		return e.pattern.MatchString(full)
	case e.exact != "":
		return full == e.exact
	case e.prefix != nil:
		return hostAndPathAllowed(e.prefix, target)
	}

	return false
}

// hostAndPathAllowed compares scheme and host as whole units (the host carries the port,
// so an entry without a port does not cover another port), then requires the target path
// to sit under the entry path: "https://api.example.com/v1" does not allow "/v10".
func hostAndPathAllowed(allowed, target *url.URL) bool {
	if !strings.EqualFold(allowed.Scheme, target.Scheme) || !strings.EqualFold(allowed.Host, target.Host) {
		return false
	}

	return pathAllowed(allowed.EscapedPath(), target.EscapedPath())
}

func pathAllowed(allowed, target string) bool {
	if allowed == "" || allowed == "/" {
		return true
	}

	rest, ok := strings.CutPrefix(target, strings.TrimSuffix(allowed, "/"))

	return ok && (rest == "" || strings.HasPrefix(rest, "/"))
}

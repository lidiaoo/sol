package wol

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
)

var (
	ErrUnknownContentKind     = errors.New("unknown content kind")
	ErrContentValue           = errors.New("content matcher requires exactly one of value or value_hex")
	ErrContentTooLarge        = errors.New("content token exceeds max length")
	ErrUnknownMACKind         = errors.New("unknown mac kind")
	ErrMACConflict            = errors.New("match.mac and match.interfaces are mutually exclusive")
	ErrInterfaceScopeConflict = errors.New("rule scope does not match its interface block")
	ErrUnknownInterface       = errors.New("unknown interface")
	ErrInvalidCIDR            = errors.New("invalid src_cidr")
	ErrUnknownAuthKind        = errors.New("unknown match.auth kind")
	ErrAuthWithoutKey         = errors.New("match.auth requires security.packet_auth")
)

// MaxContentLen caps the decoded content token size.
const MaxContentLen = 64

// ContentKind selects how Match.Content is compared against the bytes after the magic packet.
type ContentKind string

const (
	ContentAny    ContentKind = "any"
	ContentNone   ContentKind = "none"
	ContentSuffix ContentKind = "suffix"
	ContentPrefix ContentKind = "prefix"
)

// ContentMatcher matches the bytes that follow the magic packet.
type ContentMatcher struct {
	Kind   ContentKind
	Value  string
	Hex    string
	Offset int

	data []byte
}

// Compile validates the matcher and decodes its token.
func (c ContentMatcher) Compile() (ContentMatcher, error) {
	kind := c.kind()
	if kind != ContentAny && kind != ContentNone && kind != ContentSuffix && kind != ContentPrefix {
		return ContentMatcher{}, fmt.Errorf("%w: %s", ErrUnknownContentKind, kind)
	}

	c.Kind = kind

	if kind == ContentAny || kind == ContentNone {
		if c.Value != "" || c.Hex != "" {
			return ContentMatcher{}, fmt.Errorf("%w: kind %s takes no value", ErrContentValue, kind)
		}

		c.Offset = 0
		c.data = nil

		return c, nil
	}

	return c.compileToken(kind)
}

// Matches reports whether content satisfies the matcher.
func (c ContentMatcher) Matches(content []byte) bool {
	switch c.kind() {
	case ContentAny:
		return true
	case ContentNone:
		return len(content) == 0
	case ContentSuffix:
		return bytes.HasSuffix(content, c.data)
	case ContentPrefix:
		end := c.Offset + len(c.data)

		return len(content) >= end && bytes.Equal(content[c.Offset:end], c.data)
	default:
		return false
	}
}

func (c ContentMatcher) kind() ContentKind {
	if c.Kind == "" {
		return ContentNone
	}

	return c.Kind
}

func (c ContentMatcher) key() string {
	return string(c.kind()) + ":" + hex.EncodeToString(c.data)
}

func (c ContentMatcher) compileToken(kind ContentKind) (ContentMatcher, error) {
	if (c.Value == "") == (c.Hex == "") {
		return ContentMatcher{}, fmt.Errorf("%w: kind %s", ErrContentValue, kind)
	}

	data, err := decodeToken(c.Value, c.Hex)
	if err != nil {
		return ContentMatcher{}, err
	}

	if len(data) == 0 || len(data) > MaxContentLen {
		return ContentMatcher{}, fmt.Errorf("%w: %d bytes", ErrContentTooLarge, len(data))
	}

	if c.Offset < 0 {
		return ContentMatcher{}, fmt.Errorf("%w: negative offset", ErrContentValue)
	}

	if kind == ContentSuffix && c.Offset != 0 {
		return ContentMatcher{}, fmt.Errorf("%w: suffix does not use offset", ErrContentValue)
	}

	c.data = data

	return c, nil
}

func decodeToken(value string, hexValue string) ([]byte, error) {
	if hexValue != "" {
		data, err := hex.DecodeString(hexValue)
		if err != nil {
			return nil, fmt.Errorf("%w: bad value_hex: %w", ErrContentValue, err)
		}

		return data, nil
	}

	return []byte(value), nil
}

func contentOverlap(a ContentMatcher, b ContentMatcher) bool {
	if a.kind() == ContentAny || b.kind() == ContentAny {
		return true
	}

	if a.kind() == ContentNone || b.kind() == ContentNone {
		return a.kind() == b.kind()
	}

	if a.kind() != b.kind() {
		// A prefix and a suffix can always be satisfied by the same payload.
		return true
	}

	if a.kind() == ContentPrefix {
		return bytes.HasPrefix(a.data, b.data) || bytes.HasPrefix(b.data, a.data)
	}

	return bytes.HasSuffix(a.data, b.data) || bytes.HasSuffix(b.data, a.data)
}

// MACKind selects how Match.MAC picks the accepted target MAC.
type MACKind string

const (
	MACSelf      MACKind = "self"
	MACInterface MACKind = "interface"
	MACExplicit  MACKind = "explicit"
	MACAny       MACKind = "any"
)

// MACSelector describes which magic-packet target MAC a rule accepts.
type MACSelector struct {
	Kind    MACKind
	Address string
	Ifaces  []string
}

func (m MACSelector) kind() MACKind {
	if m.Kind == "" {
		return MACSelf
	}

	return m.Kind
}

type compiledMAC struct {
	kind   MACKind
	addrs  []net.HardwareAddr
	ifaces []string
}

func compileMAC(sel MACSelector, ifaces map[string]net.HardwareAddr, all []net.HardwareAddr) (compiledMAC, error) {
	kind := sel.kind()
	compiled := compiledMAC{kind: kind, ifaces: sel.Ifaces}

	switch kind {
	case MACAny:
		return compiled, nil
	case MACSelf:
		compiled.addrs = all

		return compiled, nil
	case MACExplicit:
		addr, err := net.ParseMAC(sel.Address)
		if err != nil {
			return compiledMAC{}, fmt.Errorf("invalid mac address %q: %w", sel.Address, err)
		}

		compiled.addrs = []net.HardwareAddr{addr}

		return compiled, nil
	case MACInterface:
		if len(sel.Ifaces) == 0 {
			return compiledMAC{}, fmt.Errorf("%w: no interface given", ErrUnknownInterface)
		}

		for _, name := range sel.Ifaces {
			mac, ok := ifaces[name]
			if !ok {
				return compiledMAC{}, fmt.Errorf("%w: %s", ErrUnknownInterface, name)
			}

			compiled.addrs = append(compiled.addrs, mac)
		}

		return compiled, nil
	default:
		return compiledMAC{}, fmt.Errorf("%w: %s", ErrUnknownMACKind, kind)
	}
}

func (c compiledMAC) matches(target net.HardwareAddr) bool {
	if c.kind == MACAny {
		return true
	}

	for _, addr := range c.addrs {
		if bytes.Equal(addr, target) {
			return true
		}
	}

	return false
}

func (c compiledMAC) scopeKey() string {
	parts := make([]string, 0, len(c.ifaces)+len(c.addrs)+1)
	parts = append(parts, string(c.kind))
	parts = append(parts, c.ifaces...)

	for _, addr := range c.addrs {
		parts = append(parts, addr.String())
	}

	return strings.Join(parts, "|")
}

// AuthKind selects whether a rule only matches packets carrying a valid authentication tag
// (§19.14). The zero value means the rule accepts any packet.
type AuthKind string

const (
	// AuthHMAC requires the packet to end with a valid truncated HMAC-SHA256 tag over the
	// bytes before it.
	AuthHMAC AuthKind = "hmac"
)

// Compile validates the selector.
func (a AuthKind) Compile() (AuthKind, error) {
	switch a {
	case "", AuthHMAC:
		return a, nil
	}

	return "", fmt.Errorf("%w: %s", ErrUnknownAuthKind, a)
}

// Match describes the conditions under which a rule fires.
type Match struct {
	Ports    []int
	MAC      MACSelector
	Content  ContentMatcher
	SrcCIDRs []string
	// Auth, when set to AuthHMAC, restricts the rule to authenticated packets.
	Auth AuthKind
}

// Rule binds a Match to an Action.
type Rule struct {
	Match  Match
	Action Action
	// DryRun marks a log-only rule: matching packets are logged but not executed.
	DryRun bool
}

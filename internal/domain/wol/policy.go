package wol

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"slices"
	"time"
)

var (
	ErrReservedPortAction = errors.New("reserved port only accepts noop on an empty payload")
	ErrInvalidPort        = errors.New("invalid port")
	ErrDuplicatePort      = errors.New("duplicate port condition in rules")
	ErrAmbiguousRule      = errors.New("ambiguous rules: equal specificity can match the same packet")
	ErrUnknownActionRef   = errors.New("unknown action reference")
	ErrDuplicateInterface = errors.New("duplicate interface")
	ErrSecureOnLength     = errors.New("secure_on must be exactly 6 bytes")
	ErrRuleConflict       = errors.New("overlapping rule scopes with identical conditions")
)

const (
	maxPort = 65535

	scoreContent = 100
	scoreAuth    = 50
	scoreSrc     = 10
	scoreMAC     = 10
	scorePort    = 1
)

// Event is a received packet plus the context the listeners know about it.
type Event struct {
	Payload []byte
	SrcIP   net.IP
	SrcPort int
	DstPort int
	// Interface is the listener interface owning the packet's target MAC, when known.
	Interface string
	// TargetMAC is the MAC carried by the magic packet, when known.
	TargetMAC net.HardwareAddr
	// Args carries the validated arguments of a remote command invocation (§21).
	Args map[string]string
}

// IfaceInfo describes a local interface that rules can match against.
type IfaceInfo struct {
	Name     string
	MAC      net.HardwareAddr
	IPs      []net.IP
	Up       bool
	Loopback bool
	Virtual  bool
	Eligible bool
}

// IPv4 returns the first IPv4 address of the interface, if any.
func (i IfaceInfo) IPv4() net.IP {
	for _, ip := range i.IPs {
		if v4 := ip.To4(); v4 != nil {
			return v4
		}
	}

	return nil
}

// PolicyOptions tunes construction-time validation.
type PolicyOptions struct {
	ReservedPorts []int
	AllowReserved bool
	SecureOn      []byte
	// Actions maps known action names to their definitions; nil means BuiltinActions().
	Actions map[Action]ActionDef
	// ExtraPorts are bound but not routed; used by the remote command channel (§21) and the
	// raw shell transport (§21.6), whose packets are consumed before rule matching.
	ExtraPorts []int
	// PacketKey, when set, authenticates whole packets (§19.14): a payload that ends with a
	// valid tag is matched on the bytes before the tag and can satisfy a rule with
	// `auth: hmac`. Without it, no packet is ever authenticated.
	PacketKey []byte
	// PacketWindow, when positive, enables replay protection (§19.16): authenticated packets
	// must carry a [stamp][tag] pair, the stamp has to be within the window and each tag is
	// accepted only once inside it.
	PacketWindow time.Duration
	// Now, when set, is the clock the replay guard reads; tests use it to travel in time.
	Now func() time.Time
	// OnAuthRejected, when set, reports a packet that carried a valid tag but was refused as
	// stale or as a replay. It is how an operator sees that the protection is doing work.
	OnAuthRejected func(reason string)
}

type compiledRule struct {
	rule    Rule
	ports   []int
	mac     compiledMAC
	content ContentMatcher
	auth    AuthKind
	srcNets []*net.IPNet
	score   int
}

// RoutingPolicy resolves an incoming packet to an action.
type RoutingPolicy struct {
	rules         []compiledRule
	actions       map[Action]ActionDef
	ifaceByMAC    map[string]string
	secureOn      []byte
	packetKey     []byte
	replay        *ReplayGuard
	now           func() time.Time
	reservedPorts map[int]bool
	extraPorts    []int
	allowReserved bool
}

// NewRoutingPolicy validates rules against the known interfaces and actions.
func NewRoutingPolicy(rules []Rule, ifaces []IfaceInfo, opts PolicyOptions) (*RoutingPolicy, error) {
	if len(opts.SecureOn) != 0 && len(opts.SecureOn) != MACSize {
		return nil, fmt.Errorf("%w: got %d bytes", ErrSecureOnLength, len(opts.SecureOn))
	}

	ifaceMACs, allMACs, err := indexIfaces(ifaces)
	if err != nil {
		return nil, err
	}

	policy := &RoutingPolicy{
		actions:       actionsOrDefault(opts.Actions),
		ifaceByMAC:    macIndex(ifaces),
		secureOn:      opts.SecureOn,
		packetKey:     opts.PacketKey,
		now:           opts.Now,
		replay:        NewReplayGuard(opts.PacketWindow, opts.OnAuthRejected),
		reservedPorts: reservedSet(opts.ReservedPorts),
		extraPorts:    opts.ExtraPorts,
		allowReserved: opts.AllowReserved,
	}

	for i, rule := range rules {
		compiled, compileErr := policy.compileRule(rule, ifaceMACs, allMACs)
		if compileErr != nil {
			return nil, fmt.Errorf("rule %d: %w", i+1, compileErr)
		}

		policy.rules = append(policy.rules, compiled)
	}

	if conflictErr := policy.checkConflicts(); conflictErr != nil {
		return nil, conflictErr
	}

	slices.SortStableFunc(policy.rules, func(a compiledRule, b compiledRule) int {
		return b.score - a.score
	})

	return policy, nil
}

// Decision is the outcome of resolving a packet against the policy.
type Decision struct {
	Action Action
	DryRun bool
	// Interface names the listener interface that owns the packet's target MAC, when known.
	Interface string
	// TargetMAC is the MAC carried by the magic packet.
	TargetMAC net.HardwareAddr
	// Authenticated reports that the packet carried a valid tag (§19.14).
	Authenticated bool
}

// Resolve returns the decision for a received packet.
func (p *RoutingPolicy) Resolve(ev Event) (Decision, bool) {
	parsed, ok := ParsePacket(ev.Payload, p.secureOn)
	if !ok {
		return Decision{}, false
	}

	if len(p.packetKey) > 0 {
		parsed = p.authenticate(ev.Payload, parsed)
	}

	for i := range p.rules {
		if p.rules[i].matches(ev, parsed) {
			return Decision{
				Action:        p.rules[i].rule.Action,
				DryRun:        p.rules[i].rule.DryRun,
				Interface:     p.ifaceByMAC[parsed.MAC.String()],
				TargetMAC:     parsed.MAC,
				Authenticated: parsed.Authenticated,
			}, true
		}
	}

	return Decision{}, false
}

// Ports returns the distinct ports to bind: the ports declared by the rules plus the
// extra (unrouted) ports, such as those of the remote command channel.
func (p *RoutingPolicy) Ports() []int {
	seen := make(map[int]bool, len(p.rules)+len(p.extraPorts))
	ports := make([]int, 0, len(p.rules)+len(p.extraPorts))

	for _, rule := range p.rules {
		for _, port := range rule.ports {
			if seen[port] {
				continue
			}

			seen[port] = true
			ports = append(ports, port)
		}
	}

	for _, port := range p.extraPorts {
		if seen[port] {
			continue
		}

		seen[port] = true
		ports = append(ports, port)
	}

	return ports
}

// ParsePacket parses a UDP payload with the policy's secure-on value.
func (p *RoutingPolicy) ParsePacket(payload []byte) (ParsedPacket, bool) {
	return ParsePacket(payload, p.secureOn)
}

// InterfaceForMAC returns the local interface owning mac, when known.
func (p *RoutingPolicy) InterfaceForMAC(mac net.HardwareAddr) string {
	return p.ifaceByMAC[mac.String()]
}

// Rules returns the configured rules.
func (p *RoutingPolicy) Rules() []Rule {
	rules := make([]Rule, len(p.rules))
	for i := range p.rules {
		rules[i] = p.rules[i].rule
	}

	return rules
}

func (p *RoutingPolicy) compileRule(rule Rule, ifaceMACs map[string]net.HardwareAddr, allMACs []net.HardwareAddr) (compiledRule, error) {
	if err := validatePorts(rule.Match.Ports); err != nil {
		return compiledRule{}, err
	}

	def, ok := p.actions[rule.Action]
	if !ok {
		return compiledRule{}, fmt.Errorf("%w: %s", ErrUnknownActionRef, rule.Action)
	}

	if len(rule.Match.MAC.Ifaces) > 0 && rule.Match.MAC.Kind != "" && rule.Match.MAC.Kind != MACInterface {
		return compiledRule{}, ErrMACConflict
	}

	content, auth, err := p.compileMatch(rule)
	if err != nil {
		return compiledRule{}, err
	}

	mac, err := compileMAC(rule.Match.MAC, ifaceMACs, allMACs)
	if err != nil {
		return compiledRule{}, err
	}

	srcNets, err := parseCIDRs(rule.Match.SrcCIDRs)
	if err != nil {
		return compiledRule{}, err
	}

	if err := p.checkReserved(rule.Match.Ports, def, content, auth); err != nil {
		return compiledRule{}, err
	}

	compiled := compiledRule{
		rule:    rule,
		ports:   rule.Match.Ports,
		mac:     mac,
		content: content,
		auth:    auth,
		srcNets: srcNets,
		score:   scoreRule(rule.Match.Ports, content, srcNets, mac, auth),
	}

	return compiled, nil
}

// authenticate strips a valid trailing tag and marks the packet. A payload without a valid tag
// is returned as parsed, so a rule that requires authentication cannot match it. With a window
// configured (§19.16) the payload carries a stamp in front of the tag, and the packet is refused
// when the stamp is stale or its tag was already accepted.
func (p *RoutingPolicy) authenticate(payload []byte, parsed ParsedPacket) ParsedPacket {
	data, ok := p.unwrapSignature(payload)
	if !ok {
		return parsed
	}

	stripped, ok := ParsePacket(data, p.secureOn)
	if !ok {
		return parsed
	}

	stripped.Authenticated = true

	return stripped
}

// unwrapSignature verifies the payload's tag and, when replay protection is on, its stamp too.
func (p *RoutingPolicy) unwrapSignature(payload []byte) ([]byte, bool) {
	if p.replay == nil {
		return SplitPacketSignature(p.packetKey, payload)
	}

	data, stamp, tag, ok := SplitTimestampedSignature(p.packetKey, payload)
	if !ok {
		return nil, false
	}

	if !p.replay.Accept(tag, stamp, p.clock()) {
		return nil, false
	}

	return data, true
}

// clock reads the configured time source, defaulting to the wall clock.
func (p *RoutingPolicy) clock() time.Time {
	if p.now != nil {
		return p.now()
	}

	return time.Now()
}

// compileMatch compiles the payload-side conditions of a rule and refuses an authenticated rule
// while no packet key is configured: it could never match, which is a configuration mistake.
func (p *RoutingPolicy) compileMatch(rule Rule) (ContentMatcher, AuthKind, error) {
	content, err := rule.Match.Content.Compile()
	if err != nil {
		return ContentMatcher{}, "", err
	}

	auth, err := rule.Match.Auth.Compile()
	if err != nil {
		return ContentMatcher{}, "", err
	}

	if auth == AuthHMAC && len(p.packetKey) == 0 {
		return ContentMatcher{}, "", fmt.Errorf("%w: %s", ErrAuthWithoutKey, rule.Action)
	}

	return content, auth, nil
}

func (p *RoutingPolicy) checkReserved(ports []int, def ActionDef, content ContentMatcher, auth AuthKind) error {
	for _, port := range ports {
		if !p.reservedPorts[port] {
			continue
		}

		if auth == AuthHMAC {
			return fmt.Errorf("%w: port %d cannot require an authenticated packet", ErrReservedPortAction, port)
		}

		if content.kind() != ContentNone {
			return fmt.Errorf("%w: port %d requires an empty payload", ErrReservedPortAction, port)
		}

		if def.Type != ActionTypeNoop && !p.allowReserved {
			return fmt.Errorf("%w: port %d only accepts noop", ErrReservedPortAction, port)
		}
	}

	return nil
}

func (p *RoutingPolicy) checkConflicts() error {
	for i := range p.rules {
		for j := i + 1; j < len(p.rules); j++ {
			if err := conflict(p.rules[i], p.rules[j]); err != nil {
				return err
			}
		}
	}

	return nil
}

func (r *compiledRule) matches(ev Event, parsed ParsedPacket) bool {
	if r.auth == AuthHMAC && !parsed.Authenticated {
		return false
	}

	if !portMatches(r.ports, ev.DstPort) {
		return false
	}

	if !r.mac.matches(parsed.MAC) {
		return false
	}

	if !r.content.Matches(parsed.Content) {
		return false
	}

	return srcMatches(r.srcNets, ev.SrcIP)
}

func conflict(a compiledRule, b compiledRule) error {
	if err := sameScopeConflict(a, b); err != nil {
		return err
	}

	return crossScopeConflict(a, b)
}

// sameScopeConflict keeps the original behaviour: two rules with the same MAC scope and
// the same specificity that can both match the same packet.
func sameScopeConflict(a compiledRule, b compiledRule) error {
	if a.score != b.score || a.mac.scopeKey() != b.mac.scopeKey() {
		return nil
	}

	if !portsOverlap(a.ports, b.ports) || !srcOverlap(a.srcNets, b.srcNets) || !contentOverlap(a.content, b.content) {
		return nil
	}

	if a.content.key() == b.content.key() && samePorts(a.ports, b.ports) {
		return fmt.Errorf("%w: ports %v", ErrDuplicatePort, a.ports)
	}

	return fmt.Errorf("%w: ports %v", ErrAmbiguousRule, a.ports)
}

// crossScopeConflict rejects rules whose scopes overlap without being identical while
// every other condition matches: the overlap has to be made explicit instead of being
// resolved silently by preferring the more specific rule (§8, §17.9).
func crossScopeConflict(a compiledRule, b compiledRule) error {
	if a.mac.scopeKey() == b.mac.scopeKey() || !macsOverlap(a.mac, b.mac) || a.auth != b.auth {
		return nil
	}

	if !samePorts(a.ports, b.ports) || a.content.key() != b.content.key() || !sameSRC(a.srcNets, b.srcNets) {
		return nil
	}

	return fmt.Errorf("%w: ports %v with scopes %q and %q",
		ErrRuleConflict, a.ports, a.mac.scopeKey(), b.mac.scopeKey())
}

func scoreRule(ports []int, content ContentMatcher, srcNets []*net.IPNet, mac compiledMAC, auth AuthKind) int {
	score := 0

	if content.kind() == ContentSuffix || content.kind() == ContentPrefix {
		score += scoreContent
	}

	if len(srcNets) > 0 {
		score += scoreSrc
	}

	if mac.kind == MACInterface || mac.kind == MACExplicit {
		score += scoreMAC
	}

	if auth == AuthHMAC {
		score += scoreAuth
	}

	if len(ports) > 0 {
		score += scorePort
	}

	return score
}

func validatePorts(ports []int) error {
	for _, port := range ports {
		if port < 1 || port > maxPort {
			return fmt.Errorf("%w: %d", ErrInvalidPort, port)
		}
	}

	return nil
}

// ParseCIDRs validates src_cidrs-style entries. The raw shell channel (§21.6) reuses it, so a
// network list means the same thing wherever it appears.
func ParseCIDRs(cidrs []string) ([]*net.IPNet, error) {
	return parseCIDRs(cidrs)
}

func parseCIDRs(cidrs []string) ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(cidrs))

	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidCIDR, cidr)
		}

		nets = append(nets, ipNet)
	}

	return nets, nil
}

// macIndex maps a target MAC to the interface that owns it.
func macIndex(ifaces []IfaceInfo) map[string]string {
	index := make(map[string]string, len(ifaces))

	for _, iface := range ifaces {
		index[iface.MAC.String()] = iface.Name
	}

	return index
}

func indexIfaces(ifaces []IfaceInfo) (map[string]net.HardwareAddr, []net.HardwareAddr, error) {
	byName := make(map[string]net.HardwareAddr, len(ifaces))
	all := make([]net.HardwareAddr, 0, len(ifaces))

	for _, iface := range ifaces {
		if _, exists := byName[iface.Name]; exists {
			return nil, nil, fmt.Errorf("%w: %s", ErrDuplicateInterface, iface.Name)
		}

		byName[iface.Name] = iface.MAC
		all = append(all, iface.MAC)
	}

	return byName, all, nil
}

func actionsOrDefault(actions map[Action]ActionDef) map[Action]ActionDef {
	if len(actions) == 0 {
		return BuiltinActions()
	}

	return actions
}

func reservedSet(ports []int) map[int]bool {
	if len(ports) == 0 {
		ports = DefaultReservedPorts()
	}

	set := make(map[int]bool, len(ports))
	for _, port := range ports {
		set[port] = true
	}

	return set
}

func portMatches(ports []int, dstPort int) bool {
	if len(ports) == 0 {
		return true
	}

	return slices.Contains(ports, dstPort)
}

func srcMatches(nets []*net.IPNet, srcIP net.IP) bool {
	if len(nets) == 0 {
		return true
	}

	for _, ipNet := range nets {
		if ipNet.Contains(srcIP) {
			return true
		}
	}

	return false
}

func portsOverlap(a []int, b []int) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}

	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}

	return false
}

func samePorts(a []int, b []int) bool {
	if len(a) != len(b) {
		return false
	}

	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}

	return true
}

func srcOverlap(a []*net.IPNet, b []*net.IPNet) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}

	return sameSRC(a, b)
}

func sameSRC(a []*net.IPNet, b []*net.IPNet) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i].String() != b[i].String() {
			return false
		}
	}

	return true
}

// macsOverlap reports whether two compiled MAC scopes can accept the same target MAC.
func macsOverlap(a compiledMAC, b compiledMAC) bool {
	if a.kind == MACAny || b.kind == MACAny {
		return true
	}

	for _, left := range a.addrs {
		for _, right := range b.addrs {
			if bytes.Equal(left, right) {
				return true
			}
		}
	}

	return false
}

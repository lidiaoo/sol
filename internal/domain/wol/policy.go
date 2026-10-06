package wol

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync/atomic"
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
	ErrRuleConflict       = errors.New("overlapping rule scopes with matching conditions")
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
	rule     Rule
	ports    []int
	mac      compiledMAC
	content  ContentMatcher
	auth     AuthKind
	srcNets  []*net.IPNet
	secureOn []byte
	score    int
}

// policyState is the part of a policy that a live interface refresh replaces. The compiled rules
// and the MAC -> interface index are derived from the same interface list, so they travel
// together: a packet must never be matched against a rule set and an index that disagree.
type policyState struct {
	rules      []compiledRule
	ifaceByMAC map[string]string
}

// RoutingPolicy resolves an incoming packet to an action.
type RoutingPolicy struct {
	state         atomic.Pointer[policyState]
	actions       map[Action]ActionDef
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
		secureOn:      opts.SecureOn,
		packetKey:     opts.PacketKey,
		now:           opts.Now,
		replay:        NewReplayGuard(opts.PacketWindow, opts.OnAuthRejected),
		reservedPorts: reservedSet(opts.ReservedPorts),
		extraPorts:    opts.ExtraPorts,
		allowReserved: opts.AllowReserved,
	}

	compiled, err := policy.compileRules(rules, ifaceMACs, allMACs)
	if err != nil {
		return nil, err
	}

	if conflictErr := checkConflicts(compiled); conflictErr != nil {
		return nil, conflictErr
	}

	slices.SortStableFunc(compiled, func(a compiledRule, b compiledRule) int {
		return b.score - a.score
	})

	policy.state.Store(&policyState{rules: compiled, ifaceByMAC: macIndex(ifaces)})

	return policy, nil
}

// SetInterfaces swaps in a fresh interface list (§17.2). The MAC kinds that name local interfaces
// (self, interface) are resolved from it, so a NIC that was down when sol started - or one that
// appeared afterwards, like a dock or a wireless link that came up late - starts matching without
// a restart. It reports whether the set actually changed, so the caller can audit it.
//
// A rule that names an interface which is absent right now keeps its name and matches nothing
// until the NIC is back: unplugging a dock must not turn into a failed refresh. At start-up the
// same missing name is an error, because there it is far more likely to be a typo.
func (p *RoutingPolicy) SetInterfaces(ifaces []IfaceInfo) (bool, error) {
	current := p.state.Load()

	byName, all, err := indexIfaces(ifaces)
	if err != nil {
		return false, err
	}

	index := macIndex(ifaces)
	if sameInterfaceIndex(current.ifaceByMAC, index) {
		return false, nil
	}

	rules := make([]compiledRule, 0, len(current.rules))

	for i := range current.rules {
		item := current.rules[i]

		mac, macErr := compileMAC(item.rule.Match.MAC, byName, all)
		switch {
		case macErr == nil:
			item.mac = mac
		case errors.Is(macErr, ErrUnknownInterface):
			item.mac = compiledMAC{kind: item.mac.kind, ifaces: item.mac.ifaces}
		default:
			return false, fmt.Errorf("rule %d: %w", i+1, macErr)
		}

		rules = append(rules, item)
	}

	p.state.Store(&policyState{rules: rules, ifaceByMAC: index})

	return true, nil
}

// sameInterfaceIndex reports whether two name -> MAC indexes describe the same NICs.
func sameInterfaceIndex(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for mac, name := range a {
		if b[mac] != name {
			return false
		}
	}

	return true
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
	parsed, ok := ParsePacketAny(ev.Payload, p.secureOns())
	if !ok {
		return Decision{}, false
	}

	if len(p.packetKey) > 0 {
		parsed = p.authenticate(ev.Payload, parsed)
	}

	state := p.state.Load()

	for i := range state.rules {
		if state.rules[i].matches(ev, parsed) {
			return Decision{
				Action:        state.rules[i].rule.Action,
				DryRun:        state.rules[i].rule.DryRun,
				Interface:     state.ifaceByMAC[parsed.MAC.String()],
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
	rules := p.state.Load().rules
	seen := make(map[int]bool, len(rules)+len(p.extraPorts))
	ports := make([]int, 0, len(rules)+len(p.extraPorts))

	for _, rule := range rules {
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

// ParsePacket parses a command-channel payload with the policy's default secure-on value. The
// command ports carry no rules, so the per-rule passwords do not apply to them and a frame with a
// different password is refused instead of being read as plain (§19.18).
func (p *RoutingPolicy) ParsePacket(payload []byte) (ParsedPacket, bool) {
	return ParsePacket(payload, p.secureOn)
}

// InterfaceForMAC returns the local interface owning mac, when known.
func (p *RoutingPolicy) InterfaceForMAC(mac net.HardwareAddr) string {
	return p.state.Load().ifaceByMAC[mac.String()]
}

// Rules returns the configured rules.
func (p *RoutingPolicy) Rules() []Rule {
	compiled := p.state.Load().rules
	rules := make([]Rule, len(compiled))

	for i := range compiled {
		rules[i] = compiled[i].rule
	}

	return rules
}

// compileRules compiles every rule against one interface set. Start-up and a live interface
// refresh both come through here, so the two can never disagree about what a rule means.
func (p *RoutingPolicy) compileRules(
	rules []Rule,
	ifaceMACs map[string]net.HardwareAddr,
	allMACs []net.HardwareAddr,
) ([]compiledRule, error) {
	compiled := make([]compiledRule, 0, len(rules))

	for i, rule := range rules {
		item, err := p.compileRule(rule, ifaceMACs, allMACs)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", i+1, err)
		}

		compiled = append(compiled, item)
	}

	return compiled, nil
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

	content, auth, secureOn, err := p.compileConditions(rule)
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

	if err := p.checkReserved(rule.Match.Ports, def, content, auth, secureOn); err != nil {
		return compiledRule{}, err
	}

	compiled := compiledRule{
		rule:     rule,
		ports:    rule.Match.Ports,
		mac:      mac,
		content:  content,
		auth:     auth,
		srcNets:  srcNets,
		secureOn: secureOn,
		score:    scoreRule(rule.Match.Ports, content, srcNets, mac, auth),
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

	stripped, ok := ParsePacketAny(data, p.secureOns())
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

// compileConditions resolves everything a rule needs from its match block: the content matcher,
// the authentication requirement and the password it expects (§19.18).
func (p *RoutingPolicy) compileConditions(rule Rule) (ContentMatcher, AuthKind, []byte, error) {
	content, auth, err := p.compileMatch(rule)
	if err != nil {
		return ContentMatcher{}, "", nil, err
	}

	secureOn, err := p.compileSecureOn(rule)
	if err != nil {
		return ContentMatcher{}, "", nil, err
	}

	return content, auth, secureOn, nil
}

// compileSecureOn resolves the password a rule requires: its own when set, the policy default
// otherwise, so a configuration that only sets security.secure_on keeps working (§19.18).
func (p *RoutingPolicy) compileSecureOn(rule Rule) ([]byte, error) {
	secureOn := rule.Match.SecureOn
	if secureOn == nil {
		secureOn = p.secureOn
	}

	if len(secureOn) != 0 && len(secureOn) != MACSize {
		return nil, fmt.Errorf("%w: got %d bytes", ErrSecureOnLength, len(secureOn))
	}

	return secureOn, nil
}

// secureOns lists every password a packet may carry: the policy default plus one per rule. The
// order does not matter, since all passwords sit at the same six bytes and only one can match.
func (p *RoutingPolicy) secureOns() [][]byte {
	rules := p.state.Load().rules
	candidates := make([][]byte, 0, len(rules)+1)

	if len(p.secureOn) > 0 {
		candidates = append(candidates, p.secureOn)
	}

	for i := range rules {
		secureOn := rules[i].secureOn
		if len(secureOn) == 0 {
			continue
		}

		known := false

		for _, candidate := range candidates {
			if bytes.Equal(candidate, secureOn) {
				known = true

				break
			}
		}

		if !known {
			candidates = append(candidates, secureOn)
		}
	}

	return candidates
}

func (p *RoutingPolicy) checkReserved(ports []int, def ActionDef, content ContentMatcher, auth AuthKind, secureOn []byte) error {
	for _, port := range ports {
		if !p.reservedPorts[port] {
			continue
		}

		if auth == AuthHMAC {
			return fmt.Errorf("%w: port %d cannot require an authenticated packet", ErrReservedPortAction, port)
		}

		if len(secureOn) > 0 {
			return fmt.Errorf("%w: port %d requires a plain magic packet", ErrReservedPortAction, port)
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

func checkConflicts(rules []compiledRule) error {
	for i := range rules {
		for j := i + 1; j < len(rules); j++ {
			if err := conflict(rules[i], rules[j]); err != nil {
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

	if !bytes.Equal(r.secureOn, parsed.SecureOn) {
		return false
	}

	return srcMatches(r.srcNets, ev.SrcIP)
}

// ambiguousHint names the ways two rules can be told apart without changing what either of them
// means. The list is the one the validation below actually accepts - disjoint source filters, a
// different port, a different SecureOn password, or a stricter auth requirement - so an operator
// who follows it lands on a configuration that starts (§8, §17.9: the overlap has to be explicit
// instead of being resolved silently by preferring one rule over the other).
const ambiguousHint = "tell them apart explicitly: disjoint src_cidrs, a different port, " +
	"a different secure_on password, or a stricter auth requirement"

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

	if !bytes.Equal(a.secureOn, b.secureOn) {
		// Two rules asking for different passwords can never match one packet: the payload
		// carries exactly one of them, so this is a real way to tell them apart (§19.18).
		return nil
	}

	if a.content.key() == b.content.key() && samePorts(a.ports, b.ports) && sameSRC(a.srcNets, b.srcNets) {
		return fmt.Errorf("%w: ports %v: the two rules are identical, remove one", ErrDuplicatePort, a.ports)
	}

	return fmt.Errorf("%w: ports %v: %s", ErrAmbiguousRule, a.ports, ambiguousHint)
}

// crossScopeConflict rejects rules whose scopes overlap without being identical while every
// other condition can match the same packet: the overlap has to be made explicit instead of
// being resolved silently by preferring the more specific rule (§8, §17.9). Conditions overlap
// when their source filters intersect - two different filters that share an address are not a
// way to tell two rules apart, since nothing orders one above the other for that address.
func crossScopeConflict(a compiledRule, b compiledRule) error {
	if a.mac.scopeKey() == b.mac.scopeKey() || !macsOverlap(a.mac, b.mac) || a.auth != b.auth {
		return nil
	}

	if !samePorts(a.ports, b.ports) || a.content.key() != b.content.key() || !srcOverlap(a.srcNets, b.srcNets) {
		return nil
	}

	if !bytes.Equal(a.secureOn, b.secureOn) {
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

// srcOverlap reports whether two source filters can accept the same address. An absent filter
// accepts everything, so it overlaps any filter; two filters overlap when any of their networks
// share an address. Comparing the sets for equality instead would let "10.0.0.0/8 and
// 10.1.0.0/16 on the same port" through, and a packet from 10.1.2.3 would then be resolved by
// which rule happens to be listed first (§8).
func srcOverlap(a []*net.IPNet, b []*net.IPNet) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}

	for _, left := range a {
		for _, right := range b {
			if netsIntersect(left, right) {
				return true
			}
		}
	}

	return false
}

// netsIntersect reports whether two networks share an address. Two properly masked prefixes
// intersect exactly when one network's base address falls inside the other, which also answers
// false for a v4/v6 pair.
func netsIntersect(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
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

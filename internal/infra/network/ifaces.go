package network

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/bavix/sol/internal/domain/wol"
)

var ErrNoEligibleInterface = errors.New("no eligible interface found")

// List enumerates the local interfaces together with their auto-selection eligibility.
func List() ([]wol.IfaceInfo, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}

	out := make([]wol.IfaceInfo, 0, len(ifaces))

	for _, iface := range ifaces {
		info, describeErr := describe(iface)
		if describeErr != nil {
			return nil, describeErr
		}

		out = append(out, info)
	}

	return out, nil
}

// Select returns the named interfaces, or every eligible interface when names is empty.
func Select(names []string) ([]wol.IfaceInfo, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}

	if len(names) > 0 {
		return pick(all, names)
	}

	selected := eligible(all)
	if len(selected) == 0 {
		return nil, ErrNoEligibleInterface
	}

	return selected, nil
}

func describe(iface net.Interface) (wol.IfaceInfo, error) {
	info := wol.IfaceInfo{
		Name:     iface.Name,
		MAC:      iface.HardwareAddr,
		Up:       iface.Flags&net.FlagUp != 0,
		Loopback: iface.Flags&net.FlagLoopback != 0,
		Virtual:  isVirtualName(iface.Name),
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return wol.IfaceInfo{}, fmt.Errorf("addresses for %s: %w", iface.Name, err)
	}

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}

		info.IPs = append(info.IPs, ipNet.IP)
	}

	info.Eligible = eligibleInfo(info)

	return info, nil
}

// eligibleInfo reports whether a NIC is part of this machine's identity.
//
// Availability is deliberately not part of the criterion (§17.2): a NIC that is down right now is
// still this machine, and it may come up later - a wireless link that NetworkManager brings up
// after boot, a dock that gets plugged in. Its MAC can even change when it does, which the live
// refresh in the listener picks up. Only loopback, virtual/tunnel names and entries without a
// hardware address are excluded.
func eligibleInfo(info wol.IfaceInfo) bool {
	return !info.Loopback && !info.Virtual && len(info.MAC) == wol.MACSize
}

func eligible(all []wol.IfaceInfo) []wol.IfaceInfo {
	out := make([]wol.IfaceInfo, 0, len(all))

	for _, info := range all {
		if info.Eligible {
			out = append(out, info)
		}
	}

	return out
}

func pick(all []wol.IfaceInfo, names []string) ([]wol.IfaceInfo, error) {
	byName := make(map[string]wol.IfaceInfo, len(all))
	for _, info := range all {
		byName[info.Name] = info
	}

	out := make([]wol.IfaceInfo, 0, len(names))
	seen := make(map[string]bool, len(names))

	for _, name := range names {
		if seen[name] {
			return nil, fmt.Errorf("%w: %s", wol.ErrDuplicateInterface, name)
		}

		seen[name] = true

		info, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("%w: %s", wol.ErrUnknownInterface, name)
		}

		if len(info.MAC) != wol.MACSize {
			return nil, fmt.Errorf("%w: %s", ErrNoMACAddress, name)
		}

		out = append(out, info)
	}

	return out, nil
}

func isVirtualName(name string) bool {
	lower := strings.ToLower(name)

	for _, prefix := range []string{
		"docker", "veth", "virbr", "br-", "vnet", "vmnet",
		"tun", "tap", "tailscale", "wg", "zt", "podman", "cni", "flannel",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}

	return false
}

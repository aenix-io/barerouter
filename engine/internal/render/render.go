/*
Copyright 2026 The Cozystack Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package render turns the state the operator asks for into the operations that
// put the appliance in it, and reads the running configuration back against
// them. It is pure: it never talks to the appliance.
//
// The engine owns whole subtrees of the configuration (Managed). Every render
// deletes them and sets them again in the same commit, so a tunnel, a route or
// a rule that left the desired state leaves the appliance with it, and a
// render is the whole of what those subtrees hold.
package render

import (
	"cmp"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"

	seam "github.com/aenix-io/site-router-operator/api/engine"

	"github.com/aenix-io/barerouter/engine/internal/appliance"
	"github.com/aenix-io/barerouter/engine/internal/configtree"
)

// TunnelIngressRuleSet is the rule set that decrypted traffic is sent through
// and that admits only the flows the operator allows.
const TunnelIngressRuleSet = "TUNNEL-INGRESS"

const (
	ikeGroup       = "ROUTER-IKE"
	espGroup       = "ROUTER-ESP"
	managementPort = "443"
	// mssClamp keeps TCP segments inside a 1320-byte path through the tunnel:
	// 1320 less 40 bytes of IPv4 and TCP headers.
	mssClamp = "1280"
	// maxIngressRules keeps the rule numbers, which go up by ten, under the
	// largest the appliance takes.
	maxIngressRules = 99_999
)

// The contract between the operator and an engine carries no IKE or ESP
// parameters, so every tunnel is offered this one proposal.
var (
	ikeProposal = map[string]string{"dh-group": "14", "encryption": "aes256", "hash": "sha256"}
	espProposal = map[string]string{"encryption": "aes256", "hash": "sha256"}
)

// Managed returns the paths whose whole subtree the engine owns. device is the
// ethernet device the tunnel runs over, which carries the MSS clamp.
func Managed(device string) [][]string {
	return [][]string{
		inputFilter(),
		forwardFilter(),
		tunnelIngress(),
		{"firewall", "ipv6", "input", "filter"},
		{"firewall", "ipv6", "forward", "filter"},
		{"vpn", "ipsec"},
		{"protocols", "static", "route"},
		{"protocols", "bgp"},
		{"interfaces", "ethernet", device, "ip", "adjust-mss"},
	}
}

func inputFilter(sub ...string) []string {
	return append([]string{"firewall", "ipv4", "input", "filter"}, sub...)
}

func forwardFilter(sub ...string) []string {
	return append([]string{"firewall", "ipv4", "forward", "filter"}, sub...)
}

func tunnelIngress(sub ...string) []string {
	return append([]string{"firewall", "ipv4", "name", TunnelIngressRuleSet}, sub...)
}

func set(path []string, value string) appliance.Op {
	return appliance.Op{Op: appliance.OpSet, Path: path, Value: value}
}

var (
	deviceRe = regexp.MustCompile(`^eth[0-9]+$`)
	// peerNameRe is what the appliance takes as the name of a site-to-site
	// peer. A name that ends like a child SA would be told apart from one by
	// nothing in the show output, so it is refused too.
	peerNameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)
	childLikeRe  = regexp.MustCompile(`-tunnel-[0-9]+$`)
	anyIPv4      = netip.MustParsePrefix("0.0.0.0/0")
	defaultLocal = []netip.Prefix{anyIPv4}
)

// Render returns the operations that replace the managed subtrees with d:
// the deletes of every managed subtree first, then the sets that make it again.
// The appliance applies them in one commit. Lists are sorted and their
// duplicates dropped, so two states that mean the same render the same.
//
// The source filter is rendered whatever d holds. With no allowed destination it
// admits no new decrypted flow, and with no management network the management
// API admits nobody: an empty list closes, it never opens.
func Render(d seam.Desired, device string) ([]appliance.Op, error) {
	if !deviceRe.MatchString(device) {
		return nil, fmt.Errorf("render: %q is not an ethernet device", device)
	}
	tunnels, err := tunnelsOf(d.Tunnels)
	if err != nil {
		return nil, err
	}
	routes, err := routesOf(d.Routes)
	if err != nil {
		return nil, err
	}
	bgp, err := bgpOf(d.BGP)
	if err != nil {
		return nil, err
	}
	sources, err := prefixes("tunnel ingress source", d.Guards.TunnelIngress.Sources, minSourceBits)
	if err != nil {
		return nil, err
	}
	destinations, err := prefixes("tunnel ingress destination", d.Guards.TunnelIngress.Destinations, minDestinationBits)
	if err != nil {
		return nil, err
	}
	if len(sources)*len(destinations) > maxIngressRules {
		return nil, fmt.Errorf("render: %d sources and %d destinations need more rules than the source filter can number",
			len(sources), len(destinations))
	}
	var management netip.Prefix
	if d.Guards.ManagementAllow != "" {
		if management, err = prefix("management network", d.Guards.ManagementAllow, 1); err != nil {
			return nil, err
		}
	}

	ops := make([]appliance.Op, 0, 64+3*len(sources)*len(destinations))
	for _, path := range Managed(device) {
		ops = append(ops, appliance.Op{Op: appliance.OpDelete, Path: path})
	}
	ops = append(ops, renderInputFilter(management, len(tunnels) > 0, bgp)...)
	ops = append(ops, renderForwardFilter()...)
	ops = append(ops, renderTunnelIngress(sources, destinations)...)
	ops = append(ops, renderIPv6Filters()...)
	ops = append(ops, set([]string{"interfaces", "ethernet", device, "ip", "adjust-mss"}, mssClamp))
	for _, r := range routes {
		ops = append(ops, set([]string{"protocols", "static", "route", r.dest.String(), "next-hop", r.nextHop.String()}, ""))
	}
	ops = append(ops, renderIPSec(tunnels)...)
	ops = append(ops, renderBGP(bgp)...)
	return ops, nil
}

// renderInputFilter is what reaches the appliance itself. Rule 1 drops
// decrypted traffic to the management ports before anything can accept it:
// such a packet never crosses the pod interface, where the cluster's network
// policy would have stopped it. Rule 5 keeps the sessions a commit is made over,
// the operator's own included, since every commit replaces this chain. The rest
// opens the management API to the management network, IKE, NAT-T and ESP when
// there is a tunnel, and BGP to each neighbor, and drops everything else.
func renderInputFilter(management netip.Prefix, ipsec bool, bgp *bgpPlan) []appliance.Op {
	ops := []appliance.Op{
		set(inputFilter("default-action"), "drop"),
		set(inputFilter("rule", "1", "action"), "drop"),
		set(inputFilter("rule", "1", "destination", "port"), "22,"+managementPort),
		set(inputFilter("rule", "1", "ipsec", "match-ipsec-in"), ""),
		set(inputFilter("rule", "1", "protocol"), "tcp"),
		set(inputFilter("rule", "5", "action"), "accept"),
		set(inputFilter("rule", "5", "state"), "established"),
		set(inputFilter("rule", "5", "state"), "related"),
	}
	if management.IsValid() {
		ops = append(ops,
			set(inputFilter("rule", "10", "action"), "accept"),
			set(inputFilter("rule", "10", "destination", "port"), managementPort),
			set(inputFilter("rule", "10", "protocol"), "tcp"),
			set(inputFilter("rule", "10", "source", "address"), management.String()),
		)
	}
	if ipsec {
		ops = append(ops,
			set(inputFilter("rule", "20", "action"), "accept"),
			set(inputFilter("rule", "20", "destination", "port"), "500"),
			set(inputFilter("rule", "20", "protocol"), "udp"),
			set(inputFilter("rule", "21", "action"), "accept"),
			set(inputFilter("rule", "21", "destination", "port"), "4500"),
			set(inputFilter("rule", "21", "protocol"), "udp"),
			set(inputFilter("rule", "22", "action"), "accept"),
			set(inputFilter("rule", "22", "protocol"), "esp"),
		)
	}
	if bgp != nil {
		for i, n := range bgp.neighbors {
			rule := strconv.Itoa(30 + 10*i)
			ops = append(ops,
				set(inputFilter("rule", rule, "action"), "accept"),
				set(inputFilter("rule", rule, "destination", "port"), "179"),
				set(inputFilter("rule", rule, "protocol"), "tcp"),
				set(inputFilter("rule", rule, "source", "address"), n.address.String()),
			)
		}
	}
	return ops
}

// renderForwardFilter is what passes through the gateway. Decrypted traffic is
// matched as such and sent through the source filter; the rest, which is the
// tenant's traffic on its way into the tunnel, is accepted, because a cluster
// address is never one of the remote networks and the source filter would drop
// it. Nothing else is forwarded: the gateway routes the remote networks and is
// nobody's way out to the world. The filter is not bound to the pod interface
// for the same reason the tenant's traffic is accepted.
func renderForwardFilter() []appliance.Op {
	return []appliance.Op{
		set(forwardFilter("default-action"), "drop"),
		set(forwardFilter("rule", "5", "action"), "accept"),
		set(forwardFilter("rule", "5", "state"), "established"),
		set(forwardFilter("rule", "5", "state"), "related"),
		set(forwardFilter("rule", "10", "action"), "accept"),
		set(forwardFilter("rule", "10", "ipsec", "match-none-in"), ""),
		set(forwardFilter("rule", "20", "action"), "jump"),
		set(forwardFilter("rule", "20", "ipsec", "match-ipsec-in"), ""),
		set(forwardFilter("rule", "20", "jump-target"), TunnelIngressRuleSet),
	}
}

// renderTunnelIngress admits a decrypted flow only from a remote network to an
// allowed destination, every source paired with every destination. An accept in
// a rule set jumped to is final, so an accept by source alone would forward a
// packet from a good source to any address, the world included; with no
// destination there is therefore no accept at all, and only return traffic
// passes.
func renderTunnelIngress(sources, destinations []netip.Prefix) []appliance.Op {
	ops := make([]appliance.Op, 0, 4+3*len(sources)*len(destinations))
	ops = append(ops,
		set(tunnelIngress("default-action"), "drop"),
		set(tunnelIngress("rule", "5", "action"), "accept"),
		set(tunnelIngress("rule", "5", "state"), "established"),
		set(tunnelIngress("rule", "5", "state"), "related"),
	)
	rule := 10
	for _, src := range sources {
		for _, dst := range destinations {
			n := strconv.Itoa(rule)
			ops = append(ops,
				set(tunnelIngress("rule", n, "action"), "accept"),
				set(tunnelIngress("rule", n, "destination", "address"), dst.String()),
				set(tunnelIngress("rule", n, "source", "address"), src.String()),
			)
			rule += 10
		}
	}
	return ops
}

// renderIPv6Filters keeps IPv6 closed. The gateway carries IPv4 only, but the
// guest has a link-local IPv6 address whatever the cluster runs, and a chain
// that is not configured filters nothing: over IPv6 the router takes return
// traffic and ICMPv6, which neighbour discovery on the link needs, and forwards
// nothing. The seed sets the same up for the first boot.
func renderIPv6Filters() []appliance.Op {
	input := []string{"firewall", "ipv6", "input", "filter"}
	return []appliance.Op{
		set(path(input, "default-action"), "drop"),
		set(path(input, "rule", "5", "action"), "accept"),
		set(path(input, "rule", "5", "state"), "established"),
		set(path(input, "rule", "5", "state"), "related"),
		set(path(input, "rule", "10", "action"), "accept"),
		set(path(input, "rule", "10", "protocol"), "ipv6-icmp"),
		set([]string{"firewall", "ipv6", "forward", "filter", "default-action"}, "drop"),
	}
}

// renderIPSec makes one site-to-site peer per tunnel, named after the tunnel's
// ID, which is what Observe reads back. The appliance takes the pre-shared key
// from its key list by identity, so the list carries the peer's address and the
// identity the gateway presents. The gateway sits behind a load balancer, whose
// address it presents but does not hold, so it binds any local address.
// Encapsulation in UDP is forced: plain ESP does not survive the cluster
// network's connection tracking between pods.
func renderIPSec(tunnels []tunnelPlan) []appliance.Op {
	if len(tunnels) == 0 {
		return nil
	}
	ike := []string{"vpn", "ipsec", "ike-group", ikeGroup}
	esp := []string{"vpn", "ipsec", "esp-group", espGroup}
	var ops []appliance.Op
	for _, k := range []string{"dh-group", "encryption", "hash"} {
		ops = append(ops, set(path(ike, "proposal", "1", k), ikeProposal[k]))
	}
	ops = append(ops,
		set(path(ike, "key-exchange"), "ikev2"),
		set(path(ike, "lifetime"), "28800"),
		set(path(esp, "lifetime"), "3600"),
		set(path(esp, "pfs"), "dh-group14"),
	)
	for _, k := range []string{"encryption", "hash"} {
		ops = append(ops, set(path(esp, "proposal", "1", k), espProposal[k]))
	}

	for _, t := range tunnels {
		psk := []string{"vpn", "ipsec", "authentication", "psk", t.name}
		peer := []string{"vpn", "ipsec", "site-to-site", "peer", t.name}
		ops = append(ops,
			set(path(psk, "id"), t.peer.String()),
			set(path(psk, "secret"), t.psk),
			set(path(peer, "authentication", "mode"), "pre-shared-secret"),
			set(path(peer, "default-esp-group"), espGroup),
			set(path(peer, "force-udp-encapsulation"), ""),
			set(path(peer, "ike-group"), ikeGroup),
			set(path(peer, "local-address"), "any"),
			set(path(peer, "remote-address"), t.peer.String()),
		)
		if t.localID.IsValid() {
			ops = append(ops,
				set(path(psk, "id"), t.localID.String()),
				set(path(peer, "authentication", "local-id"), t.localID.String()),
			)
		}
		n := 1
		for _, local := range t.locals {
			for _, remote := range t.remotes {
				tun := strconv.Itoa(n)
				ops = append(ops,
					set(path(peer, "tunnel", tun, "local", "prefix"), local.String()),
					set(path(peer, "tunnel", tun, "remote", "prefix"), remote.String()),
				)
				n++
			}
		}
	}
	return ops
}

func renderBGP(bgp *bgpPlan) []appliance.Op {
	if bgp == nil {
		return nil
	}
	ops := []appliance.Op{set([]string{"protocols", "bgp", "system-as"}, strconv.FormatUint(uint64(bgp.localASN), 10))}
	for _, n := range bgp.neighbors {
		ops = append(ops, set([]string{"protocols", "bgp", "neighbor", n.address.String(), "remote-as"}, strconv.FormatUint(uint64(n.remoteASN), 10)))
	}
	return ops
}

func path(base []string, sub ...string) []string {
	return append(slices.Clone(base), sub...)
}

type tunnelPlan struct {
	name    string
	peer    netip.Addr
	localID netip.Addr
	psk     string
	locals  []netip.Prefix
	remotes []netip.Prefix
}

func tunnelsOf(in []seam.Tunnel) ([]tunnelPlan, error) {
	out := make([]tunnelPlan, 0, len(in))
	for _, t := range in {
		if !peerNameRe.MatchString(t.ID) || childLikeRe.MatchString(t.ID) {
			return nil, fmt.Errorf("render: tunnel ID %q is not a peer name the appliance takes (letters, digits, - and _, not ending in -tunnel-<n>)", t.ID)
		}
		if t.PSK == "" {
			return nil, fmt.Errorf("render: tunnel %s has no pre-shared key", t.ID)
		}
		peer, err := address("tunnel "+t.ID+" peer", t.PeerAddress)
		if err != nil {
			return nil, err
		}
		var localID netip.Addr
		if t.LocalID != "" {
			if localID, err = address("tunnel "+t.ID+" local ID", t.LocalID); err != nil {
				return nil, err
			}
		}
		// The local side is the tenant's own addresses, which no fixed network
		// describes, so a tunnel that names none is "any".
		locals, err := prefixes("tunnel "+t.ID+" local selector", t.LocalSelectors, 0)
		if err != nil {
			return nil, err
		}
		if len(locals) == 0 {
			locals = defaultLocal
		}
		remotes, err := prefixes("tunnel "+t.ID+" remote selector", t.RemoteSelectors, 1)
		if err != nil {
			return nil, err
		}
		if len(remotes) == 0 {
			return nil, fmt.Errorf("render: tunnel %s has no remote selector, so it would carry nothing", t.ID)
		}
		out = append(out, tunnelPlan{name: t.ID, peer: peer, localID: localID, psk: t.PSK, locals: locals, remotes: remotes})
	}
	slices.SortFunc(out, func(a, b tunnelPlan) int { return cmp.Compare(a.name, b.name) })
	for i := 1; i < len(out); i++ {
		if out[i].name == out[i-1].name {
			return nil, fmt.Errorf("render: two tunnels have the ID %q", out[i].name)
		}
	}
	return out, nil
}

type routePlan struct {
	dest    netip.Prefix
	nextHop netip.Addr
}

func routesOf(in []seam.Route) ([]routePlan, error) {
	out := make([]routePlan, 0, len(in))
	for _, r := range in {
		dest, err := prefix("static route destination", r.Dest, 1)
		if err != nil {
			return nil, err
		}
		hop, err := address("static route "+r.Dest+" next hop", r.NextHop)
		if err != nil {
			return nil, err
		}
		out = append(out, routePlan{dest: dest, nextHop: hop})
	}
	slices.SortFunc(out, func(a, b routePlan) int {
		return cmp.Or(a.dest.Addr().Compare(b.dest.Addr()), cmp.Compare(a.dest.Bits(), b.dest.Bits()), a.nextHop.Compare(b.nextHop))
	})
	return slices.Compact(out), nil
}

type neighborPlan struct {
	address   netip.Addr
	remoteASN uint32
}

type bgpPlan struct {
	localASN  uint32
	neighbors []neighborPlan
}

func bgpOf(in *seam.BGP) (*bgpPlan, error) {
	if in == nil {
		return nil, nil
	}
	if in.LocalASN == 0 {
		return nil, fmt.Errorf("render: BGP has no local AS number")
	}
	out := &bgpPlan{localASN: in.LocalASN}
	for _, n := range in.Neighbors {
		addr, err := address("BGP neighbor", n.Address)
		if err != nil {
			return nil, err
		}
		if n.RemoteASN == 0 {
			return nil, fmt.Errorf("render: BGP neighbor %s has no AS number", n.Address)
		}
		out.neighbors = append(out.neighbors, neighborPlan{address: addr, remoteASN: n.RemoteASN})
	}
	slices.SortFunc(out.neighbors, func(a, b neighborPlan) int { return a.address.Compare(b.address) })
	for i := 1; i < len(out.neighbors); i++ {
		if out.neighbors[i].address == out.neighbors[i-1].address {
			return nil, fmt.Errorf("render: BGP neighbor %s is listed twice", out.neighbors[i].address)
		}
	}
	return out, nil
}

func address(what, raw string) (netip.Addr, error) {
	a, err := netip.ParseAddr(raw)
	if err != nil || !a.Is4() {
		return netip.Addr{}, fmt.Errorf("render: %s %q is not an IPv4 address", what, raw)
	}
	return a, nil
}

// The narrowest bounds of what the source filter admits, which SourceFilter
// holds the running configuration to as well, so that every render passes it.
// A remote network wider than a /8 is one the operator's schema refuses. A
// destination is one address: the operator admits the tenant's pods and
// Services one by one, and on a flat pod network anything wider holds the
// workloads of other tenants too.
const (
	minSourceBits      = 8
	minDestinationBits = 32
)

// prefix parses an IPv4 network written with its host bits clear and at least
// minBits long. Every address, a minBits of 0, is admitted only where it means
// "any" by design: a local selector, which is the tenant's own addresses.
// Anywhere else it is a mistake that would open the gateway or take over its
// default route.
func prefix(what, raw string, minBits int) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(raw)
	switch {
	case err != nil || !p.Addr().Is4():
		return netip.Prefix{}, fmt.Errorf("render: %s %q is not an IPv4 network", what, raw)
	case p != p.Masked():
		return netip.Prefix{}, fmt.Errorf("render: %s %q has host bits set", what, raw)
	case p.Bits() < minBits && p.Bits() == 0:
		return netip.Prefix{}, fmt.Errorf("render: %s %q is every address", what, raw)
	case p.Bits() < minBits:
		return netip.Prefix{}, fmt.Errorf("render: %s %q is wider than a /%d", what, raw, minBits)
	}
	return p, nil
}

func prefixes(what string, raws []string, minBits int) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(raws))
	for _, raw := range raws {
		p, err := prefix(what, raw, minBits)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b netip.Prefix) int {
		return cmp.Or(a.Addr().Compare(b.Addr()), cmp.Compare(a.Bits(), b.Bits()))
	})
	return slices.Compact(out), nil
}

// InSync reports whether the managed subtrees of the running configuration cfg
// hold exactly what ops set, nothing missing and nothing more. It is how a
// gateway that drifted, by a reboot onto its seed or by a hand, is told from one
// that still holds the last render.
func InSync(cfg configtree.Tree, ops []appliance.Op, device string) bool {
	var want, have []string
	for _, op := range ops {
		if op.Op == appliance.OpSet {
			want = append(want, configtree.Leaf{Path: op.Path, Value: op.Value}.Key())
		}
	}
	for _, root := range Managed(device) {
		if v, ok := configtree.Get(cfg, root...); ok {
			for _, l := range configtree.Leaves(v, root) {
				have = append(have, l.Key())
			}
		}
	}
	slices.Sort(want)
	slices.Sort(have)
	return slices.Equal(slices.Compact(want), slices.Compact(have))
}

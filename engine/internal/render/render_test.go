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

package render_test

import (
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	seam "github.com/aenix-io/site-router-operator/api/engine"

	"github.com/aenix-io/barerouter/engine/internal/appliance"
	"github.com/aenix-io/barerouter/engine/internal/configtree"
	"github.com/aenix-io/barerouter/engine/internal/render"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const ingress = "firewall ipv4 name " + render.TunnelIngressRuleSet

// gateway is the state the operator asks for of a gateway with one tunnel to a
// site with two networks, two tenant addresses, a route and BGP.
func gateway() seam.Desired {
	return seam.Desired{
		Tunnels: []seam.Tunnel{{
			ID:              "tunnel-0",
			PeerAddress:     "203.0.113.10",
			PSK:             "golden-psk",
			LocalID:         "198.51.100.1",
			LocalSelectors:  []string{"0.0.0.0/0"},
			RemoteSelectors: []string{"192.0.2.0/24", "172.31.0.0/16"},
		}},
		Routes: []seam.Route{{Dest: "192.0.2.128/25", NextHop: "10.244.1.1"}},
		BGP: &seam.BGP{LocalASN: 65001, Neighbors: []seam.Neighbor{
			{Address: "10.244.1.20", RemoteASN: 65002},
			{Address: "10.244.1.10", RemoteASN: 65002},
		}},
		Guards: seam.Guards{
			ManagementAllow: "10.244.0.0/16",
			TunnelIngress: seam.TunnelIngress{
				Sources:      []string{"192.0.2.0/24", "172.31.0.0/16"},
				Destinations: []string{"10.244.1.6/32", "10.244.1.5/32", "10.96.0.10/32"},
			},
		},
	}
}

func golden(t *testing.T, name string, ops []appliance.Op) {
	t.Helper()
	var b strings.Builder
	for _, op := range ops {
		b.WriteString(op.Op + " " + strings.Join(op.Path, " "))
		if op.Value != "" {
			fmt.Fprintf(&b, " %q", op.Value)
		}
		b.WriteByte('\n')
	}
	file := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v (run the tests with -update to write it)", err)
	}
	if b.String() != string(want) {
		t.Errorf("the render differs from %s; run the tests with -update and review the diff\ngot:\n%s", file, b.String())
	}
}

func TestRenderGolden(t *testing.T) {
	t.Parallel()
	twoTunnels := gateway()
	twoTunnels.Tunnels = append(twoTunnels.Tunnels, seam.Tunnel{
		ID: "branch_b", PeerAddress: "203.0.113.20", PSK: "golden-psk-b",
		LocalSelectors: []string{"0.0.0.0/0"}, RemoteSelectors: []string{"198.51.100.128/25"},
	})
	twoTunnels.BGP = nil
	twoTunnels.Routes = nil
	tests := []struct {
		name   string
		d      seam.Desired
		device string
	}{
		{"gateway", gateway(), "eth0"},
		// The MSS clamp follows the device the tunnel was found on.
		{"gateway-on-eth1", gateway(), "eth1"},
		// Two peers, given out of order; no LocalID, so the gateway presents its
		// own address.
		{"two-tunnels", twoTunnels, "eth0"},
		// Nothing asked for: the guards are still there, closed.
		{"empty", seam.Desired{}, "eth0"},
		// The state of a gateway whose tenant has no workload yet and whose
		// operator has no management network: no new flow, nobody at the API.
		{"tunnel-without-destinations", seam.Desired{Tunnels: gateway().Tunnels, Guards: seam.Guards{
			TunnelIngress: seam.TunnelIngress{Sources: []string{"192.0.2.0/24"}},
		}}, "eth0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ops, err := render.Render(tt.d, tt.device)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, tt.name, ops)
		})
	}
}

// Lists that mean the same render the same, whatever their order and however
// often they repeat an entry, so the revision of the render does not move
// with how the operator happened to list them.
func TestRenderIsCanonical(t *testing.T) {
	t.Parallel()
	shuffled := gateway()
	slices.Reverse(shuffled.Tunnels[0].RemoteSelectors)
	shuffled.Guards.TunnelIngress.Sources = append(slices.Clone(shuffled.Guards.TunnelIngress.Sources), "192.0.2.0/24")
	slices.Reverse(shuffled.Guards.TunnelIngress.Destinations)
	slices.Reverse(shuffled.BGP.Neighbors)
	shuffled.Routes = append(shuffled.Routes, shuffled.Routes[0])
	a, err := render.Render(gateway(), "eth0")
	if err != nil {
		t.Fatal(err)
	}
	b, err := render.Render(shuffled, "eth0")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Error("the same state in another order rendered differently")
	}
}

// Every subtree is deleted before anything is set, so a render replaces what is
// there instead of adding to it, and everything set is in a subtree that is
// deleted, so a render is the whole of what those subtrees hold.
func TestRenderReplacesTheManagedSubtrees(t *testing.T) {
	t.Parallel()
	ops, err := render.Render(gateway(), "eth0")
	if err != nil {
		t.Fatal(err)
	}
	managed := render.Managed("eth0")
	seenSet := false
	var deleted [][]string
	for _, op := range ops {
		switch op.Op {
		case appliance.OpDelete:
			if seenSet {
				t.Errorf("delete %v comes after a set", op.Path)
			}
			deleted = append(deleted, op.Path)
		case appliance.OpSet:
			seenSet = true
			if !slices.ContainsFunc(managed, func(root []string) bool { return hasPrefix(op.Path, root) }) {
				t.Errorf("set %v is outside every managed subtree", op.Path)
			}
		default:
			t.Errorf("unexpected operation %q", op.Op)
		}
	}
	if len(deleted) != len(managed) {
		t.Errorf("deleted %v, want every managed subtree %v", deleted, managed)
	}
}

func hasPrefix(path, prefix []string) bool {
	return len(path) >= len(prefix) && slices.Equal(path[:len(prefix)], prefix)
}

func sets(ops []appliance.Op) map[string][]string {
	out := map[string][]string{}
	for _, op := range ops {
		if op.Op == appliance.OpSet {
			key := strings.Join(op.Path, " ")
			out[key] = append(out[key], op.Value)
		}
	}
	return out
}

func mustRender(t *testing.T, d seam.Desired) []appliance.Op {
	t.Helper()
	ops, err := render.Render(d, "eth0")
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

// A decrypted packet is accepted only from a remote network to an allowed
// destination: an accept by source alone forwards a good source to anywhere.
func TestEveryIngressAcceptNamesASourceAndADestination(t *testing.T) {
	t.Parallel()
	s := sets(mustRender(t, gateway()))
	pairs := map[string]bool{}
	for key := range s {
		rule, ok := strings.CutPrefix(key, ingress+" rule ")
		if !ok || !strings.HasSuffix(rule, " action") || s[key][0] != "accept" {
			continue
		}
		n := strings.TrimSuffix(rule, " action")
		base := ingress + " rule " + n
		if n == "5" {
			if !slices.Equal(s[base+" state"], []string{"established", "related"}) {
				t.Errorf("rule 5 accepts %v, want return traffic only", s[base+" state"])
			}
			continue
		}
		src, dst := s[base+" source address"], s[base+" destination address"]
		if len(src) != 1 || len(dst) != 1 {
			t.Errorf("rule %s accepts without one source and one destination: %v, %v", n, src, dst)
			continue
		}
		pairs[src[0]+">"+dst[0]] = true
	}
	if len(pairs) != 2*3 {
		t.Errorf("accepted %v, want every source with every destination", slices.Sorted(maps.Keys(pairs)))
	}
	if s[ingress+" default-action"][0] != "drop" {
		t.Error("the source filter does not drop by default")
	}
}

func TestNoDestinationAdmitsNoNewFlow(t *testing.T) {
	t.Parallel()
	d := gateway()
	d.Guards.TunnelIngress.Destinations = nil
	for key := range sets(mustRender(t, d)) {
		if strings.HasPrefix(key, ingress+" rule ") && !strings.HasPrefix(key, ingress+" rule 5 ") {
			t.Errorf("with no destination the source filter still has %s", key)
		}
	}
}

func TestTheFilterOnlyTakesDecryptedTraffic(t *testing.T) {
	t.Parallel()
	s := sets(mustRender(t, gateway()))
	fwd := "firewall ipv4 forward filter"
	want := map[string]string{
		fwd + " default-action":                    "drop",
		fwd + " rule 10 action":                    "accept",
		fwd + " rule 10 ipsec match-none-in":       "",
		fwd + " rule 20 action":                    "jump",
		fwd + " rule 20 ipsec match-ipsec-in":      "",
		fwd + " rule 20 jump-target":               render.TunnelIngressRuleSet,
		"firewall ipv4 input filter rule 1 action": "drop",
	}
	for key, value := range want {
		if got := s[key]; len(got) != 1 || got[0] != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
	// Bound to an interface, the filter would also drop the tenant's own
	// traffic into the tunnel, whose sources are never remote networks.
	for key := range s {
		if strings.HasPrefix(key, "interfaces ") && strings.Contains(key, " firewall ") {
			t.Errorf("the filter is bound to an interface: %s", key)
		}
	}
}

// Decrypted traffic to the appliance's own management ports never crosses the
// pod interface, where the cluster's policy is enforced, so the appliance drops
// it, whether or not the management API is open to anyone.
func TestDecryptedTrafficNeverReachesTheManagementAPI(t *testing.T) {
	t.Parallel()
	for _, mgmt := range []string{"10.244.0.0/16", ""} {
		d := gateway()
		d.Guards.ManagementAllow = mgmt
		s := sets(mustRender(t, d))
		rule := "firewall ipv4 input filter rule 1 "
		if !slices.Equal(s[rule+"ipsec match-ipsec-in"], []string{""}) || s[rule+"action"][0] != "drop" ||
			s[rule+"destination port"][0] != "22,443" || s[rule+"protocol"][0] != "tcp" {
			t.Errorf("management %q: rule 1 is %v", mgmt, s)
		}
	}
}

func TestTheManagementAPIIsOpenToTheManagementNetworkOnly(t *testing.T) {
	t.Parallel()
	s := sets(mustRender(t, gateway()))
	if got := s["firewall ipv4 input filter rule 10 source address"]; !slices.Equal(got, []string{"10.244.0.0/16"}) {
		t.Errorf("rule 10 admits %v", got)
	}

	d := gateway()
	d.Guards.ManagementAllow = ""
	s = sets(mustRender(t, d))
	if _, open := s["firewall ipv4 input filter rule 10 action"]; open {
		t.Error("with no management network the management API is open")
	}
	if s["firewall ipv4 input filter default-action"][0] != "drop" {
		t.Error("with no management network the input filter does not drop by default")
	}
}

func TestTunnelAndBGPPortsOpenOnlyWithTunnelsAndNeighbors(t *testing.T) {
	t.Parallel()
	s := sets(mustRender(t, gateway()))
	for _, rule := range []string{"20", "21", "22", "30", "40"} {
		if s["firewall ipv4 input filter rule "+rule+" action"] == nil {
			t.Errorf("rule %s is missing", rule)
		}
	}
	if got := s["firewall ipv4 input filter rule 30 source address"]; !slices.Equal(got, []string{"10.244.1.10"}) {
		t.Errorf("rule 30 admits BGP from %v", got)
	}
	s = sets(mustRender(t, seam.Desired{}))
	for key := range s {
		if strings.HasPrefix(key, "firewall ipv4 input filter rule 2") || strings.HasPrefix(key, "firewall ipv4 input filter rule 3") {
			t.Errorf("with no tunnel and no neighbor %s is set", key)
		}
	}
}

func TestEveryPeerForcesUDPEncapsulation(t *testing.T) {
	t.Parallel()
	s := sets(mustRender(t, gateway()))
	if got := s["vpn ipsec site-to-site peer tunnel-0 force-udp-encapsulation"]; !slices.Equal(got, []string{""}) {
		t.Errorf("force-udp-encapsulation = %v", got)
	}
	if got := s["vpn ipsec authentication psk tunnel-0 id"]; !slices.Equal(got, []string{"203.0.113.10", "198.51.100.1"}) {
		t.Errorf("the key is found by %v, want the peer and the local identity", got)
	}
}

// The gateway carries no IPv6, and an IPv6 chain that is not configured filters
// nothing.
func TestIPv6IsClosed(t *testing.T) {
	t.Parallel()
	for _, d := range []seam.Desired{gateway(), {}} {
		s := sets(mustRender(t, d))
		for _, chain := range []string{"input", "forward"} {
			if got := s["firewall ipv6 "+chain+" filter default-action"]; !slices.Equal(got, []string{"drop"}) {
				t.Errorf("IPv6 %s default-action = %v", chain, got)
			}
		}
		for key := range s {
			if strings.HasPrefix(key, "firewall ipv6 forward filter rule") {
				t.Errorf("IPv6 is forwarded: %s", key)
			}
		}
	}
}

func TestNoNAT(t *testing.T) {
	t.Parallel()
	for _, op := range mustRender(t, gateway()) {
		if op.Path[0] == "nat" {
			t.Errorf("%s %v", op.Op, op.Path)
		}
	}
}

func TestRenderRefuses(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*seam.Desired){
		"a tunnel ID with a dot":            func(d *seam.Desired) { d.Tunnels[0].ID = "a.b" },
		"a tunnel ID like a child SA":       func(d *seam.Desired) { d.Tunnels[0].ID = "a-tunnel-1" },
		"an empty tunnel ID":                func(d *seam.Desired) { d.Tunnels[0].ID = "" },
		"two tunnels with one ID":           func(d *seam.Desired) { d.Tunnels = append(d.Tunnels, d.Tunnels[0]) },
		"no pre-shared key":                 func(d *seam.Desired) { d.Tunnels[0].PSK = "" },
		"a peer that is a name":             func(d *seam.Desired) { d.Tunnels[0].PeerAddress = "peer.example" },
		"an IPv6 peer":                      func(d *seam.Desired) { d.Tunnels[0].PeerAddress = "2001:db8::1" },
		"a local ID that is not an address": func(d *seam.Desired) { d.Tunnels[0].LocalID = "gw\"" },
		"no remote selector":                func(d *seam.Desired) { d.Tunnels[0].RemoteSelectors = nil },
		"a remote selector that is any":     func(d *seam.Desired) { d.Tunnels[0].RemoteSelectors = []string{"0.0.0.0/0"} },
		"host bits in a selector":           func(d *seam.Desired) { d.Tunnels[0].RemoteSelectors = []string{"192.0.2.1/24"} },
		"a destination that is any":         func(d *seam.Desired) { d.Guards.TunnelIngress.Destinations = []string{"0.0.0.0/0"} },
		"a destination that is no network":  func(d *seam.Desired) { d.Guards.TunnelIngress.Destinations = []string{"10.244.1.5"} },
		"a source that is any":              func(d *seam.Desired) { d.Guards.TunnelIngress.Sources = []string{"0.0.0.0/0"} },
		"a source wider than a /8":          func(d *seam.Desired) { d.Guards.TunnelIngress.Sources = []string{"128.0.0.0/1"} },
		"a destination that is a network":   func(d *seam.Desired) { d.Guards.TunnelIngress.Destinations = []string{"10.244.1.0/24"} },
		"a management network that is any":  func(d *seam.Desired) { d.Guards.ManagementAllow = "0.0.0.0/0" },
		"a management network with a quote": func(d *seam.Desired) { d.Guards.ManagementAllow = "10.0.0.0/8\"" },
		"a default route":                   func(d *seam.Desired) { d.Routes[0].Dest = "0.0.0.0/0" },
		"a route to a name":                 func(d *seam.Desired) { d.Routes[0].NextHop = "router" },
		"BGP without a local AS":            func(d *seam.Desired) { d.BGP.LocalASN = 0 },
		"a neighbor without an AS":          func(d *seam.Desired) { d.BGP.Neighbors[0].RemoteASN = 0 },
		"a neighbor listed twice":           func(d *seam.Desired) { d.BGP.Neighbors[1].Address = d.BGP.Neighbors[0].Address },
		"more rules than can be numbered": func(d *seam.Desired) {
			d.Guards.TunnelIngress.Sources = nil
			for i := range 400 {
				d.Guards.TunnelIngress.Sources = append(d.Guards.TunnelIngress.Sources, fmt.Sprintf("172.%d.%d.0/24", 16+i/256, i%256))
			}
			d.Guards.TunnelIngress.Destinations = nil
			for i := range 251 {
				d.Guards.TunnelIngress.Destinations = append(d.Guards.TunnelIngress.Destinations, fmt.Sprintf("10.244.%d.%d/32", i/256, i%256))
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := gateway()
			mutate(&d)
			if ops, err := render.Render(d, "eth0"); err == nil {
				t.Errorf("rendered %d operations", len(ops))
			}
		})
	}
	if _, err := render.Render(gateway(), "eth0.100"); err == nil {
		t.Error("rendered for a device that is not an ethernet device")
	}
}

// apply does to cfg what the appliance does with a commit of ops.
func apply(t *testing.T, cfg configtree.Tree, ops []appliance.Op) configtree.Tree {
	t.Helper()
	out := configtree.Clone(cfg)
	for _, op := range ops {
		switch op.Op {
		case appliance.OpDelete:
			configtree.Delete(out, op.Path)
		case appliance.OpSet:
			if err := configtree.Set(out, op.Path, op.Value); err != nil {
				t.Fatal(err)
			}
		}
	}
	return out
}

// unmanaged is configuration the engine does not own, which a render leaves as
// it is and InSync does not look at.
func unmanaged(t *testing.T) configtree.Tree {
	t.Helper()
	cfg := configtree.Tree{}
	for _, p := range [][]string{
		{"interfaces", "ethernet", "eth0", "address", "dhcp"},
		{"service", "https", "api", "keys", "id", "operator", "key", "k"},
		{"firewall", "ipv4", "name", "OTHER", "default-action", "accept"},
	} {
		if err := configtree.Set(cfg, p[:len(p)-1], p[len(p)-1]); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

func TestInSync(t *testing.T) {
	t.Parallel()
	ops := mustRender(t, gateway())
	applied := apply(t, unmanaged(t), ops)
	if !render.InSync(applied, ops, "eth0") {
		t.Fatal("a configuration that holds the render is not in sync with it")
	}
	if render.InSync(unmanaged(t), ops, "eth0") {
		t.Error("a configuration that never held the render is in sync with it")
	}

	drifts := map[string]func(configtree.Tree){
		"a rule more": func(c configtree.Tree) {
			_ = configtree.Set(c, []string{"firewall", "ipv4", "forward", "filter", "rule", "1", "action"}, "accept")
		},
		"the jump gone": func(c configtree.Tree) {
			configtree.Delete(c, []string{"firewall", "ipv4", "forward", "filter", "rule", "20"})
		},
		"another key": func(c configtree.Tree) {
			configtree.Delete(c, []string{"vpn", "ipsec", "authentication", "psk", "tunnel-0", "secret"})
			_ = configtree.Set(c, []string{"vpn", "ipsec", "authentication", "psk", "tunnel-0", "secret"}, "other")
		},
		"a peer more": func(c configtree.Tree) {
			_ = configtree.Set(c, []string{"vpn", "ipsec", "site-to-site", "peer", "rogue", "remote-address"}, "192.0.2.1")
		},
		"a route gone": func(c configtree.Tree) { configtree.Delete(c, []string{"protocols", "static", "route"}) },
		"IPv6 open": func(c configtree.Tree) {
			configtree.Delete(c, []string{"firewall", "ipv6", "input", "filter", "default-action"})
			_ = configtree.Set(c, []string{"firewall", "ipv6", "input", "filter", "default-action"}, "accept")
		},
		"the clamp gone": func(c configtree.Tree) {
			configtree.Delete(c, []string{"interfaces", "ethernet", "eth0", "ip", "adjust-mss"})
		},
	}
	for name, drift := range drifts {
		c := configtree.Clone(applied)
		drift(c)
		if render.InSync(c, ops, "eth0") {
			t.Errorf("%s: still in sync", name)
		}
	}

	c := configtree.Clone(applied)
	_ = configtree.Set(c, []string{"system", "host-name"}, "renamed")
	if !render.InSync(c, ops, "eth0") {
		t.Error("a change outside the managed subtrees broke the sync")
	}
}

// Whatever the gateway held before, a render leaves it holding a source filter
// that SourceFilter vouches for, as long as there is a destination.
func TestARenderLeavesAnActiveSourceFilter(t *testing.T) {
	t.Parallel()
	drifted := unmanaged(t)
	_ = configtree.Set(drifted, []string{"firewall", "ipv4", "forward", "filter", "rule", "1", "action"}, "accept")
	_ = configtree.Set(drifted, []string{"firewall", "ipv4", "name", render.TunnelIngressRuleSet, "rule", "7", "action"}, "accept")
	cfg := apply(t, drifted, mustRender(t, gateway()))
	if active, detail := render.SourceFilter(cfg, nil); !active {
		t.Errorf("not active after a render: %s", detail)
	}
	allowed, err := render.AllowedBy(gateway().Guards.TunnelIngress)
	if err != nil {
		t.Fatal(err)
	}
	if active, detail := render.SourceFilter(cfg, &allowed); !active {
		t.Errorf("not active after a render, held to what it was rendered from: %s", detail)
	}

	d := gateway()
	d.Guards.TunnelIngress.Destinations = nil
	cfg = apply(t, drifted, mustRender(t, d))
	if active, _ := render.SourceFilter(cfg, nil); active {
		t.Error("a filter that admits no destination is reported active")
	}
}

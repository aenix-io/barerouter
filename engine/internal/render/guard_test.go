/*
Copyright 2026 The Aenix Authors.

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
	"encoding/json"
	"strings"
	"testing"

	"github.com/aenix-io/barerouter/engine/internal/configtree"
	"github.com/aenix-io/barerouter/engine/internal/render"
)

var (
	fwd = []string{"firewall", "ipv4", "forward", "filter"}
	ti  = []string{"firewall", "ipv4", "name", render.TunnelIngressRuleSet}
)

func at(base []string, sub ...string) []string { return append(append([]string{}, base...), sub...) }

// rendered is the running configuration of a gateway the engine configured.
func rendered(t *testing.T) configtree.Tree {
	t.Helper()
	return apply(t, unmanaged(t), mustRender(t, gateway()))
}

func allowedByGateway(t *testing.T) *render.Allowed {
	t.Helper()
	a, err := render.AllowedBy(gateway().Guards.TunnelIngress)
	if err != nil {
		t.Fatal(err)
	}
	return &a
}

func setter(t *testing.T) func(configtree.Tree, []string, string) {
	return func(c configtree.Tree, path []string, value string) {
		t.Helper()
		if err := configtree.Set(c, path, value); err != nil {
			t.Fatal(err)
		}
	}
}

// replace sets the one value of a leaf, whatever it held.
func replace(t *testing.T, c configtree.Tree, path []string, value string) {
	t.Helper()
	configtree.Delete(c, path)
	setter(t)(c, path, value)
}

// The ways the guard can be taken down or got around, by a reboot onto the seed,
// by hand, or by a render that went wrong. Each reads as not active, whether or
// not the caller knows what it applied.
func TestSourceFilterIsNotActiveWhenItCanBeGotAround(t *testing.T) {
	t.Parallel()
	set := setter(t)
	dst := at(ti, "rule", "10", "destination", "address")
	src := at(ti, "rule", "10", "source", "address")
	tests := map[string]struct {
		breakIt func(configtree.Tree)
		detail  string
	}{
		"no forward filter": {func(c configtree.Tree) { configtree.Delete(c, fwd) }, "forward filter is missing"},
		"forward accepts by default": {func(c configtree.Tree) {
			replace(t, c, at(fwd, "default-action"), "accept")
		}, "does not drop by default"},
		"the jump removed":  {func(c configtree.Tree) { configtree.Delete(c, at(fwd, "rule", "20")) }, "no forward rule"},
		"the jump disabled": {func(c configtree.Tree) { set(c, at(fwd, "rule", "20", "disable"), "") }, "no forward rule"},
		"the jump goes elsewhere": {func(c configtree.Tree) {
			replace(t, c, at(fwd, "rule", "20", "jump-target"), "OTHER")
		}, "forward rule 20"},
		"an accept before the jump": {func(c configtree.Tree) { set(c, at(fwd, "rule", "15", "action"), "accept") }, "forward rule 15"},
		"an accept of new traffic before the jump": {func(c configtree.Tree) {
			set(c, at(fwd, "rule", "1", "action"), "accept")
			set(c, at(fwd, "rule", "1", "state"), "new")
		}, "forward rule 1"},
		"an accept that matches decrypted traffic too": {func(c configtree.Tree) {
			set(c, at(fwd, "rule", "10", "ipsec", "match-ipsec-in"), "")
		}, "forward rule 10"},
		// A matcher on the jump leaves the rest of the decrypted traffic to the
		// rules after it.
		"a jump for one source only": {func(c configtree.Tree) {
			set(c, at(fwd, "rule", "20", "source", "address"), "192.0.2.0/24")
		}, "forward rule 20 sends only part"},
		"a jump for one protocol only": {func(c configtree.Tree) { set(c, at(fwd, "rule", "20", "protocol"), "tcp") }, "forward rule 20 sends only part"},
		"a jump for new traffic only":  {func(c configtree.Tree) { set(c, at(fwd, "rule", "20", "state"), "new") }, "forward rule 20 sends only part"},
		"a jump on one interface only": {func(c configtree.Tree) {
			set(c, at(fwd, "rule", "20", "inbound-interface", "name"), "eth1")
		}, "forward rule 20 sends only part"},
		"a jump with another IPsec match": {func(c configtree.Tree) {
			set(c, at(fwd, "rule", "20", "ipsec", "match-none-in"), "")
		}, "forward rule 20 sends only part"},
		"an accept after the jump": {func(c configtree.Tree) { set(c, at(fwd, "rule", "30", "action"), "accept") }, "forward rule 30"},
		"a second jump after the jump": {func(c configtree.Tree) {
			set(c, at(fwd, "rule", "30", "action"), "jump")
			set(c, at(fwd, "rule", "30", "jump-target"), render.TunnelIngressRuleSet)
		}, "forward rule 30"},
		"no source filter": {func(c configtree.Tree) { configtree.Delete(c, ti) }, "source filter is missing"},
		"the source filter accepts by default": {func(c configtree.Tree) {
			replace(t, c, at(ti, "default-action"), "accept")
		}, "does not drop by default"},
		"an accept by source alone": {func(c configtree.Tree) {
			configtree.Delete(c, at(ti, "rule", "10", "destination"))
		}, "rule 10"},
		"an accept of everything but one destination": {func(c configtree.Tree) { replace(t, c, dst, "!10.244.1.5/32") }, "rule 10"},
		"an accept to every destination":              {func(c configtree.Tree) { replace(t, c, dst, "0.0.0.0/0") }, "rule 10"},
		"an accept to half of every address":          {func(c configtree.Tree) { replace(t, c, dst, "0.0.0.0/1") }, "rule 10"},
		"an accept to the other half":                 {func(c configtree.Tree) { replace(t, c, dst, "128.0.0.0/1") }, "rule 10"},
		"an accept to a range of every address":       {func(c configtree.Tree) { replace(t, c, dst, "0.0.0.0-255.255.255.255") }, "rule 10"},
		"an accept to a network, not an address":      {func(c configtree.Tree) { replace(t, c, dst, "10.244.1.0/24") }, "rule 10"},
		"an accept to a network with host bits":       {func(c configtree.Tree) { replace(t, c, dst, "10.244.1.5/24") }, "rule 10"},
		"an accept to a name":                         {func(c configtree.Tree) { replace(t, c, dst, "example.com") }, "rule 10"},
		"an accept to an IPv6 address":                {func(c configtree.Tree) { replace(t, c, dst, "2001:db8::1") }, "rule 10"},
		"an accept to something unreadable":           {func(c configtree.Tree) { replace(t, c, dst, "any") }, "rule 10"},
		"an accept to two destinations": {func(c configtree.Tree) {
			set(c, dst, "10.244.9.9/32")
		}, "rule 10"},
		"an accept from a source wider than a /8": {func(c configtree.Tree) { replace(t, c, src, "128.0.0.0/1") }, "rule 10"},
		"an accept from a range of sources": {func(c configtree.Tree) {
			replace(t, c, src, "0.0.0.0-255.255.255.255")
		}, "rule 10"},
		"a return to the forward chain": {func(c configtree.Tree) { set(c, at(ti, "rule", "1", "action"), "return") }, "rule 1"},
		"a rule without an action":      {func(c configtree.Tree) { set(c, at(ti, "rule", "1", "protocol"), "tcp") }, "rule 1"},
		"a rule numbered with a word": {func(c configtree.Tree) {
			set(c, at(ti, "rule", "first", "action"), "drop")
		}, "cannot be read"},
		"a rule that is a value": {func(c configtree.Tree) { set(c, at(fwd, "rule", "99"), "accept") }, "cannot be read"},
		"no destination admitted": {func(c configtree.Tree) {
			for _, r := range []string{"10", "20", "30", "40", "50", "60"} {
				configtree.Delete(c, at(ti, "rule", r))
			}
		}, "admits no destination"},
	}
	for name, tt := range tests {
		for _, known := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				var allowed *render.Allowed
				if known {
					allowed = allowedByGateway(t)
				}
				c := rendered(t)
				if active, detail := render.SourceFilter(c, allowed); !active {
					t.Fatalf("not active before it was broken: %s", detail)
				}
				tt.breakIt(c)
				active, detail := render.SourceFilter(c, allowed)
				if active {
					t.Fatal("still active")
				}
				if !strings.Contains(detail, tt.detail) {
					t.Errorf("detail %q, want it to say %q", detail, tt.detail)
				}
			})
		}
	}
}

// node returns the node at path of c, which the test is about to put a shape
// into that Set does not make.
func node(t *testing.T, c configtree.Tree, path ...string) configtree.Tree {
	t.Helper()
	n, ok := configtree.Node(c, path...)
	if !ok {
		t.Fatalf("no node %v", path)
	}
	return n
}

// What the check cannot read with certainty it does not vouch for: a rule number
// the appliance could read as another rule, a zone-based firewall that decides
// forwarding besides the forward filter, and every shape of the leaves it reads
// other than the one it takes the API to render.
func TestSourceFilterIsNotActiveWhenItCannotRead(t *testing.T) {
	t.Parallel()
	set := setter(t)
	tests := map[string]struct {
		breakIt func(*testing.T, configtree.Tree)
		detail  string
	}{
		"a rule number with a leading zero": {func(t *testing.T, c configtree.Tree) {
			set(c, at(ti, "rule", "05", "action"), "drop")
		}, "cannot be read"},
		"a rule number with a sign": {func(t *testing.T, c configtree.Tree) {
			set(c, at(fwd, "rule", "+5", "action"), "drop")
		}, "cannot be read"},
		"a negative rule number": {func(t *testing.T, c configtree.Tree) {
			set(c, at(fwd, "rule", "-1", "action"), "drop")
		}, "cannot be read"},
		"a zone-based firewall": {func(t *testing.T, c configtree.Tree) {
			set(c, []string{"firewall", "zone", "LAN", "interface", "name"}, "eth0")
		}, "zone-based firewall"},
		"state as a node of flags": {func(t *testing.T, c configtree.Tree) {
			configtree.Delete(c, at(ti, "rule", "5", "state"))
			set(c, at(ti, "rule", "5", "state", "established"), "")
		}, "rule 5 cannot be read"},
		"state as a number": {func(t *testing.T, c configtree.Tree) {
			node(t, c, at(ti, "rule", "5")...)["state"] = json.Number("1")
		}, "rule 5 cannot be read"},
		"state as a list with a non-string": {func(t *testing.T, c configtree.Tree) {
			node(t, c, at(fwd, "rule", "5")...)["state"] = []any{"established", map[string]any{}}
		}, "rule 5 cannot be read"},
		"disable with a value": {func(t *testing.T, c configtree.Tree) {
			set(c, at(fwd, "rule", "15", "action"), "accept")
			set(c, at(fwd, "rule", "15", "disable"), "false")
		}, "rule 15 cannot be read"},
		"disable with something under it": {func(t *testing.T, c configtree.Tree) {
			set(c, at(fwd, "rule", "15", "action"), "accept")
			set(c, at(fwd, "rule", "15", "disable", "when"), "never")
		}, "rule 15 cannot be read"},
		"ipsec as a list": {func(t *testing.T, c configtree.Tree) {
			node(t, c, at(fwd, "rule", "20")...)["ipsec"] = []any{"match-ipsec-in"}
		}, "rule 20 cannot be read"},
		"an ipsec flag with a value": {func(t *testing.T, c configtree.Tree) {
			node(t, c, at(fwd, "rule", "20", "ipsec")...)["match-ipsec-in"] = "yes"
		}, "rule 20 cannot be read"},
		"ipsec with nothing under it": {func(t *testing.T, c configtree.Tree) {
			node(t, c, at(fwd, "rule", "20")...)["ipsec"] = map[string]any{}
		}, "rule 20 cannot be read"},
		"an address as a node": {func(t *testing.T, c configtree.Tree) {
			configtree.Delete(c, at(ti, "rule", "10", "destination", "address"))
			set(c, at(ti, "rule", "10", "destination", "address", "10.244.1.5/32"), "")
		}, "rule 10 cannot be read"},
		"an address list with a non-string": {func(t *testing.T, c configtree.Tree) {
			node(t, c, at(ti, "rule", "10", "source")...)["address"] = []any{json.Number("1")}
		}, "rule 10 cannot be read"},
		"a source that is a value": {func(t *testing.T, c configtree.Tree) {
			node(t, c, at(ti, "rule", "10")...)["source"] = "172.31.0.0/16"
		}, "rule 10 cannot be read"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := rendered(t)
			tt.breakIt(t, c)
			for _, allowed := range []*render.Allowed{nil, allowedByGateway(t)} {
				active, detail := render.SourceFilter(c, allowed)
				if active {
					t.Fatal("still active")
				}
				if !strings.Contains(detail, tt.detail) {
					t.Errorf("detail %q, want it to say %q", detail, tt.detail)
				}
			}
		})
	}
}

// What narrows the filter, or stops a packet, does not take it down.
func TestSourceFilterStaysActiveWhenNarrowed(t *testing.T) {
	t.Parallel()
	tests := map[string][][]string{
		"a drop before the jump":            {at(fwd, "rule", "15", "action", "drop")},
		"a drop after the jump":             {at(fwd, "rule", "30", "action", "drop")},
		"a disabled accept before the jump": {at(fwd, "rule", "15", "action", "accept"), at(fwd, "rule", "15", "disable", "")},
		"a description and a log on the jump": {
			at(fwd, "rule", "20", "description", "decrypted traffic"), at(fwd, "rule", "20", "log", ""),
		},
		"a reject in the source filter": {at(ti, "rule", "1", "action", "reject")},
		"a protocol on an accept":       {at(ti, "rule", "10", "protocol", "tcp")},
		"a group on the destination side of an accept": {
			at(ti, "rule", "10", "destination", "group", "address-group", "PODS"),
		},
	}
	for name, paths := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := rendered(t)
			for _, p := range paths {
				setter(t)(c, p[:len(p)-1], p[len(p)-1])
			}
			if active, detail := render.SourceFilter(c, allowedByGateway(t)); !active {
				t.Errorf("not active: %s", detail)
			}
		})
	}
}

// Held to what was applied, a filter that admits another flow is not the one
// applied, however well formed it is.
func TestSourceFilterHoldsTheFilterToTheAppliedState(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(configtree.Tree){
		"another destination": func(c configtree.Tree) {
			replace(t, c, at(ti, "rule", "10", "destination", "address"), "10.244.9.9/32")
		},
		"another source": func(c configtree.Tree) {
			replace(t, c, at(ti, "rule", "10", "source", "address"), "198.51.100.0/24")
		},
		"a narrower source than applied": func(c configtree.Tree) {
			replace(t, c, at(ti, "rule", "10", "source", "address"), "172.31.1.0/24")
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := rendered(t)
			edit(c)
			if active, _ := render.SourceFilter(c, nil); !active {
				t.Fatal("the edit broke the structure; it is meant to change only what is admitted")
			}
			active, detail := render.SourceFilter(c, allowedByGateway(t))
			if active || !strings.Contains(detail, "rule 10") {
				t.Errorf("got %v, %q, want not active naming rule 10", active, detail)
			}
		})
	}
}

// The seed a gateway first boots on has the forward filter and no source filter
// yet: the guard is not up until the first Apply.
func TestSourceFilterIsNotActiveOnTheSeed(t *testing.T) {
	t.Parallel()
	c := rendered(t)
	configtree.Delete(c, ti)
	configtree.Delete(c, at(fwd, "rule", "20"))
	if active, _ := render.SourceFilter(c, nil); active {
		t.Error("active without a source filter")
	}
	if active, _ := render.SourceFilter(configtree.Tree{}, nil); active {
		t.Error("active on an empty configuration")
	}
}

// detail is for a condition a tenant reads; it names rules, not addresses.
func TestSourceFilterDetailNamesNoAddress(t *testing.T) {
	t.Parallel()
	c := rendered(t)
	replace(t, c, at(ti, "rule", "10", "destination", "address"), "10.244.9.9/32")
	for _, allowed := range []*render.Allowed{nil, allowedByGateway(t)} {
		_, detail := render.SourceFilter(c, allowed)
		for _, addr := range []string{"192.0.2.", "172.31.", "10.244.", "10.96."} {
			if strings.Contains(detail, addr) {
				t.Errorf("detail %q quotes an address", detail)
			}
		}
	}
}

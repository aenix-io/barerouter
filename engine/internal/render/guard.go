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

package render

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"

	seam "github.com/aenix-io/site-router-operator/api/engine"

	"github.com/aenix-io/barerouter/engine/internal/configtree"
)

// The check of the source filter reads the running configuration in the JSON
// the management API renders it in, and takes that JSON to have this shape: a
// leaf with one value is a string, a leaf with several is a list of strings,
// and a valueless leaf, such as `disable` or `match-ipsec-in` under `ipsec`, is
// an empty object. No answer of a real
// gateway has been captured to confirm it for the leaves the check reads:
// `state`, `disable`, `ipsec` and `address`. So any other shape of those is a
// rule the check cannot read, and a filter it does not vouch for. If a real
// gateway renders one of them otherwise, Confirm reports the filter down until
// the check learns that shape; it never reports it up.

// Allowed is what a state lets the source filter admit.
type Allowed struct {
	Sources, Destinations []netip.Prefix
}

// AllowedBy returns what ingress lets the source filter admit, parsed as Render
// parses it.
func AllowedBy(ingress seam.TunnelIngress) (Allowed, error) {
	sources, err := prefixes("tunnel ingress source", ingress.Sources, minSourceBits)
	if err != nil {
		return Allowed{}, err
	}
	destinations, err := prefixes("tunnel ingress destination", ingress.Destinations, minDestinationBits)
	if err != nil {
		return Allowed{}, err
	}
	return Allowed{Sources: sources, Destinations: destinations}, nil
}

// SourceFilter reports whether the running configuration cfg filters decrypted
// traffic: every decrypted packet that is not return traffic is sent to the
// source filter, and the source filter admits a new flow only from one remote
// network to one address, and admits at least one. It judges what the rules do,
// not whether they are the rules Render wrote, so a gateway changed by hand or
// rebooted onto its seed reads as down. A rule it cannot read, or one that does
// something it does not know, is a filter it does not vouch for.
//
// With allowed, every flow the filter admits also has to be one allowed admits.
// Without it, which is what a caller has that has not applied a state, only the
// structure is judged.
//
// No admitted flow at all is "not active": such a filter is safe, since it
// admits nothing new, but a gateway behind it carries no new flow, and nothing
// should be routed to it on its strength. detail says what is wrong, naming
// rules by number and never quoting an address.
func SourceFilter(cfg configtree.Tree, allowed *Allowed) (active bool, detail string) {
	// Between zones, zone policies decide what is forwarded, and the forward
	// filter is not the whole of it. The engine configures no zones, so a
	// gateway that has them is not one whose filter the engine can vouch for.
	if _, ok := configtree.Get(cfg, "firewall", "zone"); ok {
		return false, "a zone-based firewall decides what the gateway forwards, besides the forward filter"
	}
	if ok, detail := forwardSendsDecryptedToTheFilter(cfg); !ok {
		return false, detail
	}
	return filterAdmitsOnlyAllowedFlows(cfg, allowed)
}

func forwardSendsDecryptedToTheFilter(cfg configtree.Tree) (bool, string) {
	fwd, ok := configtree.Node(cfg, forwardFilter()...)
	if !ok {
		return false, "the forward filter is missing"
	}
	if configtree.Value(fwd, "default-action") != "drop" {
		return false, "the forward filter does not drop by default"
	}
	rules, err := rulesOf(fwd)
	if err != nil {
		return false, "the forward filter " + err.Error()
	}
	jumped := false
	for _, r := range rules {
		switch {
		case r.disabled:
		case !jumped && r.action() == "jump" && configtree.Value(r.body, "jump-target") == TunnelIngressRuleSet:
			// The jump takes all decrypted traffic or it is not the guard: a
			// matcher on it lets the rest of the decrypted traffic fall through to
			// the rules after it.
			if !r.only("action", "jump-target", "ipsec", "description", "log", "log-options") || !r.ipsecOnly("match-ipsec-in") {
				return false, fmt.Sprintf("forward rule %s sends only part of the decrypted traffic to the source filter", r.number)
			}
			jumped = true
		case r.action() == "drop" || r.action() == "reject":
		case r.action() == "accept" && (r.returnOnly() || r.ipsecOnly("match-none-in")):
		default:
			return false, fmt.Sprintf("forward rule %s lets decrypted traffic past the source filter", r.number)
		}
	}
	if !jumped {
		return false, "no forward rule sends decrypted traffic to the source filter"
	}
	return true, ""
}

func filterAdmitsOnlyAllowedFlows(cfg configtree.Tree, allowed *Allowed) (bool, string) {
	ingress, ok := configtree.Node(cfg, tunnelIngress()...)
	if !ok {
		return false, "the source filter is missing"
	}
	if configtree.Value(ingress, "default-action") != "drop" {
		return false, "the source filter does not drop by default"
	}
	rules, err := rulesOf(ingress)
	if err != nil {
		return false, "the source filter " + err.Error()
	}
	admitted := 0
	for _, r := range rules {
		switch {
		case r.disabled, r.action() == "drop", r.action() == "reject":
		case r.action() == "accept" && r.returnOnly():
		case r.action() == "accept":
			src, srcOK := r.address("source", minSourceBits)
			dst, dstOK := r.address("destination", minDestinationBits)
			if !srcOK || !dstOK {
				return false, fmt.Sprintf("rule %s of the source filter admits flows that are not from one remote network to one address", r.number)
			}
			if allowed != nil && (!slices.Contains(allowed.Sources, src) || !slices.Contains(allowed.Destinations, dst)) {
				return false, fmt.Sprintf("rule %s of the source filter admits a flow the applied state does not allow", r.number)
			}
			admitted++
		default:
			return false, fmt.Sprintf("rule %s of the source filter does something other than accept or drop", r.number)
		}
	}
	if admitted == 0 {
		return false, "the source filter admits no destination"
	}
	return true, ""
}

type rule struct {
	number   string
	n        int
	body     configtree.Tree
	disabled bool
}

// rulesOf returns the rules of a chain in the order the appliance evaluates
// them, which is by number. The appliance keys a rule by its number as written,
// so a key that is not the plain decimal form of a number, such as "05" or
// "+5", could be another rule than the one it reads as here, and is not read.
func rulesOf(chain configtree.Tree) ([]rule, error) {
	raw, ok := chain["rule"]
	if !ok {
		return nil, nil
	}
	byNumber, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("has rules that cannot be read")
	}
	rules := make([]rule, 0, len(byNumber))
	for key, v := range byNumber {
		n, err := strconv.Atoi(key)
		body, isNode := v.(map[string]any)
		if err != nil || n < 0 || strconv.Itoa(n) != key || !isNode {
			return nil, fmt.Errorf("has a rule that cannot be read")
		}
		r := rule{number: key, n: n, body: body}
		if err := r.read(); err != nil {
			return nil, fmt.Errorf("rule %s cannot be read: %w", key, err)
		}
		rules = append(rules, r)
	}
	slices.SortFunc(rules, func(a, b rule) int { return a.n - b.n })
	return rules, nil
}

// read checks that the leaves the check reads have the shapes it knows, and
// notes whether the rule is disabled.
func (r *rule) read() error {
	if v, ok := r.body["disable"]; ok {
		if !valueless(v) {
			return fmt.Errorf("disable is not a valueless leaf")
		}
		r.disabled = true
	}
	if v, ok := r.body["state"]; ok && !strings1(v) {
		return fmt.Errorf("state is not one value or a list of them")
	}
	if v, ok := r.body["ipsec"]; ok {
		m, isNode := v.(map[string]any)
		if !isNode || len(m) == 0 {
			return fmt.Errorf("ipsec is not a node of flags")
		}
		for _, flag := range m {
			if !valueless(flag) {
				return fmt.Errorf("ipsec has a flag that is not valueless")
			}
		}
	}
	for _, side := range []string{"source", "destination"} {
		v, ok := r.body[side]
		if !ok {
			continue
		}
		m, isNode := v.(map[string]any)
		if !isNode {
			return fmt.Errorf("%s is not a node", side)
		}
		if a, ok := m["address"]; ok && !strings1(a) {
			return fmt.Errorf("the %s address is not one value or a list of them", side)
		}
	}
	return nil
}

// valueless is the shape of a leaf without a value: an empty object.
func valueless(v any) bool {
	m, ok := v.(map[string]any)
	return ok && len(m) == 0
}

// strings1 is the shape of a leaf with values: a string, or a non-empty list of
// strings.
func strings1(v any) bool {
	switch t := v.(type) {
	case string:
		return true
	case []any:
		return len(t) > 0 && !slices.ContainsFunc(t, func(e any) bool {
			_, ok := e.(string)
			return !ok
		})
	}
	return false
}

func (r rule) action() string { return configtree.Value(r.body, "action") }

// only reports whether the rule has nothing but the given keys.
func (r rule) only(keys ...string) bool {
	for k := range r.body {
		if !slices.Contains(keys, k) {
			return false
		}
	}
	return true
}

// ipsecOnly reports whether the rule matches on flag, and on no other IPsec
// property.
func (r rule) ipsecOnly(flag string) bool {
	m, ok := configtree.Node(r.body, "ipsec")
	if !ok || len(m) != 1 {
		return false
	}
	_, ok = m[flag]
	return ok
}

// returnOnly is an accept of established and related traffic and nothing else.
// Any other matcher on it only narrows it further.
func (r rule) returnOnly() bool {
	states := configtree.Values(r.body, "state")
	return len(states) > 0 && !slices.ContainsFunc(states, func(s string) bool {
		return s != "established" && s != "related"
	})
}

// address returns the one network the rule matches on side, "source" or
// "destination", when it is an IPv4 address or network written plainly, with
// its host bits clear and at least minBits long. A range, a negation or
// anything else that does not parse so does not count as a bound, since it can
// stand for any number of addresses. Every other matcher of a rule, a group or a
// domain name on the same side included, is matched together with the address
// and only narrows what it admits, so they are not looked at.
func (r rule) address(side string, minBits int) (netip.Prefix, bool) {
	addrs := configtree.Values(r.body, side, "address")
	if len(addrs) != 1 {
		return netip.Prefix{}, false
	}
	p, err := netip.ParsePrefix(addrs[0])
	if err != nil {
		a, aerr := netip.ParseAddr(addrs[0])
		if aerr != nil {
			return netip.Prefix{}, false
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	if !p.Addr().Is4() || p != p.Masked() || p.Bits() < minBits {
		return netip.Prefix{}, false
	}
	return p, true
}

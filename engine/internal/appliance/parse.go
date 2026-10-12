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

package appliance

import (
	"regexp"
	"strings"
)

// ParseIPSecSA reads the text of `show vpn ipsec sa`. Lines it does not
// recognise are skipped, so a connection that is missing is "not seen", not an
// error. Two shapes are read.
//
// The table the image prints, whose column widths follow the content, so a row
// is split on runs of whitespace and never at fixed offsets:
//
//	Connection        State  Uptime  Bytes In/Out  ...  Remote address  ...
//	----------------  -----  ------  ------------  ...
//	router1-tunnel-1  up     44m41s  0B/0B         ...  10.244.2.123     ...
//
// And the swanctl listing of older releases:
//
//	peer-name:  203.0.113.15...203.0.113.10  IKEv2
//	peer-name[1]: ESTABLISHED 5 seconds ago, ...
//	peer-name{1}:  INSTALLED, TUNNEL, ...
func ParseIPSecSA(text string) []IPSecObservation {
	type entry struct {
		obs   IPSecObservation
		known bool
	}
	var order []string
	byName := map[string]*entry{}
	// note records what a line says about a connection. A summary line says
	// only that it exists, which is Down until a state line says otherwise. Up
	// wins over the rest, because a routed connection can list INSTALLED after
	// ESTABLISHED; otherwise the first state seen stays.
	note := func(name string, state IPSecState, known bool, addr string) {
		e, ok := byName[name]
		if !ok {
			e = &entry{obs: IPSecObservation{Name: name, State: IPSecDown}}
			byName[name] = e
			order = append(order, name)
		}
		if known && (!e.known || state == IPSecUp) {
			e.obs.State = state
			e.known = true
		}
		if e.obs.PeerAddress == "" {
			e.obs.PeerAddress = addr
		}
	}

	for line := range strings.SplitSeq(text, "\n") {
		if m := swanctlHeaderRe.FindStringSubmatch(line); m != nil {
			note(m[1], IPSecDown, false, m[3])
			continue
		}
		if m := swanctlStateRe.FindStringSubmatch(line); m != nil {
			if isIPSecStateWord(m[2]) {
				note(m[1], ipsecState(m[2]), true, "")
			}
			continue
		}
		if f := strings.Fields(line); len(f) >= 2 && isIPSecStateWord(f[1]) {
			addr := ""
			if len(f) >= 6 {
				addr = f[5]
			}
			note(f[0], ipsecState(f[1]), true, addr)
		}
	}

	out := make([]IPSecObservation, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name].obs)
	}
	return out
}

// swanctlHeaderRe matches the summary line of a connection:
//
//	name:  local-ip...remote-ip  IKEv*
var swanctlHeaderRe = regexp.MustCompile(`^\s*([A-Za-z0-9._-]+):\s+([\d.]+)\.{3}([\d.]+)`)

// swanctlStateRe matches the state line of an IKE SA (name[1]) or a child SA
// (name{1}).
var swanctlStateRe = regexp.MustCompile(`^\s*([A-Za-z0-9._-]+)[\[\{]\d+[\]\}]:\s*([A-Za-z_]+)`)

// isIPSecStateWord tells a state from the header ("State"), the dashed
// separator and the other words that can stand where a state does.
func isIPSecStateWord(s string) bool {
	switch strings.ToUpper(s) {
	case "UP", "DOWN", "CONNECTING", "ESTABLISHED", "INSTALLED", "CREATED", "REKEYING":
		return true
	}
	return false
}

func ipsecState(raw string) IPSecState {
	switch strings.ToUpper(raw) {
	case "UP", "ESTABLISHED", "INSTALLED":
		return IPSecUp
	case "CONNECTING", "CREATED", "REKEYING":
		return IPSecConnecting
	}
	return IPSecDown
}

// ParseInterfacesDetail reads the physical ethernet devices and their MAC
// addresses from the text of `show interfaces detail`, which follows the layout
// of `ip addr`: a header line per interface, with or without the ifindex in
// front, and an indented `link/ether` line under it. Loopback, VLAN
// sub-interfaces, tunnels and bridges are skipped.
func ParseInterfacesDetail(text string) []EthernetObservation {
	var out []EthernetObservation
	current := ""
	for line := range strings.SplitSeq(text, "\n") {
		if m := ethHeaderRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		if anyHeaderRe.MatchString(line) {
			// Any other interface closes the section of the last ethernet one,
			// so its link/ether line is not taken for that device's.
			current = ""
			continue
		}
		if m := linkEtherRe.FindStringSubmatch(line); m != nil && current != "" {
			out = append(out, EthernetObservation{Device: current, MAC: strings.ToLower(m[1])})
			current = ""
		}
	}
	return out
}

// ethHeaderRe matches the header of a plain ethernet device. A VLAN
// sub-interface ("eth1.100@eth1:") does not match: the name has to be followed
// by the colon right after its digits.
var ethHeaderRe = regexp.MustCompile(`^\s*(?:\d+:\s+)?(eth\d+):\s+<`)

// anyHeaderRe matches the header of any interface. Headers start at column 0,
// after the optional ifindex, unlike the indented attribute lines.
var anyHeaderRe = regexp.MustCompile(`^(?:\d+:\s+)?\S+:\s+<`)

var linkEtherRe = regexp.MustCompile(`^\s*link/ether\s+([0-9A-Fa-f]{2}(?::[0-9A-Fa-f]{2}){5})\b`)

// ParseBGPSummary reads the neighbor table of `show bgp summary` as FRR prints
// it:
//
//	Neighbor     V    AS  MsgRcvd  MsgSent  TblVer  InQ  OutQ  Up/Down  State/PfxRcd  ...
//	203.0.113.1  4 65000       12       12       0    0     0 00:05:03  Established   ...
//	203.0.113.2  4 65000       12       12       0    0     0    never  Idle          ...
func ParseBGPSummary(text string) []BGPObservation {
	var out []BGPObservation
	for line := range strings.SplitSeq(text, "\n") {
		m := bgpRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out = append(out, BGPObservation{PeerAddress: m[1], Session: bgpState(m[3]), Uptime: strings.TrimSpace(m[2])})
	}
	return out
}

// bgpRowRe matches a neighbor row by the shape of the whole row, so the header
// does not match. The last column is a session state, or, for an established
// session in plain FRR output, the number of prefixes received.
var bgpRowRe = regexp.MustCompile(
	`^([\d.]+)\s+\d+\s+\d+\s+\d+\s+\d+\s+\d+\s+\d+\s+\d+\s+(\S+)\s+([A-Za-z][A-Za-z0-9/]*|\d+)`,
)

func bgpState(raw string) BGPState {
	state, _, _ := strings.Cut(raw, "/")
	if isAllDigits(state) {
		return BGPEstablished
	}
	switch strings.ToLower(state) {
	case "established":
		return BGPEstablished
	case "openconfirm":
		return BGPOpenConfirm
	case "opensent":
		return BGPOpenSent
	case "active":
		return BGPActive
	case "connect":
		return BGPConnect
	}
	return BGPIdle
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

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

package engine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"sync"

	seam "github.com/aenix-io/site-router-operator/api/engine"

	"github.com/aenix-io/barerouter/engine/internal/appliance"
	"github.com/aenix-io/barerouter/engine/internal/configtree"
	"github.com/aenix-io/barerouter/engine/internal/render"
)

// revisionSchema names the shape of a render. A change to what the engine
// renders for the same state changes it, so the first Apply after an upgrade of
// the engine applies again even where the guest happens to compare equal.
const revisionSchema = "br1"

// defaultDevice is the device the tunnel runs over when the operator names no
// network card, or the guest knows none of those it names: the first and, for a
// gateway with one card, the only ethernet device.
const defaultDevice = "eth0"

// session talks to one gateway. Every error and every string it returns is
// redacted of every secret the engine knows: the credentials, the pre-shared
// keys it was given, and the secrets it has read from a gateway's
// configuration. A secret the engine has never seen cannot be known to be one;
// within one pass of the operator, which applies before anything else, every
// pre-shared key has been seen before anything can quote it.
type session struct {
	engine      *Engine
	client      *appliance.Client
	redact      redactor
	revisionKey []byte

	mu sync.Mutex
	// applied is what the last state this session applied lets the source filter
	// admit, which Confirm holds the live filter to.
	applied *render.Allowed
}

// Apply implements seam.Session. It reads the gateway first and applies when
// the revision of d is not prevRevision or when the gateway does not hold the
// render of d, so a guest that drifted, by a hand or by a reboot onto its seed,
// is put right whatever the operator remembers.
func (s *session) Apply(ctx context.Context, d seam.Desired, prevRevision string) (seam.Applied, error) {
	s.engine.secrets.add(d.Secrets()...)
	cfg, err := s.running(ctx)
	if err != nil {
		return seam.Applied{}, s.redact.err("apply: reading the configuration", err)
	}
	device, err := s.device(ctx, d.TunnelNICMACs)
	if err != nil {
		return seam.Applied{}, s.redact.err("apply: finding the device of the tunnel", err)
	}
	ops, err := render.Render(d, device)
	if err != nil {
		return seam.Applied{}, s.redact.err("apply", err)
	}
	allowed, err := render.AllowedBy(d.Guards.TunnelIngress)
	if err != nil {
		return seam.Applied{}, s.redact.err("apply", err)
	}
	revision, err := s.revision(ops)
	if err != nil {
		return seam.Applied{}, s.redact.err("apply", err)
	}
	changed := revision != prevRevision || !render.InSync(cfg, ops, device)
	if changed {
		if err := s.client.Configure(ctx, ops); err != nil {
			return seam.Applied{}, s.redact.err("apply", err)
		}
	}
	s.mu.Lock()
	s.applied = &allowed
	s.mu.Unlock()
	return seam.Applied{Revision: revision, Changed: changed}, nil
}

// Confirm implements seam.Session. It reads the source filter off the running
// configuration of the gateway, every time. After an Apply in this session the
// filter is also held to what that state allows; before one, only its structure
// is judged. A configuration that cannot be read is an error and the filter is
// Unknown: it is never vouched for.
func (s *session) Confirm(ctx context.Context) (seam.GuardReport, error) {
	cfg, err := s.running(ctx)
	if err != nil {
		return seam.GuardReport{TunnelIngress: seam.GuardUnknown}, s.redact.err("confirm: reading the configuration", err)
	}
	s.mu.Lock()
	allowed := s.applied
	s.mu.Unlock()
	if active, detail := render.SourceFilter(cfg, allowed); !active {
		return seam.GuardReport{TunnelIngress: seam.GuardDown, Detail: s.redact.text(detail)}, nil
	}
	return seam.GuardReport{TunnelIngress: seam.GuardActive}, nil
}

// Observe implements seam.Session. It reports every peer the gateway is
// configured with, by the tunnel ID it is named after, and every configured BGP
// neighbor: a tunnel or a session that is not in the show output is down.
func (s *session) Observe(ctx context.Context) (seam.Observed, error) {
	cfg, err := s.running(ctx)
	if err != nil {
		return seam.Observed{}, s.redact.err("observe: reading the configuration", err)
	}
	out := seam.Observed{Tunnels: map[string]seam.TunnelState{}, BGPEstablished: map[string]bool{}}

	peers, _ := configtree.Node(cfg, "vpn", "ipsec", "site-to-site", "peer")
	if len(peers) > 0 {
		names := slices.Sorted(maps.Keys(peers))
		for _, p := range names {
			out.Tunnels[p] = seam.TunnelDown
		}
		sas, err := s.client.ShowVPNIPSecSA(ctx)
		if err != nil {
			return seam.Observed{}, s.redact.err("observe: reading the security associations", err)
		}
		for _, sa := range sas {
			if p, ok := peerOf(sa.Name, names); ok {
				out.Tunnels[p] = better(out.Tunnels[p], tunnelState(sa.State))
			}
		}
	}

	neighbors, _ := configtree.Node(cfg, "protocols", "bgp", "neighbor")
	if len(neighbors) > 0 {
		for n := range neighbors {
			out.BGPEstablished[n] = false
		}
		sessions, err := s.client.ShowBGPSummary(ctx)
		if err != nil {
			return seam.Observed{}, s.redact.err("observe: reading the BGP sessions", err)
		}
		for _, bs := range sessions {
			if _, ok := neighbors[bs.PeerAddress]; ok {
				out.BGPEstablished[bs.PeerAddress] = bs.Session == appliance.BGPEstablished
			}
		}
	}
	return out, nil
}

// running reads the whole running configuration and learns the secrets in it,
// before anything read from it can reach an error.
func (s *session) running(ctx context.Context) (configtree.Tree, error) {
	raw, err := s.client.Retrieve(ctx, nil)
	if err != nil {
		return nil, err
	}
	cfg, err := configtree.Decode(raw)
	if err != nil {
		return nil, err
	}
	s.engine.secrets.add(secretsIn(cfg)...)
	return cfg, nil
}

// secretPaths are where the configuration of a gateway holds secrets, "*"
// standing for any one name.
var secretPaths = [][]string{
	{"vpn", "ipsec", "authentication", "psk", "*", "secret"},
	{"service", "https", "api", "keys", "id", "*", "key"},
	{"pki", "certificate", "*", "private", "key"},
	{"protocols", "bgp", "neighbor", "*", "password"},
	{"system", "login", "user", "*", "authentication", "plaintext-password"},
	{"system", "login", "user", "*", "authentication", "encrypted-password"},
}

func secretsIn(cfg configtree.Tree) []string {
	var out []string
	var walk func(v any, path []string)
	walk = func(v any, path []string) {
		if len(path) == 0 {
			for _, l := range configtree.Leaves(v, nil) {
				if l.Value != "*" {
					out = append(out, l.Value)
				}
			}
			return
		}
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		if path[0] != "*" {
			walk(m[path[0]], path[1:])
			return
		}
		for _, child := range m {
			walk(child, path[1:])
		}
	}
	for _, p := range secretPaths {
		walk(cfg, p)
	}
	return out
}

// device finds the ethernet device whose MAC address is first among macs. It
// is an error when the guest cannot be asked, rather than a guess that would
// put the MSS clamp on another card.
func (s *session) device(ctx context.Context, macs []string) (string, error) {
	if len(macs) == 0 {
		return defaultDevice, nil
	}
	devices, err := s.client.ShowInterfacesDetail(ctx)
	if err != nil {
		return "", err
	}
	byMAC := map[string]string{}
	for _, d := range devices {
		byMAC[d.MAC] = d.Device
	}
	for _, mac := range macs {
		if dev, ok := byMAC[strings.ToLower(mac)]; ok {
			return dev, nil
		}
	}
	return defaultDevice, nil
}

// revision is a keyed hash of the render: the render holds the pre-shared keys,
// and a plain hash of what is otherwise known would let a weak key be guessed
// from the revision. The key is the gateway's API key, which nobody who sees a
// revision is meant to have.
func (s *session) revision(ops []appliance.Op) (string, error) {
	payload, err := json.Marshal(ops)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.revisionKey)
	mac.Write([]byte(revisionSchema))
	mac.Write(payload)
	return revisionSchema + "-" + hex.EncodeToString(mac.Sum(nil)[:16]), nil
}

// peerOf returns the peer a connection in the show output belongs to: the peer
// itself, or one of its tunnels, which the appliance names <peer>-tunnel-<n>.
// The peer names are the tunnel IDs, which Render keeps from ending like that.
func peerOf(name string, peers []string) (string, bool) {
	if slices.Contains(peers, name) {
		return name, true
	}
	for _, p := range peers {
		if n, ok := strings.CutPrefix(name, p+"-tunnel-"); ok && n != "" && strings.Trim(n, "0123456789") == "" {
			return p, true
		}
	}
	return "", false
}

func tunnelState(s appliance.IPSecState) seam.TunnelState {
	switch s {
	case appliance.IPSecUp:
		return seam.TunnelUp
	case appliance.IPSecConnecting:
		return seam.TunnelConnecting
	}
	return seam.TunnelDown
}

// better is the state of a peer from two of its connections: up when one is.
func better(a, b seam.TunnelState) seam.TunnelState {
	rank := map[seam.TunnelState]int{seam.TunnelDown: 0, seam.TunnelConnecting: 1, seam.TunnelUp: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

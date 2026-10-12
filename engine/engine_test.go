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

package engine_test

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	seam "github.com/aenix-io/site-router-operator/api/engine"
	"github.com/aenix-io/site-router-operator/api/engine/enginetest"

	"github.com/aenix-io/barerouter/engine"
	"github.com/aenix-io/barerouter/engine/internal/configtree"
	"github.com/aenix-io/barerouter/engine/internal/fakeappliance"
)

const managementNetwork = "10.244.0.0/16"

// rig is the engine and a simulated gateway booted on the seed it rendered.
type rig struct {
	eng  seam.Engine
	plan seam.BootstrapPlan
	dev  *fakeappliance.Appliance
	ep   seam.Endpoint
}

// boot gets the engine from the registry, as the operator does, bootstraps a
// gateway of version, and boots the simulated gateway on the seed.
func boot(t *testing.T, version string, opts ...fakeappliance.Option) rig {
	t.Helper()
	eng, err := seam.New(engine.Name)
	if err != nil {
		t.Fatal(err)
	}
	return bootWith(t, eng, version, opts...)
}

func bootWith(t *testing.T, eng seam.Engine, version string, opts ...fakeappliance.Option) rig {
	t.Helper()
	plan, err := eng.Bootstrap(seam.BootstrapRequest{Instance: "gw", Namespace: "tenant", ManagementAllow: managementNetwork, Version: version})
	if err != nil {
		t.Fatal(err)
	}
	dev := fakeappliance.Boot(t, plan.SeedFiles[seam.SeedUserData], opts...)
	return rig{eng: eng, plan: plan, dev: dev, ep: seam.Endpoint{Address: dev.Addr(), Credentials: plan.Credentials}}
}

func (r rig) connect(t *testing.T) seam.Session {
	t.Helper()
	s, err := r.eng.Connect(r.ep)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func latest(t *testing.T) string {
	t.Helper()
	return engine.New().Info().Versions[0]
}

// desired is what the operator asks of a gateway with one tunnel.
func desired() seam.Desired {
	return seam.Desired{
		Tunnels: []seam.Tunnel{{
			ID:              "tunnel-0",
			PeerAddress:     "203.0.113.10",
			PSK:             "a-pre-shared-key-of-the-tenant",
			LocalID:         "198.51.100.1",
			LocalSelectors:  []string{"0.0.0.0/0"},
			RemoteSelectors: []string{"192.0.2.0/24"},
		}},
		Guards: seam.Guards{
			ManagementAllow: managementNetwork,
			TunnelIngress:   seam.TunnelIngress{Sources: []string{"192.0.2.0/24"}, Destinations: []string{"10.244.1.5/32"}},
		},
	}
}

func apply(t *testing.T, s seam.Session, d seam.Desired, prev string) seam.Applied {
	t.Helper()
	a, err := s.Apply(t.Context(), d, prev)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func confirm(t *testing.T, s seam.Session) seam.GuardReport {
	t.Helper()
	rep, err := s.Confirm(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// The shared conformance suite, against the real engine and a gateway booted on
// the seed of every version the engine declares.
func TestConformance(t *testing.T) {
	for _, version := range engine.New().Info().Versions {
		t.Run(version, func(t *testing.T) {
			enginetest.Run(t, func(t *testing.T) enginetest.Rig {
				r := boot(t, version)
				return enginetest.Rig{Engine: r.eng, Endpoint: r.ep, Dial: r.dev.Dial, BreakFilter: r.dev.Reboot}
			})
		})
	}
}

func TestImportingThePackageRegistersTheEngine(t *testing.T) {
	t.Parallel()
	if !slices.Contains(seam.Names(), engine.Name) {
		t.Fatalf("registered engines %v do not include %q", seam.Names(), engine.Name)
	}
	info := engine.New().Info()
	if info.Name != engine.Name || info.ContractVersion != seam.ContractVersion {
		t.Errorf("Info() = %+v", info)
	}
	for _, v := range []string{"0.1.0", "0.1.1"} {
		if !slices.Contains(info.Versions, v) {
			t.Errorf("the released image %s is not among %v", v, info.Versions)
		}
	}
}

func TestBootstrapPlan(t *testing.T) {
	t.Parallel()
	r := boot(t, latest(t))
	p := r.plan
	if p.Arch != "amd64" || p.DiskGiB != 12 || p.ManagementPort != 443 {
		t.Errorf("plan %s, %d GiB, port %d", p.Arch, p.DiskGiB, p.ManagementPort)
	}
	if got := slices.Sorted(maps.Keys(p.Credentials)); !slices.Equal(got, []string{"api-key", "tls.crt", "tls.key"}) {
		t.Errorf("credentials %v", got)
	}
	if !regexp.MustCompile(`^[A-Z2-7]{26}$`).Match(p.Credentials["api-key"]) {
		t.Errorf("the API key is not the 26 base32 characters the engine mints: %d bytes", len(p.Credentials["api-key"]))
	}
	block, _ := pem.Decode(p.Credentials["tls.crt"])
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cert.DNSNames, []string{"gw.tenant.site-router.invalid"}) || cert.IsCA {
		t.Errorf("the certificate names %v (CA %v)", cert.DNSNames, cert.IsCA)
	}
	if got := slices.Sorted(maps.Keys(p.SeedFiles)); !slices.Equal(got, []string{seam.SeedUserData}) {
		t.Errorf("seed files %v", got)
	}
}

// Credentials are minted once; the ones the operator kept come back as they
// are, extra keys included, and render the same seed again.
func TestBootstrapKeepsWhatItIsGiven(t *testing.T) {
	t.Parallel()
	eng := engine.New()
	req := seam.BootstrapRequest{Instance: "gw", Namespace: "tenant", ManagementAllow: managementNetwork, Version: latest(t)}
	first, err := eng.Bootstrap(req)
	if err != nil {
		t.Fatal(err)
	}
	other, err := eng.Bootstrap(req)
	if err != nil {
		t.Fatal(err)
	}
	for k := range first.Credentials {
		if string(first.Credentials[k]) == string(other.Credentials[k]) {
			t.Errorf("two gateways were minted the same %s", k)
		}
	}
	req.Existing = maps.Clone(first.Credentials)
	req.Existing["kept"] = []byte("by the operator")
	again, err := eng.Bootstrap(req)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.EqualFunc(again.Credentials, req.Existing, func(a, b []byte) bool { return string(a) == string(b) }) {
		t.Error("the credentials given were not returned unchanged")
	}
	if string(again.SeedFiles[seam.SeedUserData]) != string(first.SeedFiles[seam.SeedUserData]) {
		t.Error("the same credentials rendered another seed")
	}
}

// Credentials that are incomplete or do not belong together are refused, not
// replaced: new ones would lock the operator out of a gateway that runs the old.
func TestBootstrapRefusesCredentialsItCannotKeep(t *testing.T) {
	t.Parallel()
	eng := engine.New()
	req := seam.BootstrapRequest{Instance: "gw", Namespace: "tenant", Version: latest(t)}
	a, _ := eng.Bootstrap(req)
	b, _ := eng.Bootstrap(req)
	tests := map[string]map[string][]byte{
		"only the key":          {"api-key": a.Credentials["api-key"]},
		"no certificate key":    {"api-key": a.Credentials["api-key"], "tls.crt": a.Credentials["tls.crt"]},
		"another gateway's key": {"api-key": a.Credentials["api-key"], "tls.crt": a.Credentials["tls.crt"], "tls.key": b.Credentials["tls.key"]},
		"a key with a quote":    {"api-key": []byte(`abcdefghijklmnop"`), "tls.crt": a.Credentials["tls.crt"], "tls.key": a.Credentials["tls.key"]},
	}
	for name, existing := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			req := req
			req.Existing = existing
			_, err := eng.Bootstrap(req)
			if err == nil {
				t.Fatal("bootstrapped")
			}
			for _, v := range existing {
				if strings.Contains(err.Error(), string(v)) {
					t.Errorf("the error quotes a credential: %v", err)
				}
			}
		})
	}
}

func TestBootstrapRefuses(t *testing.T) {
	t.Parallel()
	eng := engine.New()
	for name, req := range map[string]seam.BootstrapRequest{
		"no version":                 {Instance: "gw", Namespace: "tenant"},
		"an unknown version":         {Instance: "gw", Namespace: "tenant", Version: "9.9.9"},
		"an instance with uppercase": {Instance: "GW", Namespace: "tenant", Version: "0.1.1"},
		"no namespace":               {Instance: "gw", Version: "0.1.1"},
		"an open management network": {Instance: "gw", Namespace: "tenant", Version: "0.1.1", ManagementAllow: "0.0.0.0/0"},
		"a management network quote": {Instance: "gw", Namespace: "tenant", Version: "0.1.1", ManagementAllow: `10.0.0.0/8"`},
	} {
		if _, err := eng.Bootstrap(req); err == nil {
			t.Errorf("%s: bootstrapped", name)
		}
	}
}

func TestBootstrapSeedsEachVersionAndInstance(t *testing.T) {
	t.Parallel()
	eng := engine.New()
	seeds := map[string]string{}
	for _, v := range eng.Info().Versions {
		p, err := eng.Bootstrap(seam.BootstrapRequest{Instance: "branch.office", Namespace: "tenant", Version: v})
		if err != nil {
			t.Fatal(err)
		}
		seeds[v] = string(p.SeedFiles[seam.SeedUserData])
		cfg, err := configtree.ParseBoot(seeds[v])
		if err != nil {
			t.Fatal(err)
		}
		if got := configtree.Value(cfg, "system", "host-name"); got != "branch-office" {
			t.Errorf("%s: host name %q", v, got)
		}
		if !strings.HasSuffix(seeds[v], "// Release version: "+v+"\n") {
			t.Errorf("%s: the seed is not that version's", v)
		}
	}
	if seeds["0.1.0"] == seeds["0.1.1"] {
		t.Error("two versions got one seed")
	}
}

func TestConnectRefuses(t *testing.T) {
	t.Parallel()
	r := boot(t, latest(t))
	broken := maps.Clone(r.plan.Credentials)
	broken["tls.crt"] = []byte("not a certificate " + string(r.plan.Credentials["api-key"]))
	for name, ep := range map[string]seam.Endpoint{
		"no credentials":          {Address: r.ep.Address},
		"a broken certificate":    {Address: r.ep.Address, Credentials: broken},
		"an address with no port": {Address: "10.244.1.2", Credentials: r.plan.Credentials},
	} {
		_, err := r.eng.Connect(ep)
		if err == nil {
			t.Errorf("%s: connected", name)
			continue
		}
		for _, v := range ep.Credentials {
			if strings.Contains(err.Error(), string(v)) {
				t.Errorf("%s: the error quotes a credential: %v", name, err)
			}
		}
	}
}

// The engine talks to a gateway only when it presents the certificate in the
// credentials and takes the key in them: the credentials of another gateway
// reach nothing.
func TestTheCredentialsOfAnotherGatewayReachNothing(t *testing.T) {
	t.Parallel()
	a := boot(t, latest(t))
	b := bootWith(t, a.eng, latest(t))
	mixed := map[string]map[string][]byte{
		"another certificate": {"api-key": a.plan.Credentials["api-key"], "tls.crt": b.plan.Credentials["tls.crt"], "tls.key": b.plan.Credentials["tls.key"]},
		"another key":         {"api-key": b.plan.Credentials["api-key"], "tls.crt": a.plan.Credentials["tls.crt"], "tls.key": a.plan.Credentials["tls.key"]},
	}
	for name, creds := range mixed {
		s, err := a.eng.Connect(seam.Endpoint{Address: a.ep.Address, Credentials: creds})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Apply(t.Context(), desired(), ""); err == nil {
			t.Errorf("%s: applied", name)
		}
	}
	if a.dev.Configures() != 0 {
		t.Error("the gateway took a commit")
	}
}

func TestApplyCommitsOnlyWhatChanged(t *testing.T) {
	t.Parallel()
	r := boot(t, latest(t))
	s := r.connect(t)
	first := apply(t, s, desired(), "")
	if !first.Changed || r.dev.Configures() != 1 {
		t.Fatalf("first Apply = %+v after %d commits", first, r.dev.Configures())
	}
	if again := apply(t, s, desired(), first.Revision); again.Changed || r.dev.Configures() != 1 {
		t.Errorf("an Apply of the same state = %+v and committed", again)
	}
	// An operator that restarted remembers no revision, and applies once more.
	if restart := apply(t, r.connect(t), desired(), ""); !restart.Changed || restart.Revision != first.Revision {
		t.Errorf("an Apply with no revision = %+v", restart)
	}
	// A change where the engine does not look is not drift.
	r.dev.Change(func(c configtree.Tree) { _ = configtree.Set(c, []string{"system", "domain-name"}, "example.org") })
	if again := apply(t, s, desired(), first.Revision); again.Changed {
		t.Error("a change outside the managed configuration was taken for drift")
	}
}

// Whatever is changed by hand in what the engine owns, Apply puts back, and the
// filter it puts back is the one Confirm vouches for.
func TestApplyHealsAHandEdit(t *testing.T) {
	t.Parallel()
	edits := map[string]func(configtree.Tree){
		"an accept before the jump": func(c configtree.Tree) {
			_ = configtree.Set(c, []string{"firewall", "ipv4", "forward", "filter", "rule", "1", "action"}, "accept")
		},
		"a rogue peer": func(c configtree.Tree) {
			_ = configtree.Set(c, []string{"vpn", "ipsec", "site-to-site", "peer", "rogue", "remote-address"}, "192.0.2.66")
		},
		"the IPv6 input filter gone": func(c configtree.Tree) {
			configtree.Delete(c, []string{"firewall", "ipv6", "input", "filter"})
		},
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := boot(t, latest(t))
			s := r.connect(t)
			first := apply(t, s, desired(), "")
			r.dev.Change(edit)
			if healed := apply(t, s, desired(), first.Revision); !healed.Changed {
				t.Fatal("the edit was not noticed")
			}
			if rep := confirm(t, s); rep.TunnelIngress != seam.GuardActive {
				t.Errorf("after healing: %+v", rep)
			}
			if again := apply(t, s, desired(), first.Revision); again.Changed {
				t.Error("still not in sync after healing")
			}
		})
	}
}

func TestApplyFindsTheDeviceOfTheTunnel(t *testing.T) {
	t.Parallel()
	nics := fakeappliance.WithNICs(
		fakeappliance.NIC{Device: "eth0", MAC: "02:00:00:00:00:01"},
		fakeappliance.NIC{Device: "eth1", MAC: "02:00:00:00:00:02"},
	)
	clamp := func(c configtree.Tree, dev string) string {
		return configtree.Value(c, "interfaces", "ethernet", dev, "ip", "adjust-mss")
	}
	for name, tt := range map[string]struct {
		macs []string
		want string
	}{
		"a MAC the guest knows, in another case": {[]string{"02:00:00:00:00:02"}, "eth1"},
		"the first MAC the guest knows":          {[]string{"02:ff:00:00:00:09", "02:00:00:00:00:02"}, "eth1"},
		"no MAC the guest knows":                 {[]string{"02:ff:00:00:00:09"}, "eth0"},
		"no MAC":                                 {nil, "eth0"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := boot(t, latest(t), nics)
			d := desired()
			d.TunnelNICMACs = tt.macs
			if name == "a MAC the guest knows, in another case" {
				d.TunnelNICMACs = []string{strings.ToUpper(tt.macs[0])}
			}
			apply(t, r.connect(t), d, "")
			if clamp(r.dev.Config(), tt.want) != "1280" {
				t.Errorf("the clamp is not on %s: %v", tt.want, r.dev.Config()["interfaces"])
			}
		})
	}

	r := boot(t, latest(t), nics)
	r.dev.Break("/show", http.StatusInternalServerError, "down")
	d := desired()
	d.TunnelNICMACs = []string{"02:00:00:00:00:02"}
	if _, err := r.connect(t).Apply(t.Context(), d, ""); err == nil {
		t.Error("applied without knowing the device, which would be a guess")
	}
}

// Before the first Apply the gateway runs its seed, whose filter the engine does
// not vouch for; a configuration it cannot read is Unknown, never Active.
func TestConfirmBeforeApplyAndWhenBlind(t *testing.T) {
	t.Parallel()
	r := boot(t, latest(t))
	if rep := confirm(t, r.connect(t)); rep.TunnelIngress != seam.GuardDown {
		t.Errorf("on the seed: %+v", rep)
	}
	apply(t, r.connect(t), desired(), "")
	r.dev.Break("/retrieve", http.StatusInternalServerError, "down")
	rep, err := r.connect(t).Confirm(t.Context())
	if err == nil || rep.TunnelIngress != seam.GuardUnknown {
		t.Errorf("unreadable: %+v, %v", rep, err)
	}
}

// After an Apply, Confirm holds the filter to the state applied: a filter that
// admits another destination is not the one applied, even if well formed. A
// session that applied nothing can judge only the structure.
func TestConfirmHoldsTheFilterToWhatWasApplied(t *testing.T) {
	t.Parallel()
	r := boot(t, latest(t))
	s := r.connect(t)
	apply(t, s, desired(), "")
	r.dev.Change(func(c configtree.Tree) {
		dst := []string{"firewall", "ipv4", "name", "TUNNEL-INGRESS", "rule", "10", "destination", "address"}
		configtree.Delete(c, dst)
		_ = configtree.Set(c, dst, "10.244.9.9/32")
	})
	if rep := confirm(t, s); rep.TunnelIngress != seam.GuardDown {
		t.Errorf("held to the applied state: %+v", rep)
	}
	if rep := confirm(t, r.connect(t)); rep.TunnelIngress != seam.GuardActive {
		t.Errorf("structure only: %+v", rep)
	}
}

func TestObserve(t *testing.T) {
	t.Parallel()
	r := boot(t, latest(t))
	s := r.connect(t)
	if obs, err := s.Observe(t.Context()); err != nil || len(obs.Tunnels) != 0 || len(obs.BGPEstablished) != 0 {
		t.Fatalf("on the seed: %+v, %v", obs, err)
	}
	d := desired()
	branch := d.Tunnels[0]
	branch.ID, branch.PeerAddress = "branch", "203.0.113.20"
	branch.RemoteSelectors = []string{"192.0.2.0/24", "198.51.100.0/24"}
	d.Tunnels = append(d.Tunnels, branch)
	d.BGP = &seam.BGP{LocalASN: 65001, Neighbors: []seam.Neighbor{{Address: "10.244.1.10", RemoteASN: 65002}, {Address: "10.244.1.11", RemoteASN: 65002}}}
	apply(t, s, d, "")
	r.dev.Dial("branch")
	r.dev.EstablishBGP("10.244.1.11")
	obs, err := s.Observe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantTunnels := map[string]seam.TunnelState{"tunnel-0": seam.TunnelDown, "branch": seam.TunnelUp}
	if !maps.Equal(obs.Tunnels, wantTunnels) {
		t.Errorf("tunnels %v, want %v", obs.Tunnels, wantTunnels)
	}
	wantBGP := map[string]bool{"10.244.1.10": false, "10.244.1.11": true}
	if !maps.Equal(obs.BGPEstablished, wantBGP) {
		t.Errorf("BGP %v, want %v", obs.BGPEstablished, wantBGP)
	}

	r.dev.Break("/show", http.StatusInternalServerError, "down")
	if _, err := s.Observe(t.Context()); err == nil {
		t.Error("observed without the show output")
	}
}

func TestRevision(t *testing.T) {
	t.Parallel()
	a := boot(t, latest(t))
	b := bootWith(t, a.eng, latest(t))
	revA := apply(t, a.connect(t), desired(), "").Revision
	if again := apply(t, a.connect(t), desired(), "").Revision; again != revA {
		t.Error("the same state in another session has another revision")
	}
	other := desired()
	other.Tunnels[0].PSK = "another-pre-shared-key"
	if rev := apply(t, a.connect(t), other, revA).Revision; rev == revA {
		t.Error("another key has the same revision")
	}
	// The revision is keyed by the gateway's API key, so it cannot be computed,
	// and a weak pre-shared key guessed from it, by whoever lacks that key.
	if revB := apply(t, b.connect(t), desired(), "").Revision; revB == revA {
		t.Error("two gateways with other API keys have one revision for one state")
	}
	if strings.Contains(revA, desired().Tunnels[0].PSK) {
		t.Error("the revision carries the key")
	}
}

func TestACancelledContextIsStillACancelledContext(t *testing.T) {
	t.Parallel()
	r := boot(t, latest(t))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.connect(t).Apply(ctx, desired(), ""); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}

// An engine that was never given a gateway's pre-shared key, such as the one of
// an operator that has just started, learns it from the configuration it reads
// before an error can quote it.
func TestAnEngineRedactsWhatItReadsBeforeItIsTold(t *testing.T) {
	t.Parallel()
	r := boot(t, latest(t))
	apply(t, r.connect(t), desired(), "")
	psk := desired().Tunnels[0].PSK
	r.dev.Break("/show", http.StatusBadRequest, refusal("show failed for the peer with key "+psk))

	fresh := engine.New()
	s, err := fresh.Connect(r.ep)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Observe(t.Context())
	if err == nil {
		t.Fatal("observed without the show output")
	}
	if strings.Contains(err.Error(), psk) {
		t.Errorf("the key leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "show failed for the peer with key ***") {
		t.Errorf("the gateway's reason did not come back, redacted: %v", err)
	}
}

// secrets are the values of r and d that must not leave the engine, in every
// form they can take: the key of the certificate is also in the gateway's
// configuration as one line of base64.
func secrets(t *testing.T, r rig, d seam.Desired) map[string]string {
	t.Helper()
	block, _ := pem.Decode(r.plan.Credentials["tls.key"])
	return map[string]string{
		"the pre-shared key":                d.Tunnels[0].PSK,
		"the API key":                       string(r.plan.Credentials["api-key"]),
		"the certificate key":               string(r.plan.Credentials["tls.key"]),
		"the certificate key in the config": base64.StdEncoding.EncodeToString(block.Bytes),
	}
}

func refusal(message string) string {
	b, _ := json.Marshal(map[string]any{"success": false, "data": nil, "error": message})
	return string(b)
}

// No secret leaves the engine, on any call and in any way the gateway can quote
// it: in a refused commit, which quotes what it was sent; in a refusal of a read
// that quotes the configuration; in an answer that is not the API's at all; and
// in a session that never applied, for a key another session of the engine
// applied. The operator redacts what it returns as well; this is the engine
// keeping its own.
func TestNoSecretLeavesTheEngine(t *testing.T) {
	t.Parallel()
	type call func(s seam.Session) (string, error)
	applyCall := func(s seam.Session) (string, error) {
		_, err := s.Apply(t.Context(), desired(), "")
		return "", err
	}
	confirmCall := func(s seam.Session) (string, error) {
		rep, err := s.Confirm(t.Context())
		return rep.Detail, err
	}
	observeCall := func(s seam.Session) (string, error) {
		_, err := s.Observe(t.Context())
		return "", err
	}
	// The secrets go first: a message is cut at a length after it is redacted,
	// and a secret past the cut would pass for one that was redacted.
	quoteEverything := func(r rig) string {
		raw, _ := json.Marshal(r.dev.Config())
		all := make([]string, 0, 4)
		for _, v := range secrets(t, r, desired()) {
			all = append(all, v)
		}
		slices.Sort(all)
		return "Commit failed: " + strings.Join(all, " ") + " " + string(raw)
	}
	tests := map[string]struct {
		setup func(r rig)
		calls map[string]call
		// quoted is true when what comes back quotes a secret, which then has to
		// come back redacted rather than not at all.
		quoted bool
	}{
		"a refused commit": {
			setup:  func(r rig) { r.dev.RefuseCommits("secret") },
			calls:  map[string]call{"Apply": applyCall},
			quoted: true,
		},
		"a refused read that quotes everything": {
			setup:  func(r rig) { r.dev.Break("/retrieve", http.StatusBadRequest, refusal(quoteEverything(r))) },
			calls:  map[string]call{"Apply": applyCall, "Confirm": confirmCall, "Observe": observeCall},
			quoted: true,
		},
		"a refused show that quotes everything": {
			setup:  func(r rig) { r.dev.Break("/show", http.StatusBadRequest, refusal(quoteEverything(r))) },
			calls:  map[string]call{"Observe": observeCall},
			quoted: true,
		},
		"an answer that is not the API's": {
			setup: func(r rig) { r.dev.Break("/retrieve", http.StatusInternalServerError, quoteEverything(r)) },
			calls: map[string]call{"Apply": applyCall, "Confirm": confirmCall, "Observe": observeCall},
		},
		"a filter a hand broke": {
			setup: func(r rig) {
				r.dev.Change(func(c configtree.Tree) {
					configtree.Delete(c, []string{"firewall", "ipv4", "name", "TUNNEL-INGRESS", "rule", "10", "destination"})
				})
			},
			calls: map[string]call{"Confirm": confirmCall},
		},
	}
	for name, tt := range tests {
		for callName, c := range tt.calls {
			t.Run(name+"/"+callName, func(t *testing.T) {
				t.Parallel()
				r := boot(t, latest(t))
				// Another session applies, so the gateway runs the tunnel and
				// the engine has seen its key; the session under test is new.
				apply(t, r.connect(t), desired(), "")
				r.dev.Dial("tunnel-0")
				tt.setup(r)
				text, err := c(r.connect(t))
				if err != nil {
					text += " " + err.Error()
					if errors.Unwrap(err) != nil {
						t.Errorf("the error wraps %v", errors.Unwrap(err))
					}
				}
				if strings.TrimSpace(text) == "" {
					t.Fatal("nothing came back to check")
				}
				for what, secret := range secrets(t, r, desired()) {
					if strings.Contains(text, secret) {
						t.Errorf("%s leaked: %s", what, text)
					}
				}
				if tt.quoted && !strings.Contains(text, "***") {
					t.Errorf("a quoted secret did not come back redacted: %s", text)
				}
			})
		}
	}
}

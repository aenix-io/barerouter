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

// Package fakeappliance simulates a gateway for the tests of the engine: an
// HTTPS server that speaks the management API (docs/management-api.md) over a
// configuration tree.
//
// It boots from the seed the engine rendered, the way the image does, and takes
// its API key and the certificate it presents from that seed, not from the
// credentials the engine kept. A seed that does not give the gateway the key and
// the certificate the engine will use therefore fails the tests, as it would
// leave a real gateway that nobody can configure. Commits are checked against
// the rules of the appliance that the engine's paths were validated against,
// so a render the appliance would refuse is refused here too. It is not a model
// of the router: nothing it does forwards a packet.
package fakeappliance

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aenix-io/barerouter/engine/internal/configtree"
)

// NIC is a network card of the simulated gateway.
type NIC struct{ Device, MAC string }

// Appliance is a running simulated gateway.
type Appliance struct {
	mu      sync.Mutex
	boot    configtree.Tree
	running configtree.Tree
	keys    []string
	nics    []NIC
	dialed  map[string]bool
	bgpUp   map[string]bool
	broken  map[string]answer
	// refuseQuoting, when set, refuses every commit; see RefuseCommits.
	refuseQuoting string
	configures    int
	srv           *httptest.Server
}

type answer struct {
	status int
	body   string
}

// Option configures an Appliance.
type Option func(*Appliance)

// WithNICs sets the network cards; the default is one, eth0.
func WithNICs(nics ...NIC) Option { return func(a *Appliance) { a.nics = nics } }

// Boot starts a gateway on userdata, the seed as the image reads it.
func Boot(t testing.TB, userdata []byte, opts ...Option) *Appliance {
	t.Helper()
	boot, err := configtree.ParseBoot(string(userdata))
	if err != nil {
		t.Fatalf("the gateway cannot read its seed: %v", err)
	}
	a := &Appliance{
		boot:   boot,
		nics:   []NIC{{Device: "eth0", MAC: "02:00:00:00:00:01"}},
		dialed: map[string]bool{},
		bgpUp:  map[string]bool{},
		broken: map[string]answer{},
	}
	for _, opt := range opts {
		opt(a)
	}
	ids, _ := configtree.Node(boot, "service", "https", "api", "keys", "id")
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		a.keys = append(a.keys, configtree.Values(boot, "service", "https", "api", "keys", "id", id, "key")...)
	}
	if len(a.keys) == 0 {
		t.Fatal("the seed gives the management API no key: nobody can configure the gateway")
	}
	cert := presented(t, boot)
	a.running = configtree.Clone(boot)

	a.srv = httptest.NewUnstartedServer(http.HandlerFunc(a.serve))
	a.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	a.srv.StartTLS()
	t.Cleanup(a.srv.Close)
	return a
}

// presented returns the certificate the seed tells the management API to
// present, with its key.
func presented(t testing.TB, boot configtree.Tree) tls.Certificate {
	t.Helper()
	name := configtree.Value(boot, "service", "https", "certificates", "certificate")
	if name == "" {
		t.Fatal("the seed names no certificate for the management API")
	}
	der, err := base64.StdEncoding.DecodeString(configtree.Value(boot, "pki", "certificate", name, "certificate"))
	if err != nil || len(der) == 0 {
		t.Fatalf("the seed has no certificate %s", name)
	}
	pkcs8, err := base64.StdEncoding.DecodeString(configtree.Value(boot, "pki", "certificate", name, "private", "key"))
	if err != nil || len(pkcs8) == 0 {
		t.Fatalf("the seed has no key for certificate %s", name)
	}
	key, err := x509.ParsePKCS8PrivateKey(pkcs8)
	if err != nil {
		t.Fatalf("the key of certificate %s is not PKCS#8, the one form the appliance reads: %v", name, err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		t.Fatalf("the key of certificate %s cannot sign", name)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: signer}
}

// Addr is the host:port the management API listens on.
func (a *Appliance) Addr() string { return strings.TrimPrefix(a.srv.URL, "https://") }

// Reboot puts the gateway back on its seed, as a restart of the VM does: the
// API commits configuration but does not save it, and the image installs the
// seed again on every boot. Every security association is gone with it.
func (a *Appliance) Reboot() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = configtree.Clone(a.boot)
	a.dialed = map[string]bool{}
	a.bgpUp = map[string]bool{}
}

// Dial makes the remote end of the peer named peer bring its tunnels up.
func (a *Appliance) Dial(peer string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dialed[peer] = true
}

// EstablishBGP brings the session with a neighbor up.
func (a *Appliance) EstablishBGP(neighbor string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bgpUp[neighbor] = true
}

// Change edits the running configuration behind the engine's back, as someone
// at the console would.
func (a *Appliance) Change(edit func(configtree.Tree)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	edit(a.running)
}

// Config returns a copy of the running configuration.
func (a *Appliance) Config() configtree.Tree {
	a.mu.Lock()
	defer a.mu.Unlock()
	return configtree.Clone(a.running)
}

// Configures is the number of commits the gateway took.
func (a *Appliance) Configures() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.configures
}

// Break makes an endpoint, such as "/retrieve", answer every request with status
// and body. Heal undoes it.
func (a *Appliance) Break(endpoint string, status int, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.broken[endpoint] = answer{status: status, body: body}
}

// Heal makes every endpoint answer again.
func (a *Appliance) Heal() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.broken = map[string]answer{}
	a.refuseQuoting = ""
}

// RefuseCommits makes /configure refuse a commit the way the appliance refuses
// a value it cannot take: quoting the first operation whose path holds quoting,
// value and all.
func (a *Appliance) RefuseCommits(quoting string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refuseQuoting = quoting
}

func reply(w http.ResponseWriter, status int, data any, errText string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	env := map[string]any{"success": errText == "", "data": data, "error": nil}
	if errText != "" {
		env["error"] = errText
		env["data"] = nil
	}
	_ = json.NewEncoder(w).Encode(env)
}

func (a *Appliance) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, nil, "Method Not Allowed")
		return
	}
	if err := r.ParseForm(); err != nil {
		reply(w, http.StatusBadRequest, nil, "the request is not a form")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !slices.Contains(a.keys, r.PostForm.Get("key")) {
		reply(w, http.StatusUnauthorized, nil, "Valid API key is required")
		return
	}
	data := r.PostForm.Get("data")
	if broken, ok := a.broken[r.URL.Path]; ok {
		w.WriteHeader(broken.status)
		_, _ = w.Write([]byte(broken.body))
		return
	}
	switch r.URL.Path {
	case "/configure":
		a.configure(w, data)
	case "/retrieve":
		a.retrieve(w, data)
	case "/show":
		a.show(w, data)
	default:
		reply(w, http.StatusNotFound, nil, "Not Found")
	}
}

type op struct {
	Op    string   `json:"op"`
	Path  []string `json:"path"`
	Value string   `json:"value"`
}

func (a *Appliance) configure(w http.ResponseWriter, data string) {
	var ops []op
	if err := json.Unmarshal([]byte(data), &ops); err != nil {
		var one op
		if err := json.Unmarshal([]byte(data), &one); err != nil {
			reply(w, http.StatusBadRequest, nil, "the data is not an operation or a list of them")
			return
		}
		ops = []op{one}
	}
	if a.refuseQuoting != "" {
		for _, o := range ops {
			if slices.Contains(o.Path, a.refuseQuoting) {
				reply(w, http.StatusBadRequest, nil, fmt.Sprintf("Value validation failed\nSet failed: [%s %q]", strings.Join(o.Path, " "), o.Value))
				return
			}
		}
		reply(w, http.StatusBadRequest, nil, "Commit failed")
		return
	}
	next := configtree.Clone(a.running)
	for _, o := range ops {
		switch o.Op {
		case "set":
			if err := configtree.Set(next, o.Path, o.Value); err != nil {
				reply(w, http.StatusBadRequest, nil, err.Error())
				return
			}
		case "delete":
			configtree.Delete(next, o.Path)
		default:
			reply(w, http.StatusBadRequest, nil, fmt.Sprintf("%q is not a valid operation", o.Op))
			return
		}
	}
	if err := verify(next); err != nil {
		reply(w, http.StatusBadRequest, nil, "Commit failed: "+err.Error())
		return
	}
	a.running = next
	a.configures++
	reply(w, http.StatusOK, "", "")
}

type query struct {
	Op   string   `json:"op"`
	Path []string `json:"path"`
}

func (a *Appliance) retrieve(w http.ResponseWriter, data string) {
	var q query
	if err := json.Unmarshal([]byte(data), &q); err != nil || q.Op != "showConfig" {
		reply(w, http.StatusBadRequest, nil, "only showConfig is simulated")
		return
	}
	v, ok := configtree.Get(a.running, q.Path...)
	if !ok {
		reply(w, http.StatusBadRequest, nil, "Configuration under specified path is empty")
		return
	}
	reply(w, http.StatusOK, v, "")
}

func (a *Appliance) show(w http.ResponseWriter, data string) {
	var q query
	if err := json.Unmarshal([]byte(data), &q); err != nil || q.Op != "show" {
		reply(w, http.StatusBadRequest, nil, "the data is not a show query")
		return
	}
	switch strings.Join(q.Path, " ") {
	case "vpn ipsec sa":
		reply(w, http.StatusOK, a.ipsecSA(), "")
	case "interfaces detail":
		reply(w, http.StatusOK, a.interfacesDetail(), "")
	case "bgp summary":
		if _, ok := configtree.Get(a.running, "protocols", "bgp"); !ok {
			reply(w, http.StatusBadRequest, nil, "% BGP instance not found")
			return
		}
		reply(w, http.StatusOK, a.bgpSummary(), "")
	default:
		reply(w, http.StatusBadRequest, nil, "this show command is not simulated")
	}
}

// ipsecSA prints the table the image prints: a row per installed child SA,
// named after its peer and tunnel, and nothing for a peer that has not been
// dialled.
func (a *Appliance) ipsecSA() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-20s  %-7s  %-8s  %-14s  %-16s  %-16s  %-14s  %s\n",
		"Connection", "State", "Uptime", "Bytes In/Out", "Packets In/Out", "Remote address", "Remote ID", "Proposal")
	fmt.Fprintf(&b, "%s  %s  %s  %s  %s  %s  %s  %s\n", strings.Repeat("-", 20), strings.Repeat("-", 7), strings.Repeat("-", 8),
		strings.Repeat("-", 14), strings.Repeat("-", 16), strings.Repeat("-", 16), strings.Repeat("-", 14), strings.Repeat("-", 39))
	peers, _ := configtree.Node(a.running, "vpn", "ipsec", "site-to-site", "peer")
	for _, name := range slices.Sorted(maps.Keys(peers)) {
		if !a.dialed[name] {
			continue
		}
		remote := configtree.Value(peers, name, "remote-address")
		tunnels, _ := configtree.Node(peers, name, "tunnel")
		for _, n := range slices.Sorted(maps.Keys(tunnels)) {
			fmt.Fprintf(&b, "%-20s  %-7s  %-8s  %-14s  %-16s  %-16s  %-14s  %s\n",
				name+"-tunnel-"+n, "up", "1m2s", "0B/0B", "0/0", remote, remote, "AES_CBC_256/HMAC_SHA2_256_128/MODP_2048")
		}
	}
	return b.String()
}

func (a *Appliance) interfacesDetail() string {
	var b strings.Builder
	for i, nic := range a.nics {
		fmt.Fprintf(&b, "%d: %s: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc fq_codel state UP group default qlen 1000\n", i+2, nic.Device)
		fmt.Fprintf(&b, "    link/ether %s brd ff:ff:ff:ff:ff:ff\n", nic.MAC)
	}
	b.WriteString("1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 qdisc noqueue state UNKNOWN group default qlen 1000\n")
	b.WriteString("    link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00\n")
	return b.String()
}

func (a *Appliance) bgpSummary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "BGP router identifier 10.244.1.2, local AS number %s vrf-id 0\n\n", configtree.Value(a.running, "protocols", "bgp", "system-as"))
	b.WriteString("Neighbor        V         AS   MsgRcvd   MsgSent   TblVer  InQ OutQ  Up/Down State/PfxRcd   PfxSnt Desc\n")
	neighbors, _ := configtree.Node(a.running, "protocols", "bgp", "neighbor")
	for _, n := range slices.Sorted(maps.Keys(neighbors)) {
		state, uptime := "Active", "never"
		if a.bgpUp[n] {
			state, uptime = "Established", "00:01:02"
		}
		fmt.Fprintf(&b, "%-15s 4 %10s %9d %9d %8d %4d %4d %8s %-12s %8d\n",
			n, configtree.Value(neighbors, n, "remote-as"), 12, 12, 0, 0, 0, uptime, state, 0)
	}
	return b.String()
}

var peerNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// verify refuses what the appliance's commit refuses, for the rules the
// engine's paths were validated against on the image.
func verify(cfg configtree.Tree) error {
	peers, _ := configtree.Node(cfg, "vpn", "ipsec", "site-to-site", "peer")
	for _, name := range slices.Sorted(maps.Keys(peers)) {
		p := []string{"vpn", "ipsec", "site-to-site", "peer", name}
		switch {
		case !peerNameRe.MatchString(name):
			return fmt.Errorf("peer %s: Peer connection name must be alphanumeric", name)
		case !has(cfg, append(p, "local-address")...) && !has(cfg, append(p, "dhcp-interface")...):
			return fmt.Errorf("peer %s: Missing local-address or dhcp-interface on site-to-site peer", name)
		case !has(cfg, append(p, "tunnel")...) && !has(cfg, append(p, "vti")...):
			return fmt.Errorf("peer %s: No VTI or tunnel specified on site-to-site peer", name)
		case !has(cfg, "vpn", "ipsec", "ike-group", configtree.Value(cfg, append(p, "ike-group")...)):
			return fmt.Errorf("peer %s: the ike-group does not exist", name)
		case !has(cfg, "vpn", "ipsec", "esp-group", configtree.Value(cfg, append(p, "default-esp-group")...)):
			return fmt.Errorf("peer %s: the esp-group does not exist", name)
		}
	}
	chains := map[string][]string{"input filter": {"firewall", "ipv4", "input", "filter"}, "forward filter": {"firewall", "ipv4", "forward", "filter"}}
	names, _ := configtree.Node(cfg, "firewall", "ipv4", "name")
	for n := range names {
		chains["name "+n] = []string{"firewall", "ipv4", "name", n}
	}
	for _, chain := range slices.Sorted(maps.Keys(chains)) {
		rules, _ := configtree.Node(cfg, append(chains[chain], "rule")...)
		for _, n := range slices.Sorted(maps.Keys(rules)) {
			r, _ := configtree.Node(rules, n)
			if has(r, "destination", "port") && !slices.Contains([]string{"tcp", "udp", "tcp_udp"}, configtree.Value(r, "protocol")) {
				return fmt.Errorf("%s rule %s: Protocol must be defined if specifying a port", chain, n)
			}
			if configtree.Value(r, "action") == "jump" && !has(names, configtree.Value(r, "jump-target")) {
				return fmt.Errorf("%s rule %s: Invalid jump-target", chain, n)
			}
		}
	}
	return nil
}

func has(t configtree.Tree, path ...string) bool {
	_, ok := configtree.Get(t, path...)
	return ok && !slices.Contains(path, "")
}

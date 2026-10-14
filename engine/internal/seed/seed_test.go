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

package seed_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"flag"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aenix-io/barerouter/engine/internal/configtree"
	"github.com/aenix-io/barerouter/engine/internal/render"
	"github.com/aenix-io/barerouter/engine/internal/seed"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const testAPIKey = "GOLDENAPIKEY0123456789"

// credentials are a fixed key and certificate, so the goldens do not change from
// run to run. Ed25519 signs deterministically, so the certificate is the same
// bytes every time; no key file is kept in the tree.
func credentials(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "golden.ns.barerouter.invalid"},
		DNSNames:              []string{"golden.ns.barerouter.invalid"},
		NotBefore:             time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(bytes.NewReader(make([]byte, 1024)), tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
}

func input(t *testing.T) seed.Input {
	t.Helper()
	cert, key := credentials(t)
	return seed.Input{HostName: "golden", ManagementAllow: "10.244.0.0/16", APIKey: testAPIKey, Certificate: cert, PrivateKey: key}
}

func TestVersions(t *testing.T) {
	t.Parallel()
	if got := seed.Versions(); !slices.Equal(got, []string{"0.1.1", "0.1.0"}) {
		t.Errorf("Versions() = %v, want the released images 0.1.1 and 0.1.0", got)
	}
}

func TestSeedGolden(t *testing.T) {
	t.Parallel()
	for _, version := range seed.Versions() {
		for name, mutate := range map[string]func(*seed.Input){
			"management":    func(*seed.Input) {},
			"no-management": func(in *seed.Input) { in.ManagementAllow = "" },
		} {
			t.Run(version+"/"+name, func(t *testing.T) {
				t.Parallel()
				in := input(t)
				mutate(&in)
				got, err := seed.Render(version, in)
				if err != nil {
					t.Fatal(err)
				}
				file := filepath.Join("testdata", version+"-"+name+".config.boot")
				if *update {
					if err := os.WriteFile(file, got, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(file)
				if err != nil {
					t.Fatalf("%v (run the tests with -update to write it)", err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("the seed differs from %s; run the tests with -update and review the diff\ngot:\n%s", file, got)
				}
			})
		}
	}
}

// The seed is read as the router reads it, and has to give the router what the
// engine needs to reach it and nothing the engine has not vouched for.
func TestSeedIsWhatTheEngineNeeds(t *testing.T) {
	t.Parallel()
	for _, version := range seed.Versions() {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			in := input(t)
			raw, err := seed.Render(version, in)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := configtree.ParseBoot(string(raw))
			if err != nil {
				t.Fatalf("the router could not read its seed: %v", err)
			}

			if got := configtree.Values(cfg, "service", "https", "api", "keys", "id", "site-router-controller", "key"); !slices.Equal(got, []string{testAPIKey}) {
				t.Errorf("API key %q, want the credential", got)
			}
			cert, _ := pem.Decode(in.Certificate)
			key, _ := pem.Decode(in.PrivateKey)
			pki := []string{"pki", "certificate", "site-router-api"}
			if got := configtree.Value(cfg, append(pki, "certificate")...); got != base64.StdEncoding.EncodeToString(cert.Bytes) {
				t.Error("the certificate the API presents is not the credential")
			}
			if got := configtree.Value(cfg, append(pki, "private", "key")...); got != base64.StdEncoding.EncodeToString(key.Bytes) {
				t.Error("the key of the API's certificate is not the credential's")
			}
			if got := configtree.Value(cfg, "service", "https", "certificates", "certificate"); got != "site-router-api" {
				t.Errorf("the API presents %q", got)
			}

			// Closed from the first boot: nothing reaches the router but return
			// traffic and the management network, nothing decrypted reaches its
			// API, and nothing is forwarded until the engine sets up the source
			// filter, which is not there yet.
			input := []string{"firewall", "ipv4", "input", "filter"}
			if configtree.Value(cfg, append(input, "default-action")...) != "drop" ||
				configtree.Value(cfg, append(input, "rule", "1", "action")...) != "drop" ||
				configtree.Value(cfg, append(input, "rule", "10", "source", "address")...) != in.ManagementAllow {
				t.Errorf("the input filter is not closed: %v", configtree.Leaves(cfg["firewall"], []string{"firewall"}))
			}
			if active, _ := render.SourceFilter(cfg, nil); active {
				t.Error("the seed claims a source filter the engine has not applied")
			}
			if configtree.Value(cfg, "firewall", "ipv4", "forward", "filter", "default-action") != "drop" {
				t.Error("the seed forwards by default")
			}

			users, _ := configtree.Node(cfg, "system", "login", "user")
			for name := range users {
				if got := configtree.Value(cfg, "system", "login", "user", name, "authentication", "encrypted-password"); got != "*" {
					t.Errorf("user %s can log in (%q)", name, got)
				}
			}
			if len(users) != 1 {
				t.Errorf("users %v, want the image's own login alone", users)
			}
			if _, ok := configtree.Get(cfg, "service", "ssh"); ok {
				t.Error("the seed starts SSH")
			}
			if !strings.HasSuffix(string(raw), "// Release version: "+version+"\n") {
				t.Errorf("the footer does not name %s", version)
			}
		})
	}
}

// A chain that is not configured filters nothing, and the guest has an IPv6
// link-local address whatever the cluster runs, so every chain of both families
// drops by default. Over IPv6 only return traffic and ICMPv6, which neighbour
// discovery on the link needs, reach the router, and nothing is forwarded.
func TestEveryChainDropsByDefault(t *testing.T) {
	t.Parallel()
	for _, version := range seed.Versions() {
		for _, mgmt := range []string{"10.244.0.0/16", ""} {
			in := input(t)
			in.ManagementAllow = mgmt
			raw, err := seed.Render(version, in)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := configtree.ParseBoot(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			for _, family := range []string{"ipv4", "ipv6"} {
				for _, chain := range []string{"forward", "input"} {
					if got := configtree.Value(cfg, "firewall", family, chain, "filter", "default-action"); got != "drop" {
						t.Errorf("%s, management %q: %s %s default-action = %q, want drop", version, mgmt, family, chain, got)
					}
				}
			}
			if _, ok := configtree.Get(cfg, "firewall", "ipv6", "forward", "filter", "rule"); ok {
				t.Errorf("%s: the IPv6 forward filter has rules", version)
			}
			rules, _ := configtree.Node(cfg, "firewall", "ipv6", "input", "filter", "rule")
			if len(rules) != 2 ||
				!slices.Equal(configtree.Values(rules, "5", "state"), []string{"established", "related"}) ||
				configtree.Value(rules, "10", "protocol") != "ipv6-icmp" {
				t.Errorf("%s: the IPv6 input filter admits %v, want return traffic and ICMPv6", version, configtree.Leaves(rules, nil))
			}
			if _, ok := configtree.Get(cfg, "service", "ntp", "allow-client"); ok {
				t.Errorf("%s: the gateway serves time", version)
			}
		}
	}
}

// The image renamed its default login in 0.1.1; the seed keeps each image's own.
func TestEachVersionLocksItsOwnLogin(t *testing.T) {
	t.Parallel()
	for version, login := range map[string]string{"0.1.0": "vyos", "0.1.1": "admin"} {
		raw, err := seed.Render(version, input(t))
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := configtree.ParseBoot(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := configtree.Node(cfg, "system", "login", "user", login); !ok {
			t.Errorf("%s: the seed does not lock %s", version, login)
		}
	}
}

func TestNoManagementNetworkClosesTheAPI(t *testing.T) {
	t.Parallel()
	in := input(t)
	in.ManagementAllow = ""
	raw, err := seed.Render("0.1.1", in)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := configtree.ParseBoot(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, open := configtree.Get(cfg, "firewall", "ipv4", "input", "filter", "rule", "10"); open {
		t.Error("with no management network the API is open")
	}
	if configtree.Value(cfg, "firewall", "ipv4", "input", "filter", "default-action") != "drop" {
		t.Error("with no management network the input filter is gone")
	}
}

func TestSeedRefuses(t *testing.T) {
	t.Parallel()
	cert, key := credentials(t)
	tests := map[string]func(*seed.Input){
		"a host name with a quote":              func(in *seed.Input) { in.HostName = `gw" {` },
		"a host name with a dot":                func(in *seed.Input) { in.HostName = "gw.ns" },
		"an empty host name":                    func(in *seed.Input) { in.HostName = "" },
		"a host name of one character":          func(in *seed.Input) { in.HostName = "a" },
		"a management network with a newline":   func(in *seed.Input) { in.ManagementAllow = "10.0.0.0/8\n}" },
		"a management network that is any":      func(in *seed.Input) { in.ManagementAllow = "0.0.0.0/0" },
		"a management network with host bits":   func(in *seed.Input) { in.ManagementAllow = "10.244.0.1/16" },
		"an IPv6 management network":            func(in *seed.Input) { in.ManagementAllow = "fd00::/8" },
		"an API key with a quote":               func(in *seed.Input) { in.APIKey = `abcdefghijklmnop"` },
		"a short API key":                       func(in *seed.Input) { in.APIKey = "short" },
		"a key where the certificate should be": func(in *seed.Input) { in.Certificate = key },
		"a certificate where the key should be": func(in *seed.Input) { in.PrivateKey = cert },
		"two certificates":                      func(in *seed.Input) { in.Certificate = append(slices.Clone(cert), cert...) },
		"no key":                                func(in *seed.Input) { in.PrivateKey = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := input(t)
			mutate(&in)
			if _, err := seed.Render("0.1.1", in); err == nil {
				t.Error("rendered")
			}
		})
	}
	for _, version := range []string{"", "0.2.0", "v0.1.1"} {
		if _, err := seed.Render(version, input(t)); err == nil {
			t.Errorf("rendered for version %q", version)
		}
	}
}

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

// Package seed renders the configuration a gateway boots on the first time:
// the NoCloud user-data that the image's seed unit installs, verbatim, as the
// config.boot the router loads (docs/kubevirt.md). It is a whole config.boot,
// so it carries every default of the image that has to survive, and it is
// rendered per release of the image, because the footer that ends it names the
// configuration version of one image, and a footer that names another makes the
// router migrate the configuration on boot or refuse it.
//
// What it holds is what the engine cannot set over the API before the API is
// up: the API key and the certificate the API presents, and a firewall that is
// closed from the first boot until the engine applies the full one. Everything
// else the engine applies over the API.
//
// The firewall covers IPv6 as well as IPv4: the guest has a link-local IPv6
// address whatever the cluster runs, and a chain that is not configured filters
// nothing. Of the image's defaults, the NTP service for private networks is
// left out, since a gateway is a time client and serves time to nobody.
package seed

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"text/template"
)

// image is what the seed has to agree with in one release of the image.
type image struct {
	Version string
	// Login is the account the image's own default configuration defines. The
	// seed keeps it, locked, so its well-known password opens nothing.
	Login string
	// ConfigVersion is the component map the image writes in the footer when it
	// saves a configuration. It is read off the image, never edited by hand: a
	// component named at a version the image does not have is how a migration
	// is triggered rather than avoided.
	ConfigVersion string
}

// configVersion is the footer of the 0.1.1 image, taken from a configuration
// it saved.
const configVersion = "bgp@8:broadcast-relay@1:cluster@2:config-management@1:conntrack@6:conntrack-sync@2:" +
	"container@3:dhcp-relay@2:dhcp-server@11:dhcpv6-server@6:dns-dynamic@5:dns-forwarding@4:firewall@20:" +
	"flow-accounting@3:https@7:ids@2:interfaces@34:ipoe-server@4:ipsec@14:isis@3:l2tp@10:lldp@3:mdns@1:" +
	"monitoring@2:nat@8:nat66@3:nhrp@1:ntp@3:openconnect@3:openvpn@6:ospf@2:pim@1:pki@1:policy@9:" +
	"pppoe-server@13:pptp@6:qos@3:quagga@12:reverse-proxy@3:rip@1:rpki@2:snmp@3:ssh@3:sstp@7:system@33:" +
	"vpp@6:vrf@4:vrrp@4:vyos-accel-ppp@2:wanloadbalance@4:webproxy@2"

// images are the releases of the image the engine can seed, newest first.
// 0.1.0 is 0.1.1 before its default login was renamed: both were built from
// the same pinned sources a day apart, so they share the component map.
var images = []image{
	{Version: "0.1.1", Login: "admin", ConfigVersion: configVersion},
	{Version: "0.1.0", Login: "vyos", ConfigVersion: configVersion},
}

// Versions returns the releases of the image the engine can seed, newest first.
func Versions() []string {
	out := make([]string, 0, len(images))
	for _, img := range images {
		out = append(out, img.Version)
	}
	return out
}

// Input is what makes a seed one gateway's.
type Input struct {
	HostName string
	// ManagementAllow is the network the management API admits calls from. With
	// none the API admits nobody until the engine applies one, which it cannot,
	// since it is the engine that calls.
	ManagementAllow string
	APIKey          string
	// Certificate and PrivateKey are the PEM of what the management API
	// presents. The appliance takes both as the bare base64 of their DER, and
	// the key as PKCS#8, the one form it reads.
	Certificate []byte
	PrivateKey  []byte
}

//go:embed config.boot.tmpl
var text string

var tmpl = template.Must(template.New("config.boot").Option("missingkey=error").Parse(text))

var (
	hostNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	apiKeyRe   = regexp.MustCompile(`^[A-Za-z0-9]{16,128}$`)
)

// Render returns the config.boot of a release of the image for in. Every value
// it puts in the file is checked to be of a shape that cannot leave the quotes
// it sits in, so nothing can end a node and start another.
func Render(version string, in Input) ([]byte, error) {
	i := slices.IndexFunc(images, func(img image) bool { return img.Version == version })
	if i < 0 {
		return nil, fmt.Errorf("seed: version %q is not one this engine seeds (it seeds %v)", version, Versions())
	}
	img := images[i]
	if !hostNameRe.MatchString(in.HostName) {
		return nil, fmt.Errorf("seed: %q is not a host name", in.HostName)
	}
	if in.ManagementAllow != "" {
		p, err := netip.ParsePrefix(in.ManagementAllow)
		switch {
		case err != nil || !p.Addr().Is4():
			return nil, fmt.Errorf("seed: the management network %q is not an IPv4 network", in.ManagementAllow)
		case p != p.Masked():
			return nil, fmt.Errorf("seed: the management network %q has host bits set", in.ManagementAllow)
		case p.Bits() == 0:
			return nil, fmt.Errorf("seed: the management network %q is every address", in.ManagementAllow)
		}
	}
	if !apiKeyRe.MatchString(in.APIKey) {
		return nil, errors.New("seed: the API key is not 16 to 128 letters and digits")
	}
	cert, err := body(in.Certificate, "CERTIFICATE")
	if err != nil {
		return nil, err
	}
	key, err := body(in.PrivateKey, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	err = tmpl.Execute(&b, map[string]string{
		"HostName":        in.HostName,
		"ManagementAllow": in.ManagementAllow,
		"APIKey":          in.APIKey,
		"Certificate":     cert,
		"PrivateKey":      key,
		"Login":           img.Login,
		"ConfigVersion":   img.ConfigVersion,
		"Version":         img.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	return b.Bytes(), nil
}

// body returns the DER of a PEM block of the given type as one line of base64.
func body(pemBytes []byte, blockType string) (string, error) {
	block, rest := pem.Decode(pemBytes)
	if block == nil || block.Type != blockType || len(bytes.TrimSpace(rest)) != 0 {
		return "", fmt.Errorf("seed: not a single PEM %s", blockType)
	}
	return base64.StdEncoding.EncodeToString(block.Bytes), nil
}

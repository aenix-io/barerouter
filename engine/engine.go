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

// Package engine is the engine of the site-router operator for the barerouter
// appliance. Importing it registers it under Name:
//
//	import _ "github.com/aenix-io/barerouter/engine"
//
// The engine is released with the image: a tag of this module goes with the
// image release of the same version, and Info lists the releases of the image
// it can seed and drive. Which image the operator boots is the operator's
// configuration, never the engine's. docs/management-api.md is what the engine
// relies on in the image.
package engine

import (
	"fmt"
	"maps"
	"net"
	"regexp"
	"strings"

	seam "github.com/aenix-io/site-router-operator/api/engine"

	"github.com/aenix-io/barerouter/engine/internal/appliance"
	"github.com/aenix-io/barerouter/engine/internal/seed"
)

// Name is the name the engine is registered under.
const Name = "barerouter"

// What every release of the image the engine drives needs from the operator:
// the image is an amd64 disk of 10 GB, imported onto a volume with room for
// the import, and the management API listens on the HTTPS port.
const (
	arch           = "amd64"
	diskGiB        = 12
	managementPort = 443
)

func init() {
	seam.Register(Name, func() (seam.Engine, error) { return New(), nil })
}

// Engine drives barerouter gateways.
type Engine struct {
	// secrets are every secret the engine has been given or has read, for
	// every session to redact: a session reads a gateway's configuration before
	// it knows the keys in it, and a session that applied nothing would
	// otherwise not know the pre-shared key an error can quote.
	secrets *secretSet
}

// New returns an engine. The operator gets one through the registry.
func New() *Engine { return &Engine{secrets: newSecretSet()} }

// Info implements seam.Engine.
func (e *Engine) Info() seam.Info {
	return seam.Info{Name: Name, ContractVersion: seam.ContractVersion, Versions: seed.Versions()}
}

var (
	instanceRe  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	namespaceRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
)

// Bootstrap implements seam.Engine. It mints credentials only when the request
// carries none; credentials it is given come back unchanged, and credentials
// that are incomplete or do not belong together are an error, never a reason to
// mint new ones, which would lock the operator out of a gateway seeded with the
// old ones.
func (e *Engine) Bootstrap(r seam.BootstrapRequest) (seam.BootstrapPlan, error) {
	plan, err := e.bootstrap(r)
	if err != nil {
		red := e.secrets.with(credentialSecrets(r.Existing)...)
		return seam.BootstrapPlan{}, red.err("bootstrap", err)
	}
	return plan, nil
}

func (e *Engine) bootstrap(r seam.BootstrapRequest) (seam.BootstrapPlan, error) {
	if r.Version == "" {
		return seam.BootstrapPlan{}, fmt.Errorf("no version of the image was asked for (supported: %v)", seed.Versions())
	}
	if !instanceRe.MatchString(r.Instance) || !namespaceRe.MatchString(r.Namespace) {
		return seam.BootstrapPlan{}, fmt.Errorf("instance %q in namespace %q is not a name a gateway can be given", r.Instance, r.Namespace)
	}
	// The host name is one DNS label; an instance can be a dotted name.
	host := strings.ReplaceAll(r.Instance, ".", "-")
	if len(host) > 63 {
		host = strings.TrimRight(host[:63], "-")
	}

	var creds map[string][]byte
	if len(r.Existing) == 0 {
		var err error
		if creds, err = mint(r.Instance, r.Namespace); err != nil {
			return seam.BootstrapPlan{}, err
		}
	} else {
		if err := check(r.Existing); err != nil {
			return seam.BootstrapPlan{}, err
		}
		creds = maps.Clone(r.Existing)
	}
	e.secrets.add(credentialSecrets(creds)...)

	userdata, err := seed.Render(r.Version, seed.Input{
		HostName:        host,
		ManagementAllow: r.ManagementAllow,
		APIKey:          string(creds[credAPIKey]),
		Certificate:     creds[credCertificate],
		PrivateKey:      creds[credPrivateKey],
	})
	if err != nil {
		return seam.BootstrapPlan{}, err
	}
	return seam.BootstrapPlan{
		Arch:           arch,
		DiskGiB:        diskGiB,
		SeedFiles:      map[string][]byte{seam.SeedUserData: userdata},
		Credentials:    creds,
		ManagementPort: managementPort,
	}, nil
}

// Connect implements seam.Engine. It reaches the gateway only over HTTPS, and
// only when the gateway presents the certificate it was seeded with.
func (e *Engine) Connect(ep seam.Endpoint) (seam.Session, error) {
	if err := check(ep.Credentials); err != nil {
		return nil, e.secrets.with(credentialSecrets(ep.Credentials)...).err("connect", err)
	}
	e.secrets.add(credentialSecrets(ep.Credentials)...)
	red := e.secrets.with()
	if _, _, err := net.SplitHostPort(ep.Address); err != nil {
		return nil, red.err("connect", fmt.Errorf("the address of the gateway is not host:port"))
	}
	key := ep.Credentials[credAPIKey]
	c, err := appliance.New("https://"+ep.Address, string(key), appliance.WithPinnedCertificate(ep.Credentials[credCertificate]))
	if err != nil {
		return nil, red.err("connect", err)
	}
	return &session{engine: e, client: c, redact: red, revisionKey: key}, nil
}

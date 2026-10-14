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
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"regexp"
	"slices"
	"time"
)

// The credentials of a gateway, keys of BootstrapPlan.Credentials and of
// Endpoint.Credentials. The operator keeps them in a Secret and hands them back;
// the seed carries the same values into the gateway.
const (
	// credAPIKey is the key of the management API.
	credAPIKey = "api-key"
	// credCertificate is the PEM certificate the management API presents, which
	// the engine pins. It is not secret, but the credentials are the one thing
	// the operator hands back to Connect.
	credCertificate = "tls.crt"
	// credPrivateKey is the PEM PKCS#8 key of that certificate. The engine
	// never uses it; it is kept so that a seed rendered again is the same seed.
	credPrivateKey = "tls.key"
)

// certificateLifetime is how long a gateway's certificate is valid. Nothing
// renews it: a gateway that outlives it is reached by nobody until it is
// bootstrapped again.
const certificateLifetime = 10 * 365 * 24 * time.Hour

var apiKeyRe = regexp.MustCompile(`^[A-Za-z0-9]{16,128}$`)

// mint makes the credentials of a new gateway: an API key, and a self-signed
// Ed25519 certificate for a name of its own. Ed25519 because the appliance
// reads a private key only as PKCS#8, and that is the form Go writes it in.
// The name is under .invalid, which never resolves: it is what the client asks
// the gateway to prove, while it dials the gateway's address.
func mint(instance, namespace string) (map[string][]byte, error) {
	name := instance + "." + namespace + ".site-router.invalid"
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("minting the key of the management API: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("minting the key of the management API: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		// An hour back, so that a gateway whose clock runs behind the
		// operator's does not present a certificate that is not valid yet.
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certificateLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		return nil, fmt.Errorf("minting the certificate of the management API: %w", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("minting the certificate of the management API: %w", err)
	}
	return map[string][]byte{
		credAPIKey:      []byte(rand.Text()),
		credCertificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		credPrivateKey:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}),
	}, nil
}

// check reports whether creds are the credentials of a gateway: a key of the
// form the engine mints, and a certificate and its key that belong together.
// The errors name what is wrong, never a value.
func check(creds map[string][]byte) error {
	for _, k := range []string{credAPIKey, credCertificate, credPrivateKey} {
		if len(creds[k]) == 0 {
			return fmt.Errorf("the credentials have no %q (they have %v)", k, slices.Sorted(maps.Keys(creds)))
		}
	}
	if !apiKeyRe.Match(creds[credAPIKey]) {
		return errors.New(`the credential "api-key" is not 16 to 128 letters and digits`)
	}
	block, _ := pem.Decode(creds[credCertificate])
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New(`the credential "tls.crt" is not a PEM certificate`)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(cert.DNSNames) == 0 {
		return errors.New(`the credential "tls.crt" is not a certificate for a host name`)
	}
	block, _ = pem.Decode(creds[credPrivateKey])
	if block == nil || block.Type != "PRIVATE KEY" {
		return errors.New(`the credential "tls.key" is not a PEM PKCS#8 key`)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return errors.New(`the credential "tls.key" is not a PEM PKCS#8 key`)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return errors.New(`the credential "tls.key" cannot sign`)
	}
	pub, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(pub, cert.RawSubjectPublicKeyInfo) {
		return errors.New(`the credentials "tls.crt" and "tls.key" do not belong together`)
	}
	return nil
}

// credentialSecrets returns what of creds must not reach an error, in every
// form it can take there: the key of the certificate also appears in the
// gateway's configuration as one line of base64.
func credentialSecrets(creds map[string][]byte) []string {
	out := make([]string, 0, len(creds)+1)
	for _, k := range slices.Sorted(maps.Keys(creds)) {
		out = append(out, string(creds[k]))
	}
	if block, _ := pem.Decode(creds[credPrivateKey]); block != nil {
		out = append(out, base64.StdEncoding.EncodeToString(block.Bytes))
	}
	return out
}

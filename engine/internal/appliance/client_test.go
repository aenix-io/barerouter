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

package appliance_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aenix-io/barerouter/engine/internal/appliance"
)

const testAPIKey = "test-api-key"

// server serves handler over TLS and returns a client pinned to its certificate,
// which every httptest TLS server shares.
func server(t *testing.T, handler http.HandlerFunc) *appliance.Client {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	c, err := appliance.New(srv.URL, testAPIKey, appliance.WithPinnedCertificate(certPEM(srv)))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func certPEM(srv *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

// readForm returns the data and key fields of a request, after checking that it
// is the form-encoded POST the API takes.
func readForm(t *testing.T, r *http.Request) (data, key string) {
	t.Helper()
	if r.Method != http.MethodPost {
		t.Errorf("method %s, want POST", r.Method)
	}
	if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type %q, want a form", got)
	}
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}
	return r.PostForm.Get("data"), r.PostForm.Get("key")
}

func answer(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func TestConfigureSendsTheBatchInOneRequest(t *testing.T) {
	t.Parallel()
	var calls int
	var sent []appliance.Op
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/configure" {
			t.Errorf("path %s, want /configure", r.URL.Path)
		}
		data, key := readForm(t, r)
		if key != testAPIKey {
			t.Errorf("key %q, want %q", key, testAPIKey)
		}
		if err := json.Unmarshal([]byte(data), &sent); err != nil {
			t.Errorf("data is not a list of operations: %v", err)
		}
		answer(w, `{"success": true, "data": "", "error": null}`)
	})
	ops := []appliance.Op{
		{Op: appliance.OpDelete, Path: []string{"protocols", "bgp"}},
		{Op: appliance.OpSet, Path: []string{"vpn", "ipsec", "site-to-site", "peer", "p", "force-udp-encapsulation"}},
		{Op: appliance.OpSet, Path: []string{"vpn", "ipsec", "site-to-site", "peer", "p", "remote-address"}, Value: "203.0.113.10"},
	}
	if err := c.Configure(t.Context(), ops); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(sent) != len(ops) {
		t.Fatalf("%d requests carrying %+v, want one carrying %+v", calls, sent, ops)
	}
	for i := range ops {
		if sent[i].Op != ops[i].Op || sent[i].Value != ops[i].Value || strings.Join(sent[i].Path, " ") != strings.Join(ops[i].Path, " ") {
			t.Errorf("operation %d = %+v, want %+v", i, sent[i], ops[i])
		}
	}
}

// A valueless set and a delete go out without a value key at all, which is how
// the API tells them from a set to the empty string.
func TestConfigureOmitsEmptyValues(t *testing.T) {
	t.Parallel()
	var data string
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ = readForm(t, r)
		answer(w, `{"success": true, "data": null, "error": null}`)
	})
	if err := c.Configure(t.Context(), []appliance.Op{{Op: appliance.OpDelete, Path: []string{"protocols", "bgp"}}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(data, "value") {
		t.Errorf("a delete carried a value: %s", data)
	}
}

func TestConfigureSendsNothingForAnEmptyBatch(t *testing.T) {
	t.Parallel()
	called := false
	c := server(t, func(http.ResponseWriter, *http.Request) { called = true })
	if err := c.Configure(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("an empty batch reached the appliance")
	}
}

func TestARefusalKeepsTheAppliancesReason(t *testing.T) {
	t.Parallel()
	c := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		answer(w, `{"success": false, "data": null, "error": "Commit failed: peer name must be alphanumeric"}`)
	})
	err := c.Configure(t.Context(), []appliance.Op{{Op: appliance.OpSet, Path: []string{"x"}}})
	var refused *appliance.Error
	if !errors.As(err, &refused) {
		t.Fatalf("got %v, want an *appliance.Error", err)
	}
	if refused.Status != http.StatusBadRequest || !strings.Contains(refused.Message, "must be alphanumeric") {
		t.Errorf("got %+v, want the status and the appliance's message", refused)
	}
}

func TestARefusalWithoutAReasonSaysSo(t *testing.T) {
	t.Parallel()
	c := server(t, func(w http.ResponseWriter, _ *http.Request) {
		answer(w, `{"success": false, "data": null, "error": null}`)
	})
	_, err := c.Retrieve(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "no reason given") {
		t.Errorf("got %v", err)
	}
}

// An answer that is not the envelope can carry anything, the configuration and
// its secrets included, and its body must not reach the error: a prefix cut for
// length is how a secret gets past redaction.
func TestAnAnswerThatIsNotTheEnvelopeIsNotQuoted(t *testing.T) {
	t.Parallel()
	const secret = "psk-that-must-not-leak"
	tests := map[string]http.HandlerFunc{
		"http 500": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "traceback: secret "+secret, http.StatusInternalServerError)
		},
		"not json": func(w http.ResponseWriter, _ *http.Request) {
			answer(w, "<html>"+secret+"</html>")
		},
	}
	for name, handler := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := server(t, handler)
			_, err := c.Retrieve(t.Context(), nil)
			if err == nil {
				t.Fatal("no error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the error quotes the answer: %v", err)
			}
		})
	}
}

func TestErrorsDoNotCarryTheAPIKey(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	pin := certPEM(srv)
	srv.Close() // a connection error carries the request URL
	c, err := appliance.New(srv.URL, testAPIKey, appliance.WithPinnedCertificate(pin))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Retrieve(t.Context(), nil)
	if err == nil || strings.Contains(err.Error(), testAPIKey) {
		t.Errorf("got %v", err)
	}
}

// The key is in the body of every request, so it never goes in clear text, nor
// with credentials or anything else in the address that errors would repeat.
func TestOnlyAnHTTPSAddressIsAccepted(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{
		"http://10.244.0.5:443",
		"10.244.0.5:443",
		"https://",
		"https://user:pass@10.244.0.5:443",
		"https://10.244.0.5:443?key=x",
	} {
		if _, err := appliance.New(addr, testAPIKey); err == nil {
			t.Errorf("%q was accepted", addr)
		}
	}
}

// A redirect makes an HTTP client resend the body, key included, to wherever it
// points. The target here presents the same certificate as the appliance, so
// only the refusal to follow keeps the key from it.
func TestRedirectsAreNotFollowed(t *testing.T) {
	t.Parallel()
	var reached atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		answer(w, `{"success": true, "data": null, "error": null}`)
	}))
	t.Cleanup(target.Close)
	for _, code := range []int{http.StatusMovedPermanently, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		c := server(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+r.URL.Path, code)
		})
		err := c.Configure(t.Context(), []appliance.Op{{Op: appliance.OpSet, Path: []string{"x"}}})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("http %d", code)) {
			t.Errorf("redirect %d: got %v, want an error naming it", code, err)
		}
	}
	if reached.Load() {
		t.Error("the redirect target received the request, and the key in it")
	}
}

// impostor is a TLS server that presents the certificate every httptest server
// shares, and records whether anything reached it.
func impostor(t *testing.T) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	var reached atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		answer(w, `{"success": true, "data": {}, "error": null}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &reached
}

// A custom HTTP client that trusts the server must not take the place of the
// pin, whichever of the two options comes first.
func TestThePinHoldsWhateverTheOrderOfTheOptions(t *testing.T) {
	t.Parallel()
	pin, _ := selfSigned(t, "a.ns.gateway.invalid")
	orders := map[string]func(*http.Client) []appliance.Option{
		"pin first": func(hc *http.Client) []appliance.Option {
			return []appliance.Option{appliance.WithPinnedCertificate(pin), appliance.WithHTTPClient(hc)}
		},
		"client first": func(hc *http.Client) []appliance.Option {
			return []appliance.Option{appliance.WithHTTPClient(hc), appliance.WithPinnedCertificate(pin)}
		},
	}
	for name, opts := range orders {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv, reached := impostor(t)
			c, err := appliance.New(srv.URL, testAPIKey, opts(srv.Client())...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Retrieve(t.Context(), nil); err == nil || reached.Load() {
				t.Errorf("the client trusted its HTTP client's roots instead of the pin (err %v)", err)
			}
		})
	}
}

// The pin is applied to a copy: the caller's client keeps trusting what it did,
// and keeps following redirects if it did.
func TestTheCallersClientIsNotChanged(t *testing.T) {
	t.Parallel()
	srv, _ := impostor(t)
	hc := srv.Client()
	transport := hc.Transport.(*http.Transport)
	tlsConfig := transport.TLSClientConfig
	pin, _ := selfSigned(t, "a.ns.gateway.invalid")
	if _, err := appliance.New(srv.URL, testAPIKey, appliance.WithHTTPClient(hc), appliance.WithPinnedCertificate(pin)); err != nil {
		t.Fatal(err)
	}
	if hc.Transport != transport || transport.TLSClientConfig != tlsConfig || hc.CheckRedirect != nil {
		t.Error("New changed the HTTP client it was given")
	}
	if _, err := hc.Get(srv.URL); err != nil {
		t.Errorf("the caller's client no longer reaches its server: %v", err)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A transport the client cannot set a proxy or TLS settings on is refused rather
// than used as it is.
func TestATransportThatCannotBeConfiguredIsRefused(t *testing.T) {
	t.Parallel()
	hc := &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) { return nil, errors.New("unused") })}
	pin, _ := selfSigned(t, "a.ns.gateway.invalid")
	for name, opts := range map[string][]appliance.Option{
		"with a pin":    {appliance.WithHTTPClient(hc), appliance.WithPinnedCertificate(pin)},
		"without a pin": {appliance.WithHTTPClient(hc)},
	} {
		if _, err := appliance.New("https://10.244.0.5:443", testAPIKey, opts...); err == nil {
			t.Errorf("%s: accepted a %T transport", name, hc.Transport)
		}
	}
}

func TestRetrieveAsksForTheConfiguration(t *testing.T) {
	t.Parallel()
	var q struct {
		Op   string    `json:"op"`
		Path *[]string `json:"path"`
	}
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/retrieve" {
			t.Errorf("path %s, want /retrieve", r.URL.Path)
		}
		data, _ := readForm(t, r)
		_ = json.Unmarshal([]byte(data), &q)
		answer(w, `{"success": true, "data": {"system": {"host-name": "gw"}}, "error": null}`)
	})
	raw, err := c.Retrieve(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The whole configuration is an empty path, not a missing one.
	if q.Op != "showConfig" || q.Path == nil || len(*q.Path) != 0 {
		t.Errorf("asked for %+v, want showConfig of []", q)
	}
	if !strings.Contains(string(raw), `"host-name"`) {
		t.Errorf("got %s", raw)
	}
}

func TestShowReturnsTheText(t *testing.T) {
	t.Parallel()
	tests := map[string]struct{ data, want string }{
		"text":    {`"line 1\nline 2\n"`, "line 1\nline 2\n"},
		"nothing": {`null`, ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := server(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/show" {
					t.Errorf("path %s, want /show", r.URL.Path)
				}
				data, _ := readForm(t, r)
				if !strings.Contains(data, `"op":"show"`) || !strings.Contains(data, `["vpn","ipsec","sa"]`) {
					t.Errorf("asked for %s", data)
				}
				answer(w, `{"success": true, "data": `+tt.data+`, "error": null}`)
			})
			got, err := c.Show(t.Context(), []string{"vpn", "ipsec", "sa"})
			if err != nil || got != tt.want {
				t.Errorf("got %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestShowInterfacesDetailParsesTheText(t *testing.T) {
	t.Parallel()
	text, _ := json.Marshal(fixture(t, "synthetic-interfaces-detail-numbered.txt"))
	c := server(t, func(w http.ResponseWriter, _ *http.Request) {
		answer(w, `{"success": true, "data": `+string(text)+`, "error": null}`)
	})
	got, err := c.ShowInterfacesDetail(t.Context())
	if err != nil || len(got) != 1 || got[0].Device != "eth0" {
		t.Errorf("got %+v, %v", got, err)
	}
}

// selfSigned mints a certificate the way the engine does for a gateway: Ed25519,
// self-signed, not a CA, issued for name.
func selfSigned(t *testing.T, name string) ([]byte, tls.Certificate) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		DNSNames:              []string{name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// pinnedClient starts a TLS server presenting present and returns a client
// pinned to pinPEM. The server is dialled by 127.0.0.1, which no certificate
// here names, as the operator dials a gateway by the address of its pod.
func pinnedClient(t *testing.T, present tls.Certificate, pinPEM []byte) *appliance.Client {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		answer(w, `{"success": true, "data": {}, "error": null}`)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{present}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	c, err := appliance.New(srv.URL, testAPIKey, appliance.WithPinnedCertificate(pinPEM))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPinnedCertificateIsAccepted(t *testing.T) {
	t.Parallel()
	certPEM, pair := selfSigned(t, "a.ns.gateway.invalid")
	if _, err := pinnedClient(t, pair, certPEM).Retrieve(t.Context(), nil); err != nil {
		t.Fatalf("the pinned certificate was refused: %v", err)
	}
}

// Whoever holds the address the operator dials gets the API key unless the
// certificate is checked. The same name under another key is someone else.
func TestAnotherKeyUnderTheSameNameIsRefused(t *testing.T) {
	t.Parallel()
	pinPEM, _ := selfSigned(t, "a.ns.gateway.invalid")
	_, impostor := selfSigned(t, "a.ns.gateway.invalid")
	_, err := pinnedClient(t, impostor, pinPEM).Retrieve(t.Context(), nil)
	if err == nil {
		t.Fatal("a certificate other than the pinned one was accepted")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Errorf("got %v, want a certificate verification failure", err)
	}
}

func TestTheCertificateOfAnotherGatewayIsRefused(t *testing.T) {
	t.Parallel()
	pinPEM, _ := selfSigned(t, "a.ns.gateway.invalid")
	_, other := selfSigned(t, "b.ns.gateway.invalid")
	if _, err := pinnedClient(t, other, pinPEM).Retrieve(t.Context(), nil); err == nil {
		t.Fatal("the certificate of another gateway was accepted")
	}
}

// A pin that cannot be applied is an error at construction, never a client that
// looks pinned and connects unverified.
func TestAnUnusablePinIsAnError(t *testing.T) {
	t.Parallel()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	tests := map[string][]byte{
		"empty":         nil,
		"not PEM":       []byte("not a certificate"),
		"not a cert":    []byte("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"),
		"broken DER":    []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"),
		"no host names": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
	for name, pin := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := appliance.New("https://10.244.0.5:443", testAPIKey, appliance.WithPinnedCertificate(pin)); err == nil {
				t.Error("an unusable pin was accepted")
			}
		})
	}
}

func TestNoAPIKeyIsAnError(t *testing.T) {
	t.Parallel()
	if _, err := appliance.New("https://10.244.0.5:443", ""); err == nil {
		t.Error("a client without an API key was built")
	}
}

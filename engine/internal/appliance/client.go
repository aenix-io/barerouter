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

// Package appliance talks to the management API of the appliance: the HTTPS
// API of the VyOS rolling release the image is built from. Every request is a
// form-encoded POST carrying the operation as JSON in `data` and the API key in
// `key`, and every answer is the same JSON envelope. docs/management-api.md is
// the contract this package relies on.
package appliance

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Op is one change sent to /configure. A set with an empty Value creates a node
// that carries no value: a valueless leaf such as `force-udp-encapsulation`, or
// the key of a tag node such as the address under `next-hop`.
type Op struct {
	Op    string   `json:"op"`
	Path  []string `json:"path"`
	Value string   `json:"value,omitempty"`
}

// Operations accepted by /configure.
const (
	OpSet    = "set"
	OpDelete = "delete"
)

type query struct {
	Op   string   `json:"op"`
	Path []string `json:"path"`
}

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// maxAnswer bounds what is read of an answer. The whole configuration of a
// gateway is a few hundred kilobytes at most.
const maxAnswer = 16 << 20

// Error is a request the appliance answered and refused. Message is the
// appliance's own text, which can quote the configuration it was sent,
// secrets included, so it is for a caller that redacts.
type Error struct {
	Endpoint string
	Status   int
	Message  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: the appliance refused the request (http %d): %s", e.Endpoint, e.Status, e.Message)
}

// Client talks to the management API of one appliance.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// settings are what the options ask for. The HTTP client is built from them
// once, after every option has been read, so the order of the options does not
// change what the client checks.
type settings struct {
	client *http.Client
	pin    *x509.Certificate
}

// Option configures a Client.
type Option func(*settings) error

// WithHTTPClient builds the client from hc, which tests use to reach an httptest
// server: its timeout and its transport's settings are kept, on a copy, and hc
// itself is not changed. Its transport has to be an *http.Transport, because
// the client takes the proxy and, with a pin, the TLS settings out of the
// caller's hands.
func WithHTTPClient(hc *http.Client) Option {
	return func(s *settings) error {
		if hc == nil {
			return errors.New("management API: nil HTTP client")
		}
		s.client = hc
		return nil
	}
}

// WithPinnedCertificate makes the client accept the appliance only when it
// presents the certificate certPEM.
//
// The API key authenticates the client to the appliance and says nothing about
// who answered. The appliance is dialled by the address of its pod, which a
// stale cache or a recycled address can hand to someone else, and the gateway
// pod is the one whose source-address checks the network relaxes, so without
// this the key goes to whoever holds the address. The certificate is
// self-signed and minted for one gateway, so it is the only root the client
// trusts, and the name it is issued for is the name the client asks for: the
// dialled address is not in the certificate and does not need to be.
func WithPinnedCertificate(certPEM []byte) Option {
	return func(s *settings) error {
		block, _ := pem.Decode(certPEM)
		if block == nil || block.Type != "CERTIFICATE" {
			return errors.New("management API: the pinned certificate is not a PEM certificate")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("management API: the pinned certificate does not parse: %w", err)
		}
		if len(cert.DNSNames) == 0 {
			return errors.New("management API: the pinned certificate names no host to verify the appliance against")
		}
		s.pin = cert
		return nil
	}
}

// New returns a client for the API at baseURL, such as https://10.244.0.5:443,
// authenticating with apiKey. An option that cannot do its job is an error, so a
// client that was asked to pin a certificate never connects without it.
//
// The key travels in the body of every request, so the client refuses what
// could hand it to someone other than the appliance: a scheme other than https,
// a redirect, which resends the body to wherever it points, and a proxy taken
// from the environment, which is not the pod address the operator dials.
func New(baseURL, apiKey string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("management API: no API key")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("management API: the address has to be https://host[:port], which carries the key encrypted and nothing else")
	}
	var s settings
	for _, opt := range opts {
		if err := opt(&s); err != nil {
			return nil, err
		}
	}
	hc, err := s.build()
	if err != nil {
		return nil, err
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: hc}, nil
}

// requestTimeout bounds one request to the appliance. A command of the 0.1
// images that needs privileges takes about 20 seconds, because the host name the
// guest was given does not resolve, and a /configure or a /retrieve can outlast
// 30 seconds of them.
const requestTimeout = 90 * time.Second

func (s settings) build() (*http.Client, error) {
	hc := &http.Client{Timeout: requestTimeout}
	base := http.DefaultTransport
	if s.client != nil {
		copied := *s.client
		hc = &copied
		if hc.Transport != nil {
			base = hc.Transport
		}
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("management API: the HTTP client's transport is a %T, not an *http.Transport, so its proxy and TLS settings cannot be enforced", base)
	}
	transport = transport.Clone()
	transport.Proxy = nil
	if s.pin != nil {
		pool := x509.NewCertPool()
		pool.AddCert(s.pin)
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, ServerName: s.pin.DNSNames[0], MinVersion: tls.VersionTLS12}
	}
	hc.Transport = transport
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return hc, nil
}

// Configure applies ops in one commit: the appliance applies all of them or
// none. An empty batch sends nothing.
func (c *Client) Configure(ctx context.Context, ops []Op) error {
	if len(ops) == 0 {
		return nil
	}
	_, err := c.post(ctx, "/configure", ops)
	return err
}

// Retrieve returns the running configuration under path as the JSON the API
// renders it in. An empty path is the whole configuration.
func (c *Client) Retrieve(ctx context.Context, path []string) (json.RawMessage, error) {
	if path == nil {
		path = []string{}
	}
	return c.post(ctx, "/retrieve", query{Op: "showConfig", Path: path})
}

// Show runs an operational-mode `show` command and returns its text. The API
// answers null when the command printed nothing, which is returned as "".
func (c *Client) Show(ctx context.Context, path []string) (string, error) {
	raw, err := c.post(ctx, "/show", query{Op: "show", Path: path})
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", fmt.Errorf("/show %s: the answer is not text", strings.Join(path, " "))
	}
	return text, nil
}

// ShowVPNIPSecSA reads `show vpn ipsec sa`.
func (c *Client) ShowVPNIPSecSA(ctx context.Context) ([]IPSecObservation, error) {
	text, err := c.Show(ctx, []string{"vpn", "ipsec", "sa"})
	if err != nil {
		return nil, err
	}
	return ParseIPSecSA(text), nil
}

// ShowInterfacesDetail reads `show interfaces detail`.
func (c *Client) ShowInterfacesDetail(ctx context.Context) ([]EthernetObservation, error) {
	text, err := c.Show(ctx, []string{"interfaces", "detail"})
	if err != nil {
		return nil, err
	}
	return ParseInterfacesDetail(text), nil
}

// ShowBGPSummary reads `show bgp summary`.
func (c *Client) ShowBGPSummary(ctx context.Context) ([]BGPObservation, error) {
	text, err := c.Show(ctx, []string{"bgp", "summary"})
	if err != nil {
		return nil, err
	}
	return ParseBGPSummary(text), nil
}

// post sends one request and unpacks the envelope. No error carries the body of
// an answer: an answer that is not the envelope can hold anything, the
// configuration with its secrets included, and a prefix cut off for length is
// where a secret survives redaction. The appliance's own error message is kept,
// in full, for the caller to redact.
func (c *Client) post(ctx context.Context, endpoint string, payload any) (json.RawMessage, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%s: encoding the request: %w", endpoint, err)
	}
	form := url.Values{"data": {string(encoded)}, "key": {c.apiKey}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%s: building the request: %w", endpoint, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return nil, fmt.Errorf("%s: reading the answer: %w", endpoint, err)
	}

	if resp.StatusCode/100 == 3 {
		return nil, fmt.Errorf("%s: http %d: the appliance redirects, and the key is not sent anywhere else", endpoint, resp.StatusCode)
	}
	var env envelope
	decodeErr := json.Unmarshal(body, &env)
	switch {
	case decodeErr == nil && !env.Success:
		msg := strings.TrimSpace(env.Error)
		if msg == "" {
			msg = "no reason given"
		}
		return nil, &Error{Endpoint: endpoint, Status: resp.StatusCode, Message: msg}
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("%s: http %d", endpoint, resp.StatusCode)
	case decodeErr != nil:
		return nil, fmt.Errorf("%s: the answer (%d bytes) is not the API's JSON envelope", endpoint, len(body))
	}
	return env.Data, nil
}

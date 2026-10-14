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

package appliance

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// The images answer a command that needs privileges in about 20 seconds, and a
// call that runs a few of them is not an error for that.
func TestTheDefaultTimeoutOutlastsTheSlowCommandsOfTheImage(t *testing.T) {
	c, err := New("https://10.244.0.5:443", "key")
	if err != nil {
		t.Fatal(err)
	}
	if c.http.Timeout < 60*time.Second {
		t.Errorf("the default timeout is %v, want at least a minute", c.http.Timeout)
	}
}

// The address is a pod's, and a proxy named by the environment would receive
// the key. This reads the transport rather than sending through a proxy: the
// standard library never proxies a loopback address, which is all a test
// server has, and reads the environment once per process.
func TestTheEnvironmentsProxyIsNotUsed(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	t.Setenv("https_proxy", "http://proxy.invalid:3128")
	proxied := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "proxy.invalid:3128"})}}
	for name, opts := range map[string][]Option{
		"default client":             nil,
		"a client that has a proxy":  {WithHTTPClient(proxied)},
		"a client that has no proxy": {WithHTTPClient(&http.Client{})},
	} {
		c, err := New("https://10.244.0.5:443", "key", opts...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		transport, ok := c.http.Transport.(*http.Transport)
		if !ok || transport.Proxy != nil {
			t.Errorf("%s: the transport has a proxy function", name)
		}
	}
}

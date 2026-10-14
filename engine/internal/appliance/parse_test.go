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
	"os"
	"path/filepath"
	"testing"

	"github.com/aenix-io/barerouter/engine/internal/appliance"
)

// The fixtures in testdata are named by where they come from:
//
//   - image-*: printed by a running appliance and kept verbatim. image-ipsec-sa-up
//     is the responder of an end-to-end run, image-ipsec-sa-up-far-end the
//     initiator on the other side of the same tunnel, with a shorter name and so
//     narrower columns.
//   - derived-*: the header of an image-* capture without its rows, the table
//     the image prints when no child SA is installed.
//   - synthetic-*: written by hand in the shape of the upstream documentation.
//     The swanctl listing is the shape of older upstream releases, which the
//     parser still reads.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseIPSecSA(t *testing.T) {
	t.Parallel()
	tests := []struct {
		fixture string
		want    []appliance.IPSecObservation
	}{
		{"image-ipsec-sa-up.txt", []appliance.IPSecObservation{
			{Name: "router1-tunnel-1", PeerAddress: "10.244.2.123", State: appliance.IPSecUp},
		}},
		{"image-ipsec-sa-up-far-end.txt", []appliance.IPSecObservation{
			{Name: "siteA-tunnel-1", PeerAddress: "10.10.100.200", State: appliance.IPSecUp},
		}},
		{"derived-ipsec-sa-no-child.txt", nil},
		{"synthetic-swanctl-established.txt", []appliance.IPSecObservation{
			{Name: "aws-prod", PeerAddress: "203.0.113.10", State: appliance.IPSecUp},
		}},
		{"synthetic-swanctl-connecting.txt", []appliance.IPSecObservation{
			{Name: "partner", PeerAddress: "198.51.100.20", State: appliance.IPSecConnecting},
		}},
		{"synthetic-swanctl-down.txt", []appliance.IPSecObservation{
			{Name: "lonely", PeerAddress: "198.51.100.99", State: appliance.IPSecDown},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			t.Parallel()
			got := appliance.ParseIPSecSA(fixture(t, tt.fixture))
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("connection %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseIPSecSAEmpty(t *testing.T) {
	t.Parallel()
	if got := appliance.ParseIPSecSA(""); len(got) != 0 {
		t.Errorf("got %+v from no output", got)
	}
}

// A routed connection lists INSTALLED after ESTABLISHED, and an unrelated word in
// the state position (IKEv2 on the SPI line) must not demote it.
func TestParseIPSecSAUpWins(t *testing.T) {
	t.Parallel()
	text := "       gw[1]: IKEv2 SPIs: 9ab...\n" +
		"       gw[1]: CONNECTING, IKE_SA_INIT\n" +
		"       gw{2}:  INSTALLED, TUNNEL\n" +
		"       gw[1]: CONNECTING, again\n"
	got := appliance.ParseIPSecSA(text)
	if len(got) != 1 || got[0].State != appliance.IPSecUp {
		t.Errorf("got %+v, want one connection, Up", got)
	}
}

func TestParseBGPSummary(t *testing.T) {
	t.Parallel()
	got := appliance.ParseBGPSummary(fixture(t, "synthetic-bgp-summary.txt"))
	want := []appliance.BGPObservation{
		{PeerAddress: "203.0.113.1", Session: appliance.BGPEstablished, Uptime: "00:05:03"},
		{PeerAddress: "203.0.113.2", Session: appliance.BGPIdle, Uptime: "never"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// Plain FRR prints the number of received prefixes where the state would be once
// a session is up.
func TestParseBGPSummaryNumericPrefixCountIsEstablished(t *testing.T) {
	t.Parallel()
	got := appliance.ParseBGPSummary(fixture(t, "synthetic-bgp-summary-numeric-pfx.txt"))
	want := appliance.BGPObservation{PeerAddress: "10.0.0.1", Session: appliance.BGPEstablished, Uptime: "00:12:00"}
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestParseBGPSummaryIgnoresTheHeader(t *testing.T) {
	t.Parallel()
	header := "Neighbor        V         AS   MsgRcvd   MsgSent   TblVer  InQ OutQ  Up/Down State/PfxRcd"
	if got := appliance.ParseBGPSummary(header); len(got) != 0 {
		t.Errorf("got %+v from the header alone", got)
	}
}

func TestParseInterfacesDetail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		fixture string
		want    []appliance.EthernetObservation
	}{
		// eth1.100 carries eth1's MAC and lo has no ethernet address: neither
		// counts, and the uppercase MAC of eth1 comes back lowercase.
		{"synthetic-interfaces-detail.txt", []appliance.EthernetObservation{
			{Device: "eth0", MAC: "52:54:00:11:22:33"},
			{Device: "eth1", MAC: "52:54:00:aa:bb:cc"},
		}},
		{"synthetic-interfaces-detail-numbered.txt", []appliance.EthernetObservation{
			{Device: "eth0", MAC: "02:00:c0:a8:01:01"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			t.Parallel()
			got := appliance.ParseInterfacesDetail(fixture(t, tt.fixture))
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("device %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseInterfacesDetailEmpty(t *testing.T) {
	t.Parallel()
	if got := appliance.ParseInterfacesDetail(""); len(got) != 0 {
		t.Errorf("got %+v from no output", got)
	}
}

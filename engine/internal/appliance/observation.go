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

package appliance

// IPSecState is the state of a security association.
type IPSecState string

// IPsec states.
const (
	IPSecUp         IPSecState = "Up"
	IPSecConnecting IPSecState = "Connecting"
	IPSecDown       IPSecState = "Down"
)

// IPSecObservation is one connection in `show vpn ipsec sa`.
type IPSecObservation struct {
	// Name is the connection name as printed: the peer name for an IKE SA, and
	// the peer name with a "-tunnel-<n>" suffix for a child SA in the table
	// the image prints. Telling the two apart needs the configured peer names,
	// which the caller has and this package does not.
	Name string
	// PeerAddress is the address of the remote end, when the output has it.
	PeerAddress string
	State       IPSecState
}

// EthernetObservation is one physical ethernet device in `show interfaces
// detail`.
type EthernetObservation struct {
	Device string // kernel name, such as eth0
	MAC    string // lowercase, colon-separated
}

// BGPState is the state of a BGP session.
type BGPState string

// BGP states.
const (
	BGPIdle        BGPState = "Idle"
	BGPConnect     BGPState = "Connect"
	BGPActive      BGPState = "Active"
	BGPOpenSent    BGPState = "OpenSent"
	BGPOpenConfirm BGPState = "OpenConfirm"
	BGPEstablished BGPState = "Established"
)

// BGPObservation is one neighbor in `show bgp summary`.
type BGPObservation struct {
	PeerAddress string
	Session     BGPState
	Uptime      string // as printed, such as 00:05:03 or never
}

# What the engine relies on in the image

The engine in this module is how the site-router operator drives a gateway booted from this image. It reaches the gateway in two ways only: the seed, which the image installs at boot (see [`docs/kubevirt.md`](../../docs/kubevirt.md)), and the HTTPS management API, the REST API of the VyOS rolling release the image is built from. This page is everything the engine relies on in both. A release of the image that changes any of it needs a release of the engine that knows, which is why the two are tagged together ([`docs/releasing.md`](../../docs/releasing.md)).

The operator enables the engine by importing the module, and names it `barerouter` in its configuration:

```go
import _ "github.com/aenix-io/barerouter/engine"
```

What is marked unverified below has not been seen on a running gateway and is the first thing to check on one.

## Credentials

`Bootstrap` mints the credentials of a gateway when the operator has none, and the operator keeps them in a Secret and hands them back on every later call. They are opaque to the operator; to the engine they are three keys.

| key | what it is |
|---|---|
| `api-key` | the key of the management API: 26 characters of base32 from the system's random source |
| `tls.crt` | the PEM certificate the management API presents: Ed25519, self-signed, not a CA, for the DNS name `<instance>.<namespace>.site-router.invalid`, valid for ten years from minting |
| `tls.key` | the PEM PKCS#8 key of that certificate, kept so that a seed rendered again is the same seed; the engine never uses it otherwise |

The certificate is not a secret, but the credentials are the one thing the operator hands back to `Connect`, which pins it. Credentials the operator hands to `Bootstrap` come back unchanged; credentials that are incomplete, or whose certificate and key do not belong together, are an error, never a reason to mint new ones. Nothing renews the certificate.

## The seed

The seed is the NoCloud user-data the image installs verbatim as `config.boot`, so it is a whole configuration, rendered per release of the image. It carries what the engine cannot set over the API before the API is up, and keeps the gateway closed until the first `Apply`.

- The API key, under `service https api keys id site-router-controller key`.
- The certificate and its key under `pki certificate site-router-api`, as the bare base64 of their DER, and `service https certificates certificate site-router-api`. The appliance reads a private key only as PKCS#8, which is why the key is Ed25519, whose PKCS#8 form is the one Go writes.
- `service https listen-address 0.0.0.0`, since the address of the pod is not known when the seed is rendered.
- A firewall that drops by default on every chain: IPv4 input admits return traffic and HTTPS from the operator's management network only, and drops decrypted traffic to ports 22 and 443 before anything else; IPv4 forward admits return traffic and traffic that was not decrypted; IPv6 input admits return traffic and ICMPv6 only, and IPv6 forward nothing. With no management network the API admits nobody. The IPv6 chains are unverified on a running gateway.
- The image's own login, locked, and no SSH. The image's NTP service for private networks is left out: a gateway is a time client only.
- The footer of the release: the configuration version the image writes when it saves a configuration, and the release version.

| release | login | configuration version |
|---|---|---|
| 0.1.1 | `admin` | taken from a configuration the 0.1.1 image saved |
| 0.1.0 | `vyos` | the same as 0.1.1, unverified: both were built from the same vyos-build pin a day apart |

## Transport

- HTTPS to the address of the gateway's pod on port 443, the port `Bootstrap` reports. The client trusts the certificate in the credentials and nothing else, and asks the gateway to prove the name in it; the address it dials is not in the certificate and does not need to be.
- Every request is a POST of a form with two fields: `data`, the request as JSON, and `key`, the API key. The key is in the body of every request, so the client refuses an address that is not `https`, does not follow redirects, which resend the body to wherever they point, and does not use a proxy from the environment.
- Every answer is the envelope `{"success": true|false, "data": ..., "error": "..."|null}`. A refused request has `success` false and the reason in `error`, usually with HTTP 400. The reason can quote what the request carried, secrets included; the engine redacts it before it returns it. A body that is not the envelope is never quoted at all.

## Endpoints

| endpoint | `data` | the engine uses it to |
|---|---|---|
| `/configure` | a list of `{"op": "set" or "delete", "path": [...], "value": "..."}`, `value` left out for a delete and for a node that takes none | apply the managed configuration |
| `/retrieve` | `{"op": "showConfig", "path": []}` | read the whole running configuration, for `Apply`, `Confirm` and `Observe` |
| `/show` | `{"op": "show", "path": ["vpn", "ipsec", "sa"]}`, `["interfaces", "detail"]` or `["bgp", "summary"]` | read the state of tunnels and BGP sessions, and the MAC address of each device |

What the engine takes the appliance to do:

- `/configure` applies the whole list in one commit, all of it or none of it, and a delete of a path that is not there is not an error. A commit is not saved: a reboot puts the gateway back on its seed, which the image installs again on every boot.
- `/retrieve` with an empty path returns the whole configuration, secrets in clear, as JSON in which a leaf with one value is a string, a leaf with several is a list of strings, and a valueless leaf, such as `disable` or `ipsec match-ipsec-in`, is an empty object. That shape is unverified for the leaves the source filter is judged by (`state`, `disable`, `ipsec`, `address`), and any other shape of them makes `Confirm` report the filter down, never up.
- `/show` returns the text the command prints, or null when it prints nothing. `vpn ipsec sa` prints a table with a row per installed child SA named `<peer>-tunnel-<n>`, captured from a running image in `internal/appliance/testdata`; `interfaces detail` prints in the layout of `ip addr`; `bgp summary` prints FRR's neighbor table.

## What the engine owns

Every `Apply` deletes these subtrees and sets them again in the same commit, so what left the operator's desired state leaves the gateway too, and a change by hand inside them is undone:

- `firewall ipv4 input filter`, `firewall ipv4 forward filter` and `firewall ipv4 name TUNNEL-INGRESS`
- `firewall ipv6 input filter` and `firewall ipv6 forward filter`
- `vpn ipsec`
- `protocols static route` and `protocols bgp`
- `interfaces ethernet <device> ip adjust-mss`, on the device whose MAC address the operator gives

Everything else stays as the seed set it. A tunnel is a site-to-site peer named after the operator's tunnel ID, with one child per pair of a local and a remote selector, UDP encapsulation forced, and one proposal (IKEv2, AES-256, SHA-256, DH group 14), since the operator passes no IKE or ESP parameters.

## The source filter

Decrypted traffic is sent by an unconditional jump of the forward filter, `ipsec match-ipsec-in`, into the rule set `TUNNEL-INGRESS`, which admits return traffic and, rule by rule, one remote network to one tenant address, and drops the rest. `Confirm` reads it off the running configuration on every call and reports it active only when the forward filter drops by default, sends all decrypted traffic to the rule set and lets nothing past it before or after, the rule set drops by default and admits at least one flow, and every flow it admits is from an IPv4 network of at least a /8 to a single IPv4 address. After an `Apply` in the same session, every admitted flow also has to be one the applied state allows. A zone-based firewall, which decides forwarding besides the forward filter, makes it not active, and so does a rule number that is not plain decimal.

## Paths checked on a running image

The paths and syntax of the IPv4 firewall, the site-to-site peer and its pre-shared key, `force-udp-encapsulation` and `adjust-mss` are the ones the previous controller validated on the rolling release this image is built from and in end-to-end runs on the 0.1.1 disk. The IPv6 firewall, BGP and the shape of `/retrieve` above have not been checked on a running gateway.

# barerouter

barerouter is a router image built from the public VyOS rolling sources with the VyOS name, trademarks and logo artwork removed. It is published with its SBOM and the list of every package it contains, so it can be redistributed and run where a VyOS-compatible appliance is needed.

barerouter is not produced, endorsed or supported by VyOS Inc. VyOS and the VyOS logo are trademarks of their owner and are named here only to say where the sources come from.

## Where it comes from

- [vyos/vyos-build](https://github.com/vyos/vyos-build) at the commit pinned as `VYOS_BUILD_REF` in the [`Makefile`](./Makefile), built inside the `vyos/vyos-build` container pinned as `VYOS_BUILD_IMAGE`.
- Debian bookworm and the VyOS rolling package repository, as they are on the day of the build. The exact version of every installed package is in the `.packages.tsv` file published with each release.

## What a release contains

- `barerouter-<version>-amd64.iso` and its `.sha256`
- `barerouter-<version>-amd64.cdx.json` and `.spdx.json`, the SBOM in CycloneDX and SPDX
- `barerouter-<version>-amd64.packages.tsv`, every installed binary package with its source package and version
- `barerouter-<version>-amd64.sources.md`, where the source of each of those packages is: the snapshot.debian.org page of the exact Debian source version, or the VyOS repository and the vyos-build scripts it was built from
- `barerouter-<version>-amd64.vyos-build.tar.gz`, the vyos-build tree the image was built from, and `barerouter-<version>-amd64.refs.lock`, every upstream repository and reference in it resolved to the commit it named on the day of the build
- `barerouter-<version>-kubevirt-amd64.qcow2`, its `.sha256` and `.sources.md`, and the same disk as the containerDisk `ghcr.io/aenix-io/barerouter/kubevirt-disk:<version>`: the ISO installed and turned into a KubeVirt appliance, described in [`docs/kubevirt.md`](./docs/kubevirt.md). The ISO itself is published unmodified.

## Building

A build needs Docker able to run a `--privileged` container, about 20 GB of free disk and roughly an hour.

```bash
make verify-pin      # the pinned vyos-build commit still matches the kernel on the mirror
make test-hook       # the debranding hook against the vyos-1x package the mirror serves now
make iso VERSION=0.1.0
make check VERSION=0.1.0
make disk VERSION=0.1.0         # the KubeVirt disk, from the ISO above
make check-disk VERSION=0.1.0
make disk-image VERSION=0.1.0   # the containerDisk; PUSH=1 to push it
```

`make iso` runs the first two itself. `make check` and `make check-disk` read the finished artifacts and fail if the VyOS name or artwork is still where a user sees it.

## Debranding

What is replaced, what is deliberately kept and why is in [`docs/debranding.md`](./docs/debranding.md). In short: the name and artwork a user sees are replaced; copyright and licence notices, code comments and identifiers such as `ID=vyos` or `/usr/libexec/vyos` are not branding and stay. The default login is `admin`, password `admin`.

## License

The scripts in this repository are Apache-2.0, see [`LICENSE`](./LICENSE). The image is a collection of components under their own licences; the image says so at `/usr/share/vyos/EULA`, which `show license` prints, and the licence of each installed package is under `/usr/share/doc/<package>/copyright`.

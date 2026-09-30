# Releasing

A release is a tag `vX.Y.Z` on `main`. Pushing it runs `.github/workflows/release.yml`, which builds the image from that commit, runs the branding check, and publishes the GitHub release with every artifact of that one build: the ISO, its `.sha256`, both SBOMs, the package list and the sources manifest. A release is never assembled from artifacts of different runs, because the image content is not a function of the commit alone (see below).

## What pins what

- `VYOS_BUILD_REF` pins the vyos-build tree: the build scripts, the package lists, and through `data/defaults.toml` the kernel version.
- `VYOS_BUILD_IMAGE` pins the build container by digest.
- Nothing pins the packages. vyos-1x, FRR, strongSwan and the rest are installed from the VyOS rolling repository and Debian as they are on the day of the build, so two builds of the same commit can differ. The `.packages.tsv` and `.sources.md` of a release are the record of what that image holds.

## Moving the vyos-build pin

The rolling mirror keeps only the current kernel. A pin whose `kernel_version` is no longer on the mirror cannot be built, and `make verify-pin` says so before an hour of live-build does.

1. Take the current head of `rolling` in vyos-build: `gh api repos/vyos/vyos-build/commits/rolling --jq .sha`.
2. Check it against the mirror: `make verify-pin VYOS_BUILD_REF=<sha>`. When the head asks for a kernel the mirror does not have yet, wait for the mirror rather than pinning an older commit; an older commit asks for a kernel the mirror has already dropped.
3. Set `VYOS_BUILD_REF` in the `Makefile`, run `make test-hook`, and open a pull request. `build.yml` builds the image and runs the branding check on it.
4. When a debranding edit stops matching, follow the last section of [`debranding.md`](./debranding.md).

The container digest moves the same way: read the digest of `vyos/vyos-build:rolling` from Docker Hub, set `VYOS_BUILD_IMAGE`, and let `build.yml` prove it.

## Cutting a release

1. Merge everything the release should carry into `main` and wait for `build.yml` to pass on it.
2. Tag `main`: `git tag -s vX.Y.Z -m vX.Y.Z` and push the tag.
3. `release.yml` publishes the release. If it fails after the build, fix the cause and re-run the workflow for the same tag; do not move the tag.

A tag can stop being rebuildable: once the mirror drops the kernel its pin names, re-running `release.yml` for it fails. The published release stays as it is; a new release moves the pin.

## Corresponding source

Every image is a collection of GPL-licensed packages, so a release has to make their corresponding source available to anyone who receives the image. barerouter does it in two parts.

- **Pointers, published with the release.** `.sources.md` links every Debian source package to its exact version on snapshot.debian.org, which Debian never removes. For the packages VyOS builds, `.vyos-build.tar.gz` carries the vyos-build tree the image was built from, with every package-build definition, patch and kernel configuration, and `.refs.lock` resolves every upstream repository and reference in it to the commit it named on the day of the build. The KubeVirt disk's `.sources.md` adds the two Debian packages it installs and points at this repository's tag for the rest.
- **A written offer.** For what is only pointed to, GPLv2 section 3(b) needs an offer, valid for three years from each release, to give any third party the complete corresponding source for no more than the cost of performing the distribution. The offer is in `debrand/NOTICE.image`, which the image carries at `/usr/share/vyos/EULA`, and the release job refuses to publish while it is missing.

Whoever gives the offer has to be able to honour it for three years, including when an upstream has since deleted a commit the lock names. Moving to a full source archive removes that dependency: fetch every repository in `.refs.lock` at its commit, the kernel tarball, and the sources the package-build scripts download themselves, and publish them with the release. The lock and the tree are the input for that; nothing else in the build has to change.

## Consumers

cozystack's site-router pins a release's containerDisk by digest and imports it as the gateway's boot disk. Moving it to a new release is a pull request there that updates the pin and re-captures its `config.boot` footer from the new image, as its own documentation describes.

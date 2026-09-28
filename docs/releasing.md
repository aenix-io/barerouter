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

## Consumers

cozystack's site-router pins a release by the ISO's URL and SHA256 and turns it into its appliance disk. Moving it to a new release is a pull request there that updates both, and re-captures its `config.boot` from the new image as its own documentation describes.

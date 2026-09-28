# KubeVirt disk

Each release carries a second artifact next to the ISO: the ISO installed onto a disk and turned into a KubeVirt appliance. The ISO is never modified for it; someone who wants the plain router takes the ISO, someone who runs it as a KubeVirt VM takes the disk.

- `barerouter-<version>-kubevirt-amd64.qcow2`, its `.sha256`, and its own `.sources.md`, which is the ISO's plus what the disk adds.
- `ghcr.io/aenix-io/barerouter/kubevirt-disk:<version>`, the same qcow2 as a containerDisk at `/disk/barerouter.qcow2`, which KubeVirt and CDI's registry importer both read and which can be pinned by digest.

The disk is built by `hack/build-disk.sh` from the ISO and the vyos-build tree of the same run: `build-vyos-image --reuse-iso` with `kubevirt/flavor.toml` makes a 10 GB raw disk with the serial console on `ttyS0` at 115200, and `kubevirt/inject-appliance.sh` adds the appliance to it.

## What the appliance adds

These are the interface a consumer builds on, so a change to any of them is a breaking change of the disk.

- **Configuration seed.** `vyos-appliance-seed.service` runs before `vyos-router.service`. It reads `user-data` from the NoCloud disk labelled `cidata` and installs it as the configuration `vyos-router` loads, so `user-data` has to be a complete `config.boot`. KubeVirt's `cloudInitNoCloud` produces such a disk from a Secret. The seed never fails the boot: without a usable seed the router comes up on the baked default configuration.
- **Diagnostics disk.** An optional second disk labelled `cozydiag`, read by the seed, for bring-up diagnostics on the serial console. It is separate from `cidata` so that a broken diagnostics payload cannot touch the configuration.
- **Configuration report.** `vyos-appliance-config-report.service` prints on the console why a configuration was rejected, which upstream reports only as "Configuration error".
- **Guest agent.** `qemu-guest-agent` from Debian.
- **Locked bootloader.** GRUB `superusers` with a password generated at build time and discarded, so the boot command line, entry editing and the password reset entry are unreachable from the console; the normal boot entries are marked `--unrestricted` so the VM still boots unattended. `hack/test-appliance.sh` covers the two ways this can fail open silently.
- **Default configuration.** `kubevirt/overlay/config.boot.default`: the image's own default with `eth0` on DHCP and the `vyos` login locked. It is what the router runs when no seed is present.

## Moving the pin

`kubevirt/overlay/config.boot.default` follows the syntax of the vyos-1x the pin installs. When the pin moves, take the new image's `/usr/share/vyos/config.boot.default`, apply the two changes above, and replace the file, rather than carrying the old one forward.

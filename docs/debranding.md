# Debranding

VyOS's rolling sources may be built and the result redistributed, provided the VyOS name and logo are not used for it. `LICENSE.artwork` in vyos-build puts it as replacing "all artwork files that contain the VyOS logo and all end-user-visible mentions of the VyOS name", and the rolling EULA says the same about the marks. This file is the list of what that means for barerouter: every surface that is changed, and every mention that is deliberately kept.

## How it is done

Two steps, both of which refuse to continue when an edit does not match as often as it expects:

- `hack/debrand-tree.py` edits the vyos-build checkout before live-build runs, because anything replaced after the squashfs is made would still be in the layer that ships.
- `debrand/chroot/99-barerouter-debrand.chroot` runs inside the live-build chroot after every package is installed and rewrites the text vyos-1x put there. `make test-hook` runs it against the vyos-1x package the mirror serves now, so a string that moved upstream shows up in seconds.

`hack/check-branding.sh` then reads the finished ISO independently of both and fails on any VyOS mention left on a user-visible surface. It was run against the vanilla VyOS Stream 2026.03 ISO to confirm it reports the branding there.

## Replaced

| Surface | Where | Becomes |
| --- | --- | --- |
| `os-release` `NAME` and `PRETTY_NAME` | `scripts/image-build/build-vyos-image` | `BareRouter` |
| ISO application and volume label | same | `BareRouter` |
| isolinux menu titles | same, and `includes.binary/isolinux/menu.cfg` | `BareRouter` |
| hostname of the live system | same, `--bootappend-live` | `barerouter` |
| boot splash, isolinux and GRUB | `includes.binary/isolinux/splash.png`, `bootloaders/grub-pc/splash.png` | a plain dark image of the same size |
| website, support, bug tracker, documentation and news URLs | `data/defaults.toml`, shown in `os-release`, `version.json` and the login banner | this repository |
| the rolling EULA | `data/build-types/development.toml` | removed; `debrand/NOTICE.image` is placed at `/usr/share/vyos/EULA` so `show license` still works |
| login banner and pre-login issue | `templates/login/*.j2`, `conf_mode/system_login_banner.py` | `BareRouter` |
| `show version` | `op_mode/version.py` | `BareRouter` |
| crash report and its instructions | `vyos/airbag.py` | `BareRouter`, pointing at this repository's issues and releases |
| boot and shutdown messages | `init/vyos-router` | `BareRouter` |
| HTTP API title and greeting | `services/vyos-http-api-server` | `BareRouter` |
| EFI bootloader id | `vyos/system/grub.py`, and `scripts/image-build/raw_image.py`, which installs GRUB on the KubeVirt disk through a vyos-1x checkout of its own | `BareRouter` |
| GRUB compat menu entry and header | `templates/grub/grub_compat.j2` | `BareRouter` |
| default hostname | `config.boot.default`, and `conf_mode/system_host-name.py` for a configuration that sets none | `barerouter` |
| default login and its password, both upstream's own name | `config.boot.default`; the account the boot loader recreates when the configuration fails to load (`vyos-boot-config-loader.py`); the prompts and config path of `install image` (`op_mode/image_installer.py`); the default password the CLI warns about (`vyos/utils/auth.py`) | `admin`, password `admin` |
| default NTP servers, run by the VyOS project for its own images | `config.boot.default` | `0.pool.ntp.org` to `2.pool.ntp.org` |
| CLI help text | `help:` lines of the `node.def` templates, `help`/`help_text` in `op_cache.json`, `op_cache.py` and `reftree.cache` | `BareRouter` |
| the message `configure` prints to root | `configure/node.def`, `op_cache.json`, `op_cache.py` | no product name |
| default certificate organization | `default_value` in `reftree.cache` and `vyos_1x_cache.py` | `BareRouter` |
| SBOM document root | `hack/rebrand-sbom.py`, after the build | `BareRouter`, supplied by barerouter |

## Kept, and why

- **Copyright and licence notices.** `Copyright VyOS maintainers and contributors` in source headers and the `Copyright:` line of `show version` are authorship statements the GPL requires to be preserved. Removing them would be the violation.
- **Code comments and docstrings.** Nobody using the router sees them.
- **Identifiers.** `ID=vyos` in `os-release`, the `vyos-1x` and other package names, `/usr/libexec/vyos` and every other path, upstream's own systemd units such as `vyos-router.service`, and Python module names. They are how the code finds itself; renaming them is a fork of the code rather than a change of branding, and it breaks every script and config that refers to them. The default login is not on this list although it is an identifier too: a user types it at every console prompt.
- **Component suppliers in the SBOM.** A package VyOS wrote is attributed to VyOS whoever ships it.
- **Package origin.** `/etc/apt/sources.list.d` still points at the VyOS rolling repository, because that is where the packages came from.

## When the build fails on an edit

An edit that stopped matching means upstream reworded or moved the string. Find the new wording in the vyos-build or vyos-1x commit that changed it, update the edit and its expected count, and run `make test-hook` before the full build. Do not relax an exact count to "at least one" to get past it: the count is what makes a leak fail the build instead of shipping. The CLI help sweeps are the one place that requires only at least one hit, because they cover thousands of templates whose number moves with every upstream change; `make check` is what catches a help string they missed.

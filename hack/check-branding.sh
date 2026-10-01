#!/usr/bin/env bash
# Fail when a built image still shows the VyOS name or logo where a user sees
# it. The edits in debrand-tree.py and the chroot hook each check the strings
# they know about; this reads the finished ISO instead, so a string upstream
# added to the same surfaces is caught too.
#
# Copyright and licence notices and code comments are not branding and are
# skipped; docs/debranding.md explains the line.
set -euo pipefail

ISO="${1:?usage: check-branding.sh <iso>}"
: "${VYOS_BUILD_REF:?}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail=0
report() {
  echo "E: $*" >&2
  fail=1
}

label="$(file -b "$ISO")"
case "$label" in
  *"'BareRouter'"*) ;;
  *) report "ISO volume label is not BareRouter: ${label}" ;;
esac

7z x -y -bso0 -bsp0 -o"${WORK}/iso" "$ISO" >/dev/null
squashfs="${WORK}/iso/live/filesystem.squashfs"
if [ ! -f "$squashfs" ]; then
  echo "E: no live/filesystem.squashfs in ${ISO}; nothing to check" >&2
  exit 1
fi

# The boot menus of the live ISO.
while IFS= read -r hit; do
  report "boot menu: ${hit#"${WORK}/iso/"}"
done < <(grep -rn 'VyOS' "${WORK}/iso/isolinux" "${WORK}/iso/boot/grub" --include='*.cfg' || true)

# The logo artwork, compared with the files at the pinned upstream commit.
for pair in \
  "isolinux/splash.png data/live-build-config/includes.binary/isolinux/splash.png" \
  "boot/grub/splash.png data/live-build-config/bootloaders/grub-pc/splash.png"; do
  read -r shipped_rel upstream_rel <<<"$pair"
  [ -f "${WORK}/iso/${shipped_rel}" ] || continue
  upstream="$(curl -fsSL "https://raw.githubusercontent.com/vyos/vyos-build/${VYOS_BUILD_REF}/${upstream_rel}" | sha256sum | cut -d' ' -f1)"
  shipped="$(sha256sum "${WORK}/iso/${shipped_rel}" | cut -d' ' -f1)"
  if [ "$upstream" = "$shipped" ]; then
    report "${shipped_rel} is the upstream VyOS logo"
  fi
done

# 7z reports symlinks and device nodes it cannot recreate as errors; the files
# checked below are regular files, and their absence is checked separately.
7z x -y -bso0 -bsp0 -o"${WORK}/fs" "$squashfs" >/dev/null 2>&1 || true
FS="${WORK}/fs"
if [ ! -d "${FS}/usr/share/vyos" ]; then
  echo "E: the squashfs did not unpack; nothing to check" >&2
  exit 1
fi

# build-vyos-image writes /etc/os-release; usr/lib/os-release is Debian's own
# from base-files and names Debian.
grep -q '^NAME="BareRouter"$' "${FS}/etc/os-release" || report "etc/os-release does not name BareRouter"

surfaces=(
  etc/os-release
  usr/lib/os-release
  usr/share/vyos/EULA
  usr/share/vyos/version.json
  usr/share/vyos/config.boot.default
  usr/libexec/vyos/op_mode/version.py
  usr/libexec/vyos/conf_mode/system_login_banner.py
  usr/libexec/vyos/services/vyos-http-api-server
  usr/libexec/vyos/init/vyos-router
  usr/lib/python3/dist-packages/vyos/airbag.py
)
while IFS= read -r tpl; do
  surfaces+=("${tpl#"${FS}/"}")
done < <(find "${FS}/usr/share/vyos/templates/login" "${FS}/usr/share/vyos/templates/grub" -name '*.j2' 2>/dev/null)
checked=0
for rel in "${surfaces[@]}"; do
  path="${FS}/${rel}"
  [ -f "$path" ] || continue
  checked=$((checked + 1))
  while IFS= read -r hit; do
    report "${rel}:${hit}"
  done < <(grep -n 'VyOS' "$path" | grep -v -E '^[0-9]+:[[:space:]]*#|Copyright' || true)
done
if [ "$checked" -lt 10 ]; then
  report "only ${checked} user-visible files found; the image layout has moved and this check reads nothing"
fi

help_hits="$(grep -rh '^help:.*VyOS' "${FS}/opt/vyatta/share" 2>/dev/null | sort -u || true)"
if [ -n "$help_hits" ]; then
  while IFS= read -r hit; do
    report "CLI help: ${hit}"
  done <<<"$help_hits"
fi

# The default login. A user types it at every console prompt, so upstream's name
# for it is a surface like the banner, and so is the password named after it.
login_files=(
  usr/share/vyos/config.boot.default
  usr/libexec/vyos/vyos-boot-config-loader.py
  usr/libexec/vyos/op_mode/image_installer.py
  usr/lib/python3/dist-packages/vyos/utils/auth.py
)
for rel in "${login_files[@]}"; do
  if [ ! -f "${FS}/${rel}" ]; then
    report "${rel} is not in the image; the default-login check reads nothing there"
    continue
  fi
  while IFS= read -r hit; do
    report "default login: ${rel}:${hit}"
  done < <(grep -n -E "user vyos \{|\"vyos\" user|'user', 'vyos'|DEFAULT_PASSWORD: str = 'vyos'|'vyos' in users" "${FS}/${rel}" || true)
done
grep -q -E '^[[:space:]]+user admin \{$' "${FS}/usr/share/vyos/config.boot.default" ||
  report "usr/share/vyos/config.boot.default: the default login is not admin"

# The generated caches the CLI reads help, completion and defaults from.
while IFS= read -r hit; do
  report "cache: ${hit}"
done < <(python3 - "$FS" <<'EOF'
import json
import os
import re
import sys

fs = sys.argv[1]


def walk(o, rel):
    if isinstance(o, dict):
        for k, v in o.items():
            if isinstance(v, str):
                if "VyOS" in v and k in ("help", "help_text", "default_value", "command"):
                    print(f"{rel}: {k}: {v[:80]!r}")
            else:
                walk(v, rel)
    elif isinstance(o, list):
        for v in o:
            walk(v, rel)


for rel in ("usr/share/vyos/op_cache.json", "usr/share/vyos/reftree.cache"):
    path = os.path.join(fs, rel)
    if os.path.exists(path):
        with open(path, encoding="utf-8") as f:
            walk(json.load(f), rel)
    else:
        print(f"W: {rel} is not in this image", file=sys.stderr)

literal = re.compile(r"'(help_text|default_value)': '((?:[^'\\]|\\.)*VyOS(?:[^'\\]|\\.)*)'")
for rel in (
    "usr/lib/python3/dist-packages/vyos/xml_ref/op_cache.py",
    "usr/lib/python3/dist-packages/vyos/xml_ref/pkg_cache/vyos_1x_cache.py",
):
    path = os.path.join(fs, rel)
    if not os.path.exists(path):
        print(f"W: {rel} is not in this image", file=sys.stderr)
        continue
    with open(path, encoding="utf-8") as f:
        for m in literal.finditer(f.read()):
            print(f"{rel}: {m.group(1)}: {m.group(2)[:80]!r}")
EOF
)

while IFS= read -r art; do
  report "artwork named after VyOS: ${art#"${FS}/"}"
done < <(find "$FS" -iname '*vyos*' \( -iname '*.png' -o -iname '*.svg' -o -iname '*.jpg' \) -print)

if [ "$fail" -ne 0 ]; then
  echo "E: branding check failed for ${ISO}" >&2
  exit 1
fi
echo "I: no VyOS branding found in ${checked} user-visible files, the CLI help and the boot media of ${ISO}"

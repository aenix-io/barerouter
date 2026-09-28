#!/usr/bin/env bash
# Fail when the KubeVirt disk shows the VyOS name where a user sees it.
#
# check-branding.sh reads the ISO. The disk adds what build-vyos-image --reuse-iso
# writes when it installs that ISO, the EFI boot entry and the GRUB configuration,
# and what inject-appliance.sh places in the overlay, none of which the ISO check
# can see. Comments and copyright notices are skipped, as there.
set -euo pipefail

DISK="${1:?usage: check-disk.sh <qcow2>}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail=0
report() {
  echo "E: $*" >&2
  fail=1
}

# The disk is GPT: BIOS boot, the EFI system partition, then the root.
7z e -y -bso0 -bsp0 -o"$WORK" "$DISK" 1.img 2.img >/dev/null
for part in 1.img 2.img; do
  if [ ! -s "${WORK}/${part}" ]; then
    echo "E: partition ${part} is not in ${DISK}; the disk layout has moved and nothing is checked" >&2
    exit 1
  fi
done

efi_dirs="$(7z l -slt "${WORK}/1.img" | sed -n 's|^Path = EFI/\([^/]*\)$|\1|p' | sort -u)"
if [ -z "$efi_dirs" ]; then
  report "no EFI/ directory on the EFI system partition"
fi
while IFS= read -r d; do
  [ -n "$d" ] || continue
  case "$d" in
    [Vv][Yy][Oo][Ss]) report "EFI boot entry directory is named ${d}" ;;
  esac
done <<<"$efi_dirs"

# The GRUB configuration and the overlay the appliance writes into.
7z x -y -bso0 -bsp0 -o"${WORK}/root" "${WORK}/2.img" 'boot/grub/*' -r >/dev/null 2>&1 || true
7z x -y -bso0 -bsp0 -o"${WORK}/root" "${WORK}/2.img" 'boot/*/rw/*' -r >/dev/null 2>&1 || true
checked=0
while IFS= read -r f; do
  checked=$((checked + 1))
  while IFS= read -r hit; do
    report "${f#"${WORK}/root/"}:${hit}"
  done < <(grep -n 'VyOS' "$f" | grep -v -E '^[0-9]+:[[:space:]]*#|Copyright' || true)
done < <(find "${WORK}/root/boot" -type f \( -name '*.cfg' -o -name '*.service' -o -name '*.sh' -o -name 'config.boot*' \) 2>/dev/null)
if [ "$checked" -lt 3 ]; then
  report "only ${checked} GRUB and appliance files found on the root partition; nothing meaningful was checked"
fi

if [ "$fail" -ne 0 ]; then
  echo "E: branding check failed for ${DISK}" >&2
  exit 1
fi
echo "I: no VyOS branding found in ${checked} GRUB and appliance files or the EFI entries of ${DISK}"

#!/usr/bin/env bash
# The two ways the KubeVirt disk's bootloader lock can fail open silently.
#
# The appliance sets GRUB `superusers` so the serial console cannot reach a root
# shell through the "Password reset" boot entry, which runs
# init=/usr/libexec/vyos/system/standalone_root_pw_reset. kubevirt/inject-appliance.sh
# installs the lock, and neither half of it fails loudly on its own:
#
# - The drop-in name. Upstream's grub_main.j2 sources only
#   `${prefix}/grub.cfg.d/*-autoload.cfg`; a drop-in named anything else is
#   written, survives every check, and is never read.
# - The menuentry regex. `superusers` restricts every menu entry not marked
#   --unrestricted, so the normal-boot entry has to carry the flag or the VM
#   stops at a password prompt nobody can answer. That entry's line shape is
#   upstream's (grub_vyos_version.j2), so the regex is the part that rots. It
#   lives in its own .sed file so this test runs the program the build runs.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SEDPROG="${REPO_ROOT}/kubevirt/grub-unrestrict.sed"
INJECT="${REPO_ROOT}/kubevirt/inject-appliance.sh"
failed=0

expect() {
  local name="$1" got="$2" want="$3"
  if [ "$got" != "$want" ]; then
    echo "FAIL: ${name}" >&2
    echo "  got:  ${got}" >&2
    echo "  want: ${want}" >&2
    failed=1
  else
    echo "ok: ${name}"
  fi
}

# The shape raw_image.py writes into grub.cfg.d/vyos-versions/*.cfg.
expect "a generated version menu entry is marked unrestricted" \
  "$(printf 'menuentry "0.1.0" --id vyos-1A2B3C4D {\n' | sed -E -f "$SEDPROG")" \
  'menuentry "0.1.0" --id vyos-1A2B3C4D --unrestricted {'

# grub_vyos_version.j2 itself, patched so a structure migration re-renders the
# entry with the flag intact.
expect "the upstream template line is marked unrestricted" \
  "$(printf 'menuentry "{{ version_name }}" --id {{ version_uuid }} {\n' | sed -E -f "$SEDPROG")" \
  'menuentry "{{ version_name }}" --id {{ version_uuid }} --unrestricted {'

untouched='menuentry "already" --id vyos-DEADBEEF --unrestricted {
    linux /boot/0.1.0/vmlinuz console=ttyS0,115200
}
submenu "Boot options" {'
expect "an entry that already has the flag, and other lines, are left alone" \
  "$(printf '%s\n' "$untouched" | sed -E -f "$SEDPROG")" \
  "$untouched"

dropins="$(grep -oE '(grub\.cfg\.d|GRUB_CFG_D\})/[A-Za-z0-9._-]+\.cfg' "$INJECT" | sed 's|.*/||' | sort -u || true)"
if [ -z "$dropins" ]; then
  echo "FAIL: no GRUB drop-in filename in ${INJECT}; has the bootloader lock been removed?" >&2
  failed=1
fi
for f in $dropins; do
  case "$f" in
    *-autoload.cfg) echo "ok: GRUB drop-in ${f} is one grub.cfg sources" ;;
    *)
      echo "FAIL: GRUB drop-in ${f} is never read; grub.cfg sources only *-autoload.cfg" >&2
      failed=1
      ;;
  esac
done

exit "$failed"

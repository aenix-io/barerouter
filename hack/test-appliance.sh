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

# The diagnostics emitter. Its install path is part of the disk's interface
# (docs/kubevirt.md): a consumer's cron entry names it, and nothing at runtime
# says anything when the two disagree. cron adds rules of its own that fail just
# as silently, so they are pinned here against the seed that does the install.
SEED="${REPO_ROOT}/kubevirt/overlay/vyos-appliance-seed.sh"
EMITTER=/usr/local/sbin/cozy-guest-diag.sh
installed="$(grep -oE 'install -m 0755 [^ ]+ (/[^ ]+)' "$SEED" | head -1 | awk '{print $NF}')"
# Not under /config: vyos-router mounts the persistent configuration over it
# while it starts, after the seed has run, so a file there is gone before cron
# ever reads it.
expect "the seed installs the emitter at the documented path, outside /config" "$installed" "$EMITTER"

cronfile="$(grep -oE '/etc/cron\.d/[A-Za-z0-9._-]+' "$SEED" | head -1)"
case "${cronfile##*/}" in
  "") echo "FAIL: the seed installs no /etc/cron.d entry" >&2; failed=1 ;;
  *.*) echo "FAIL: the seed installs cron.d entry ${cronfile##*/}, and cron ignores a name with a dot" >&2; failed=1 ;;
  *) echo "ok: cron.d entry ${cronfile##*/} has a name cron reads" ;;
esac
if [ -n "$cronfile" ] && grep -qE "install -m 0644 [^ ]+ ${cronfile}" "$SEED"; then
  echo "ok: the cron.d entry is installed 0644, which cron requires"
else
  echo "FAIL: the seed does not install ${cronfile:-its cron.d entry} mode 0644" >&2
  failed=1
fi
# A logged success that did not check the install is how a shadowed path once
# survived every run.
if grep -q 'if install -m 0755' "$SEED"; then
  echo "ok: the seed checks the emitter install before reporting it"
else
  echo "FAIL: the seed reports the emitter install without checking it" >&2
  failed=1
fi

exit "$failed"

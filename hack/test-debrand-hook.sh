#!/usr/bin/env bash
# Run the chroot hook against the vyos-1x package the rolling mirror serves
# now, which is the one the next build will install. A string upstream moved
# fails here in seconds rather than an hour into live-build.
set -euo pipefail

: "${VYOS_MIRROR:?}" "${ARCH:?}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

deb_path="$(curl -fsSL "${VYOS_MIRROR}/dists/rolling/main/binary-${ARCH}/Packages.gz" \
  | gunzip | awk '/^Package: vyos-1x$/ {p=1} p && !done && /^Filename: / {print $2; done=1}')"
if [ -z "$deb_path" ]; then
  echo "E: vyos-1x is not in the ${ARCH} index of ${VYOS_MIRROR}" >&2
  exit 1
fi
echo "I: ${deb_path}"
curl -fsSL -o "${WORK}/vyos-1x.deb" "${VYOS_MIRROR}/${deb_path}"

mkdir -p "${WORK}/root"
(cd "$WORK" && ar x vyos-1x.deb)
tar -xf "$(find "$WORK" -maxdepth 1 -name 'data.tar.*' -print -quit)" -C "${WORK}/root"

mkdir -p "${WORK}/root/usr/share/vyos"
cp "${REPO_ROOT}/debrand/NOTICE.image" "${WORK}/root/usr/share/vyos/EULA"

DEBRAND_ROOT="${WORK}/root" python3 "${REPO_ROOT}/debrand/chroot/99-barerouter-debrand.chroot"

#!/usr/bin/env bash
# Fail before a build when the pinned vyos-build ref asks for a kernel the
# rolling mirror no longer serves. The mirror keeps only the current kernel, so
# this is the usual reason a build that worked last month stops working, and
# live-build reports it an hour in as `E: Unable to locate package`.
set -euo pipefail

: "${VYOS_BUILD_REF:?}" "${VYOS_MIRROR:?}" "${ARCH:?}"

want="$(curl -fsSL "https://raw.githubusercontent.com/vyos/vyos-build/${VYOS_BUILD_REF}/data/defaults.toml" \
  | sed -n 's/^kernel_version *= *"\(.*\)"$/\1/p')"
if [ -z "$want" ]; then
  echo "E: no kernel_version in data/defaults.toml at ${VYOS_BUILD_REF}" >&2
  exit 1
fi

have="$(curl -fsSL "${VYOS_MIRROR}/dists/rolling/main/binary-${ARCH}/Packages.gz" \
  | gunzip | sed -n 's/^Package: linux-image-\([0-9.]*\)-vyos$/\1/p' | sort -u)"

if ! printf '%s\n' "$have" | grep -qx "$want"; then
  echo "E: vyos-build ${VYOS_BUILD_REF} wants kernel ${want}; the mirror serves: $(tr '\n' ' ' <<<"$have")" >&2
  echo "E: move VYOS_BUILD_REF to a commit whose kernel_version the mirror has (docs/releasing.md)" >&2
  exit 1
fi
echo "I: kernel ${want} is on the mirror"

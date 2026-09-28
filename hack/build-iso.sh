#!/usr/bin/env bash
# Build a debranded ISO from the pinned vyos-build tree.
#
# The tree is cloned fresh into $OUT/vyos-build, edited by debrand-tree.py, and
# built by build-vyos-image inside the pinned vyos-build container. Debranding
# has to happen before live-build runs: files replaced afterwards would still
# sit in the lower layer of the squashfs, and that layer is what is shipped.
set -euo pipefail

: "${VYOS_BUILD_IMAGE:?}" "${VYOS_BUILD_REF:?}" "${VYOS_MIRROR:?}" "${VERSION:?}" "${ARCH:?}" "${OUT:?}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${REPO_ROOT}/${OUT}/vyos-build"
DEST="${REPO_ROOT}/${OUT}"
NAME="barerouter-${VERSION}-${ARCH}"

mkdir -p "$DEST"
if [ ! -d "${WORK}/.git" ]; then
  rm -rf "$WORK"
  git clone --filter=blob:none https://github.com/vyos/vyos-build.git "$WORK"
fi
git -C "$WORK" fetch --depth 1 origin "$VYOS_BUILD_REF"
# Drop the previous build's debranding edits first, or a checkout of a
# different pin refuses to overwrite them.
git -C "$WORK" reset --hard --quiet
git -C "$WORK" clean -xdfq
# A branch rather than a detached HEAD: build-vyos-image reads the commit and
# branch for version.json and `show version`, and gets neither when detached.
git -C "$WORK" checkout -q -B rolling "$VYOS_BUILD_REF"

python3 "${REPO_ROOT}/hack/debrand-tree.py" "$WORK" "$REPO_ROOT"

# build-vyos-image generates an SBOM with syft, which the container image does
# not always carry. Pinned by checksum so a release is not built with whatever
# the download happens to serve.
SYFT_VERSION=1.49.0
SYFT_SHA256=7aa2f03ee92739cf643279ba3990548b9925d4e22cae13f46831ee62821147fe

docker run --rm -i \
  --privileged \
  -v /dev:/dev \
  -v "${WORK}:/vyos" \
  -w /vyos \
  "$VYOS_BUILD_IMAGE" \
  bash -c '
    set -euo pipefail
    if ! command -v syft >/dev/null; then
      curl -fsSL -o /tmp/syft.tgz "https://github.com/anchore/syft/releases/download/v$4/syft_$4_linux_amd64.tar.gz"
      echo "$5  /tmp/syft.tgz" | sha256sum -c -
      sudo tar -xzf /tmp/syft.tgz -C /usr/local/bin syft
    fi
    sudo ./build-vyos-image \
      --architecture "$1" \
      --build-type development \
      --build-by "barerouter" \
      --vyos-mirror "$3" \
      --version "$2" \
      generic
    sudo chown -R "$(id -u):$(id -g)" .
    # The package list the source manifest is built from. It is read from the
    # dpkg database inside the squashfs that ships: the chroot live-build
    # leaves behind lists fewer than half of the installed packages.
    unsquashfs -q -n -d /tmp/shipped build/binary/live/filesystem.squashfs var/lib/dpkg/status
    dpkg-query --admindir=/tmp/shipped/var/lib/dpkg -W \
      -f "\${Package}\t\${Version}\t\${source:Package}\t\${source:Version}\n" \
      | sort > build/packages.tsv
  ' -- "$ARCH" "$VERSION" "$VYOS_MIRROR" "$SYFT_VERSION" "$SYFT_SHA256"

ISO_SRC="$(find "$WORK" -maxdepth 2 -name "vyos-${VERSION}-generic-${ARCH}.iso" -print -quit)"
if [ -z "$ISO_SRC" ]; then
  echo "E: build-vyos-image produced no ISO under ${WORK}" >&2
  exit 1
fi
base="${ISO_SRC%.iso}"
mv -f "$ISO_SRC" "${DEST}/${NAME}.iso"
for ext in cdx.json spdx.json; do
  if [ -f "${base}.${ext}" ]; then
    python3 "${REPO_ROOT}/hack/rebrand-sbom.py" "${base}.${ext}" "${DEST}/${NAME}.${ext}" "$VERSION"
  fi
done

if [ ! -s "${WORK}/build/packages.tsv" ]; then
  echo "E: no package list at ${WORK}/build/packages.tsv; the source bundle cannot be built" >&2
  exit 1
fi
cp "${WORK}/build/packages.tsv" "${DEST}/${NAME}.packages.tsv"
python3 "${REPO_ROOT}/hack/source-manifest.py" "${DEST}/${NAME}.packages.tsv" "${DEST}/${NAME}.sources.md" "$VERSION"

(cd "$DEST" && sha256sum "${NAME}.iso" > "${NAME}.iso.sha256")
echo "I: ${DEST}/${NAME}.iso"

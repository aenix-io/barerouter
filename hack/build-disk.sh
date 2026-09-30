#!/usr/bin/env bash
# Build the KubeVirt disk from the ISO hack/build-iso.sh produced.
#
# build-vyos-image --reuse-iso installs the ISO onto a disk without running
# live-build again, and kubevirt/inject-appliance.sh then adds the appliance:
# the NoCloud config seed, the guest agent, the config report and the locked
# bootloader. The ISO itself is left untouched and is published as it is.
set -euo pipefail

: "${VYOS_BUILD_IMAGE:?}" "${VERSION:?}" "${ARCH:?}" "${OUT:?}"
: "${KUBEVIRT_DEB_URLS:?}" "${KUBEVIRT_DEB_SHA256:?}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${REPO_ROOT}/${OUT}/vyos-build"
DEST="${REPO_ROOT}/${OUT}"
ISO="${DEST}/barerouter-${VERSION}-${ARCH}.iso"
NAME="barerouter-${VERSION}-kubevirt-${ARCH}"

if [ ! -f "$ISO" ]; then
  echo "E: ${ISO} is missing; run make iso first" >&2
  exit 1
fi
if [ ! -d "${WORK}/.git" ]; then
  echo "E: ${WORK} is missing; the disk is built from the tree the ISO was built from" >&2
  exit 1
fi

# The debs are fetched by URL and checked by digest, so a package that left the
# Debian pool fails here instead of being replaced by whatever is there now.
DEBS="${WORK}/kubevirt-debs"
rm -rf "$DEBS" "${WORK}/build"
mkdir -p "$DEBS"
read -r -a urls <<<"$KUBEVIRT_DEB_URLS"
read -r -a sums <<<"$KUBEVIRT_DEB_SHA256"
if [ "${#urls[@]}" -ne "${#sums[@]}" ]; then
  echo "E: KUBEVIRT_DEB_URLS and KUBEVIRT_DEB_SHA256 have different lengths" >&2
  exit 1
fi
for i in "${!urls[@]}"; do
  deb="${DEBS}/$(basename "${urls[$i]}")"
  curl -fsSL --retry 3 -o "$deb" "${urls[$i]}"
  echo "${sums[$i]}  ${deb}" | sha256sum -c -
done

cp "${REPO_ROOT}/kubevirt/flavor.toml" "${WORK}/data/build-flavors/kubevirt.toml"
cp "$ISO" "${WORK}/barerouter.iso"
rm -rf "${WORK}/kubevirt"
mkdir -p "${WORK}/kubevirt"
cp -r "${REPO_ROOT}/kubevirt/overlay" "${REPO_ROOT}/kubevirt/inject-appliance.sh" "${REPO_ROOT}/kubevirt/grub-unrestrict.sed" "${WORK}/kubevirt/"

docker run --rm -i \
  --privileged \
  -v /dev:/dev \
  -v "${WORK}:/vyos" \
  -w /vyos \
  "$VYOS_BUILD_IMAGE" \
  bash -c '
    set -euo pipefail
    sudo ./build-vyos-image \
      --architecture "$1" \
      --build-type development \
      --version "$2" \
      --reuse-iso /vyos/barerouter.iso \
      kubevirt
    raw="$(find /vyos/build -maxdepth 1 -name "*.raw" -print -quit)"
    [ -n "$raw" ] || { echo "E: build-vyos-image produced no raw image" >&2; exit 1; }
    sudo /vyos/kubevirt/inject-appliance.sh "$raw" /vyos/kubevirt/overlay /vyos/kubevirt-debs
    sudo qemu-img convert -f raw -O qcow2 "$raw" /vyos/build/kubevirt.qcow2
    sudo chown -R "$(id -u):$(id -g)" .
    # The packages the disk adds to the ISO, for its sources manifest.
    for deb in /vyos/kubevirt-debs/*.deb; do
      dpkg-deb --show --showformat "\${Package}\t\${Version}\t\${source:Package}\t\${source:Version}\n" "$deb"
    done > /vyos/build/kubevirt-packages.tsv
  ' -- "$ARCH" "$VERSION"

mv -f "${WORK}/build/kubevirt.qcow2" "${DEST}/${NAME}.qcow2"
python3 "${REPO_ROOT}/hack/disk-manifest.py" \
  "${DEST}/barerouter-${VERSION}-${ARCH}.sources.md" \
  "${WORK}/build/kubevirt-packages.tsv" \
  "${DEST}/${NAME}.sources.md" \
  "$VERSION"
(cd "$DEST" && sha256sum "${NAME}.qcow2" > "${NAME}.qcow2.sha256")
echo "I: ${DEST}/${NAME}.qcow2"

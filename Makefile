SHELL := /usr/bin/env bash

# ── Pinned build inputs ─────────────────────────────────────────────────────
# The upstream rolling apt repository keeps only the current kernel, so a
# VYOS_BUILD_REF whose data/defaults.toml names an older kernel stops building
# the day that kernel leaves the mirror. `make verify-pin` compares the two
# before a build is attempted; docs/releasing.md has the bump procedure.
VYOS_BUILD_IMAGE ?= vyos/vyos-build:rolling@sha256:482461a415e2fa05b5b1753bfa5490f6fd349585b88be6c23314867b770bb1a0
VYOS_BUILD_REF   ?= d1394291337eb0274e026145a4c6245207c1c71d
VYOS_MIRROR      ?= https://packages.vyos.net/repositories/rolling

# What the KubeVirt disk installs on top of the ISO. Neither is in the VyOS
# repository, so both come from the Debian pool, pinned by digest. A `+deb12uN`
# version is a security-update stream and leaves the pool once superseded; the
# digest check then fails the build rather than taking whatever is there.
KUBEVIRT_DEB_URLS ?= \
  http://deb.debian.org/debian/pool/main/libu/liburing/liburing2_2.3-3_amd64.deb \
  http://deb.debian.org/debian/pool/main/q/qemu/qemu-guest-agent_7.2+dfsg-7+deb12u18+b3_amd64.deb
KUBEVIRT_DEB_SHA256 ?= \
  c23077e3640e6cb4b819c134b3f41d5cf21b3edac099654b1b4142c3069a39a3 \
  ffdc7852decec129a903acf1b9ac083859cabee60e7b24477b652f36e228e808

VERSION ?= 0.0.0-dev
ARCH    ?= amd64
OUT     ?= _out

ISO        := $(OUT)/barerouter-$(VERSION)-$(ARCH).iso
DISK       := $(OUT)/barerouter-$(VERSION)-kubevirt-$(ARCH).qcow2
DISK_IMAGE ?= ghcr.io/aenix-io/barerouter/kubevirt-disk

export VYOS_BUILD_IMAGE VYOS_BUILD_REF VYOS_MIRROR VERSION ARCH OUT KUBEVIRT_DEB_URLS KUBEVIRT_DEB_SHA256

.PHONY: iso disk disk-image check check-disk test-hook verify-pin lint clean

iso: verify-pin test-hook
	hack/build-iso.sh

# Built from the ISO and the tree `make iso` left in $(OUT); run after it.
disk:
	hack/build-disk.sh

# The containerDisk for KubeVirt. PUSH=1 pushes it; without it the image is
# only built, into the local docker.
disk-image:
	docker buildx build -f kubevirt/Dockerfile \
		--build-arg DISK=$(notdir $(DISK)) \
		-t $(DISK_IMAGE):$(VERSION) \
		--metadata-file $(OUT)/disk-image.json \
		$(if $(PUSH),--push,--load) \
		$(OUT)

# The checks unpack the whole image; on a host whose /tmp is a tmpfs that does
# not fit, so they unpack under $(OUT) instead.
check:
	TMPDIR=$(abspath $(OUT)) hack/check-branding.sh $(ISO)

check-disk:
	TMPDIR=$(abspath $(OUT)) hack/check-disk.sh $(DISK)

test-hook:
	hack/test-debrand-hook.sh

verify-pin:
	hack/verify-pin.sh

lint:
	shellcheck hack/*.sh kubevirt/*.sh kubevirt/overlay/*.sh
	python3 -m py_compile hack/*.py debrand/chroot/*.chroot

clean:
	rm -rf $(OUT)

# `make -s print-VYOS_BUILD_REF`, for workflows that need a pin's value.
print-%:
	@echo '$($*)'

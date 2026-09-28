SHELL := /usr/bin/env bash

# ── Pinned build inputs ─────────────────────────────────────────────────────
# The upstream rolling apt repository keeps only the current kernel, so a
# VYOS_BUILD_REF whose data/defaults.toml names an older kernel stops building
# the day that kernel leaves the mirror. `make verify-pin` compares the two
# before a build is attempted; docs/releasing.md has the bump procedure.
VYOS_BUILD_IMAGE ?= vyos/vyos-build:rolling@sha256:482461a415e2fa05b5b1753bfa5490f6fd349585b88be6c23314867b770bb1a0
VYOS_BUILD_REF   ?= d1394291337eb0274e026145a4c6245207c1c71d
VYOS_MIRROR      ?= https://packages.vyos.net/repositories/rolling

VERSION ?= 0.0.0-dev
ARCH    ?= amd64
OUT     ?= _out

ISO := $(OUT)/barerouter-$(VERSION)-$(ARCH).iso

export VYOS_BUILD_IMAGE VYOS_BUILD_REF VYOS_MIRROR VERSION ARCH OUT

.PHONY: iso check test-hook verify-pin lint clean

iso: verify-pin test-hook
	hack/build-iso.sh

check:
	hack/check-branding.sh $(ISO)

test-hook:
	hack/test-debrand-hook.sh

verify-pin:
	hack/verify-pin.sh

lint:
	shellcheck hack/*.sh
	python3 -m py_compile hack/*.py debrand/chroot/*.chroot

clean:
	rm -rf $(OUT)

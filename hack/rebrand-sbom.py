#!/usr/bin/env python3
"""Rewrite the document root of an SBOM that build-vyos-image produced.

Usage: rebrand-sbom.py <input.{cdx,spdx}.json> <output> <version>

build-vyos-image names the operating system VyOS and VyOS Networks as its
supplier. For this image both are false, so the root is rewritten. Component
entries keep the suppliers syft and build-vyos-image assigned: a vyos-1x package
is VyOS's work whoever ships it, and saying otherwise would be the wrong
attribution in the other direction.
"""
import json
import sys

NAME = "BareRouter"
REPO_URL = "https://github.com/cozystack/barerouter"

src, dst, version = sys.argv[1], sys.argv[2], sys.argv[3]
with open(src, encoding="utf-8") as f:
    doc = json.load(f)

if doc.get("bomFormat") == "CycloneDX":
    supplier = {"name": "barerouter", "url": [REPO_URL]}
    meta = doc["metadata"]
    meta["authors"] = [{"name": "barerouter contributors"}]
    meta["supplier"] = supplier
    meta["component"]["name"] = NAME
    meta["component"]["version"] = version
    meta["component"]["supplier"] = supplier
elif "spdxVersion" in doc:
    doc["name"] = f"{NAME}-{version}"
    doc["documentNamespace"] = f"{REPO_URL}/sbom/" + doc["documentNamespace"].rsplit("/", 1)[-1]
    creators = [c for c in doc["creationInfo"]["creators"] if not c.startswith("Organization:")]
    doc["creationInfo"]["creators"] = creators + ["Organization: barerouter contributors"]
    roots = [p for p in doc["packages"] if p.get("SPDXID", "").startswith("SPDXRef-DocumentRoot-")]
    if len(roots) != 1:
        sys.exit(f"E: {src}: expected one document root package, found {len(roots)}")
    roots[0]["name"] = NAME
    roots[0]["versionInfo"] = version
    roots[0]["supplier"] = f"Organization: barerouter ({REPO_URL})"
else:
    sys.exit(f"E: {src}: neither CycloneDX nor SPDX")

with open(dst, "w", encoding="utf-8") as f:
    json.dump(doc, f)

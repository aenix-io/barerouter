#!/usr/bin/env python3
"""Write the sources manifest of the KubeVirt disk.

Usage: disk-manifest.py <iso sources.md> <added packages.tsv> <output.md> <version>

The disk is the ISO installed onto a disk image plus what
kubevirt/inject-appliance.sh adds, so its manifest is the ISO's with a section
for those additions: the Debian packages it installs, and the files and the
upstream template edit that come from this repository at the same tag.
"""
import sys
import urllib.parse

iso_md, added_tsv, out, version = sys.argv[1:5]

with open(iso_md, encoding="utf-8") as f:
    text = f.read()
old_title = f"# Sources for barerouter {version}\n"
if not text.startswith(old_title):
    sys.exit(f"E: {iso_md} does not start with {old_title!r}")
text = f"# Sources for the barerouter {version} KubeVirt disk\n" + text[len(old_title):]

rows = []
with open(added_tsv, encoding="utf-8") as f:
    for line in f:
        pkg, ver, src, srcver = (line.rstrip("\n").split("\t") + ["", "", "", ""])[:4]
        if pkg:
            rows.append((pkg, ver, src or pkg, srcver or ver))
if not rows:
    sys.exit(f"E: {added_tsv} lists no packages; the disk adds at least the guest agent")

lines = [
    "",
    "## Added by the KubeVirt disk",
    "",
    f"The disk is the ISO above installed by `build-vyos-image --reuse-iso`, plus what `kubevirt/inject-appliance.sh` at tag `v{version}` adds: the files under `kubevirt/overlay`, a GRUB superuser file, and an edit of vyos-1x's GRUB template `grub_vyos_version.j2`, which stays under vyos-1x's licence and whose edit is the sed program `kubevirt/grub-unrestrict.sed` in the same tag. It also installs these Debian packages:",
    "",
    "| Source package | Version | Binary package |",
    "| --- | --- | --- |",
]
for pkg, ver, src, srcver in sorted(rows, key=lambda r: r[2]):
    link = f"https://snapshot.debian.org/package/{src}/{urllib.parse.quote(srcver)}/"
    lines.append(f"| [`{src}`]({link}) | `{srcver}` | `{pkg}` `{ver}` |")

with open(out, "w", encoding="utf-8") as f:
    f.write(text.rstrip("\n") + "\n" + "\n".join(lines) + "\n")
print(f"I: {len(rows)} added packages listed in {out}")

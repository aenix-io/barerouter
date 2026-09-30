#!/usr/bin/env python3
"""Remove VyOS branding from a vyos-build checkout before it is built.

Usage: debrand-tree.py <vyos-build checkout> <barerouter repo root>

Every edit has to match exactly the number of times it expects. An upstream
change that moves or rewords a string then fails the build here, instead of
producing an image that quietly carries the name again.

What is changed and what is deliberately left alone is in docs/debranding.md.
"""
import os
import re
import shutil
import struct
import sys
import zlib

NAME = "BareRouter"
REPO_URL = "https://github.com/aenix-io/barerouter"

tree, repo = sys.argv[1], sys.argv[2]


def edit(path, replacements):
    full = os.path.join(tree, path)
    with open(full, encoding="utf-8") as f:
        text = f.read()
    for old, new, count in replacements:
        found = text.count(old)
        if found != count:
            sys.exit(f"E: {path}: expected {count} of {old!r}, found {found}")
        text = text.replace(old, new)
    with open(full, "w", encoding="utf-8") as f:
        f.write(text)


def edit_re(path, pattern, new, count):
    full = os.path.join(tree, path)
    with open(full, encoding="utf-8") as f:
        text = f.read()
    text, found = re.subn(pattern, lambda _: new, text, flags=re.S | re.M)
    if found != count:
        sys.exit(f"E: {path}: expected {count} of /{pattern}/, found {found}")
    with open(full, "w", encoding="utf-8") as f:
        f.write(text)


def png(path, width, height, rgb):
    """Write a solid-colour RGB PNG, the replacement for the logo artwork."""
    row = b"\x00" + bytes(rgb) * width
    raw = row * height

    def chunk(kind, data):
        body = kind + data
        return struct.pack(">I", len(data)) + body + struct.pack(">I", zlib.crc32(body) & 0xFFFFFFFF)

    data = b"\x89PNG\r\n\x1a\n"
    data += chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
    data += chunk(b"IDAT", zlib.compress(raw, 9))
    data += chunk(b"IEND", b"")
    with open(os.path.join(tree, path), "wb") as f:
        f.write(data)


# os-release, the isolinux title of the generated menu, the ISO labels and the
# hostname of the live system.
edit("scripts/image-build/build-vyos-image", [
    ('PRETTY_NAME="VyOS {version}', f'PRETTY_NAME="{NAME} {{version}}', 1),
    ('NAME="VyOS"', f'NAME="{NAME}"', 1),
    ("menu title VyOS {build_config['version']}", f"menu title {NAME} {{build_config['version']}}", 1),
    ('--iso-application "VyOS"', f'--iso-application "{NAME}"', 1),
    ('--iso-volume "VyOS"', f'--iso-volume "{NAME}"', 1),
    ("hostname=vyos username=live", "hostname=barerouter username=live", 1),
])

# The disk build installs GRUB through a vyos-1x checkout of its own rather
# than through the image, so the EFI boot entry takes that checkout's default id
# unless it is passed here.
edit("scripts/image-build/raw_image.py", [
    ("grub.install(con.loop_device, f'/boot/', f'/boot/efi', chroot=con.squash_dir)",
     f"grub.install(con.loop_device, f'/boot/', f'/boot/efi', id='{NAME}', chroot=con.squash_dir)", 1),
])

edit("data/live-build-config/includes.binary/isolinux/menu.cfg", [
    ("menu title VyOS - Boot Menu", f"menu title {NAME} - Boot Menu", 1),
])

# The project URLs end up in os-release, version.json and the login banner.
edit_re("data/defaults.toml", r'^website_url = "[^"\n]*"$', f'website_url = "{REPO_URL}"', 1)
edit_re("data/defaults.toml", r'^support_url = "[^"\n]*"$', f'support_url = "{REPO_URL}/issues"', 1)
edit_re("data/defaults.toml", r'^bugtracker_url = "[^"\n]*"$', f'bugtracker_url = "{REPO_URL}/issues"', 1)
edit_re("data/defaults.toml", r'^documentation_url = "[^"\n]*"$', f'documentation_url = "{REPO_URL}#readme"', 1)
edit_re("data/defaults.toml", r'^project_news_url = "[^"\n]*"$', f'project_news_url = "{REPO_URL}/releases"', 1)

# The rolling EULA is VyOS's statement about VyOS's own binaries. This image is
# not one of them; the chroot hook puts barerouter's notice at the same path so
# `show license` still has something to show.
edit_re(
    "data/build-types/development.toml",
    r"^\[\[includes_chroot\]\]\n  path = 'usr/share/vyos/EULA'\n  data = '''\n.*?\n'''\n",
    "",
    1,
)

# Both splash images are the VyOS logo, which LICENSE.artwork reserves.
for splash in (
    "data/live-build-config/includes.binary/isolinux/splash.png",
    "data/live-build-config/bootloaders/grub-pc/splash.png",
):
    png(splash, 640, 480, (0x1F, 0x23, 0x28))

hook = os.path.join(tree, "data/live-build-config/hooks/live/99-barerouter-debrand.chroot")
shutil.copyfile(os.path.join(repo, "debrand/chroot/99-barerouter-debrand.chroot"), hook)
os.chmod(hook, 0o755)

notice = os.path.join(tree, "data/live-build-config/includes.chroot/usr/share/vyos/EULA")
os.makedirs(os.path.dirname(notice), exist_ok=True)
shutil.copyfile(os.path.join(repo, "debrand/NOTICE.image"), notice)

print("I: vyos-build tree debranded")

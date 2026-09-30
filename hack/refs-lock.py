#!/usr/bin/env python3
"""Resolve every upstream reference the image was built from to a commit.

Usage: refs-lock.py <vyos-build tree> <packages.tsv> <output.tsv>

The release carries the vyos-build tree itself, so the patches, kernel
configuration and build scripts are there as they were. What the tree cannot
say is where a branch or tag pointed on the day of the build: `rolling` moves,
and a tag can be moved. This file records that, one line per reference, so the
corresponding source of a release can still be assembled after upstream has
moved on. It is also the input a full source archive would fetch from.

Columns: kind, name, repository, reference, resolved commit.
"""
import concurrent.futures
import glob
import gzip
import os
import re
import subprocess
import sys
import tomllib
import urllib.request

tree, packages_tsv, out = sys.argv[1], sys.argv[2], sys.argv[3]
mirror = os.environ["VYOS_MIRROR"]
arch = os.environ["ARCH"]

SHA = re.compile(r"[0-9a-f]{7,40}")


def ls_remote(url, ref):
    """The commit a ref names now: the peeled tag, else the tag, else the branch."""
    try:
        res = subprocess.run(
            ["git", "ls-remote", url, f"refs/tags/{ref}", f"refs/tags/{ref}^{{}}", f"refs/heads/{ref}"],
            capture_output=True, text=True, timeout=120, env={**os.environ, "GIT_TERMINAL_PROMPT": "0"},
        )
    except subprocess.TimeoutExpired:
        return None
    if res.returncode != 0:
        return None
    found = dict(reversed(line.split("\t", 1)) for line in res.stdout.splitlines() if "\t" in line)
    for key in (f"refs/tags/{ref}^{{}}", f"refs/tags/{ref}", f"refs/heads/{ref}"):
        if key in found:
            return found[key]
    return None


# Every definition vyos-build builds a package from.
defs = []
for toml in sorted(glob.glob(os.path.join(tree, "scripts/package-build/*/package.toml"))):
    with open(toml, "rb") as f:
        doc = tomllib.load(f)
    for p in doc.get("packages", []):
        defs.append((os.path.basename(os.path.dirname(toml)), p.get("name", ""), p.get("scm_url", ""), str(p.get("commit_id", ""))))
if not defs:
    sys.exit(f"E: no package-build definitions under {tree}; the tree layout has moved")

with open(os.path.join(tree, "data/defaults.toml"), "rb") as f:
    kernel = tomllib.load(f)["kernel_version"]

# VyOS-origin source packages that no definition builds: VyOS's own repositories.
# The index captured when the build started (VYOS_INDEX), so a version the
# repository replaced during the build is still recognised; the mirror only
# when the script runs on its own.
if os.environ.get("VYOS_INDEX"):
    with open(os.environ["VYOS_INDEX"], "rb") as f:
        index = gzip.decompress(f.read()).decode("utf-8", "replace")
else:
    req = urllib.request.Request(
        f"{mirror}/dists/rolling/main/binary-{arch}/Packages.gz",
        headers={"User-Agent": "barerouter-build"},
    )
    with urllib.request.urlopen(req) as r:
        index = gzip.decompress(r.read()).decode("utf-8", "replace")
vyos_bin, name = set(), None
for line in index.splitlines():
    if line.startswith("Package: "):
        name = line.split(": ", 1)[1]
    elif line.startswith("Version: ") and name:
        vyos_bin.add((name, line.split(": ", 1)[1]))
def norm(s):
    """Binary source names and package-build names spell the same project
    differently: node-exporter and node_exporter, vyos-intel-i40e and i40e,
    pkg-nftables and nftables, libyang3 and libyang."""
    s = s.lower().replace("_", "-")
    for prefix in ("vyos-drivers-", "vyos-intel-", "vyos-", "pkg-"):
        if s.startswith(prefix):
            s = s[len(prefix):]
            break
    if s.startswith("shim"):
        return "shim-signed"
    return re.sub(r"\d+$", "", s)


defined = {norm(d[1]) for d in defs} | {norm(d[0]) for d in defs}
own = set()
with open(packages_tsv, encoding="utf-8") as f:
    for line in f:
        pkg, ver, src, _ = (line.rstrip("\n").split("\t") + ["", "", "", ""])[:4]
        src = src or pkg
        if (pkg, ver) in vyos_bin and norm(src) not in defined and src != "linux-upstream":
            own.add(src)

rows = [("kernel", "linux", "https://cdn.kernel.org/pub/linux/kernel/v6.x/", f"linux-{kernel}.tar.xz", "-")]
jobs = {}
with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
    for d, n, url, ref in defs:
        if not url:
            # Built by a script in package-build/<dir>, which fetches its own
            # source; the script is in the tree archive.
            rows.append(("script", n, f"package-build/{d}", "-", "-"))
        elif SHA.fullmatch(ref):
            rows.append(("package-build", n, url, ref, ref))
        else:
            jobs[pool.submit(ls_remote, url, ref)] = ("package-build", n, url, ref)
    for src in sorted(own):
        url = f"https://github.com/vyos/{src}"
        jobs[pool.submit(ls_remote, url, "rolling")] = ("vyos", src, url, "rolling")
    unresolved = []
    for fut in concurrent.futures.as_completed(jobs):
        kind, n, url, ref = jobs[fut]
        sha = fut.result()
        if sha is None:
            if kind == "package-build":
                unresolved.append(f"{n} {url} {ref}")
            rows.append((kind, n, url, ref, "unresolved"))
        else:
            rows.append((kind, n, url, ref, sha))

# A definition whose reference no longer resolves is an input upstream has
# removed; releasing on top of it would publish a lock that points nowhere.
if unresolved:
    sys.exit("E: package-build references that no longer resolve:\n  " + "\n  ".join(sorted(unresolved)))

with open(out, "w", encoding="utf-8") as f:
    f.write("# kind\tname\trepository\treference\tcommit\n")
    for row in sorted(rows):
        f.write("\t".join(row) + "\n")
missing = sum(1 for r in rows if r[4] == "unresolved")
print(f"I: {len(rows)} references locked in {out}; {missing} VyOS repositories not located by name")

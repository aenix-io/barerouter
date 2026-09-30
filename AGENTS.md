# AI Agents Overview

This file provides structured guidance for AI coding assistants and agents working with **barerouter**.

## Activation

**CRITICAL**: When the user asks you to do something that matches the scope of a documented process, you MUST read the corresponding documentation file and follow the instructions exactly as written.

- **Commits, PRs, git operations** (e.g., "create a commit", "make a PR", "fix review comments", "rebase", "cherry-pick")
  - Read: [`contributing.md`](./docs/agents/contributing.md)
  - Action: Read the entire file and follow ALL instructions step-by-step

- **Debranding** (e.g., "the branding check fails", "a VyOS string showed up in the image", "a debrand edit stopped matching after a pin bump")
  - Read: [`debranding.md`](./docs/debranding.md)
  - Action: Read the entire file before touching `hack/debrand-tree.py` or the chroot hook. It lists what is replaced, what is deliberately kept, and why each kept item is not branding

- **Releases and pin bumps** (e.g., "cut a release", "bump vyos-build", "the kernel package is gone from the mirror")
  - Read: [`releasing.md`](./docs/releasing.md)
  - Action: Read the entire file and follow the steps in order. A release without its source bundle is not a release

**Important rules:**
- ✅ **ONLY read the file if the task matches the documented process scope** - do not read files for tasks that don't match their purpose
- ✅ **ALWAYS read the file FIRST** before starting the task (when applicable)
- ✅ **Follow instructions EXACTLY** as written in the documentation
- ❌ **Do NOT assume** you know the process - always check the documentation when the task matches

## Project Overview

**barerouter** builds a router image from the public VyOS rolling sources with the VyOS name, trademarks and logo artwork removed, and publishes it on the release page together with its corresponding source. It is not produced, endorsed or supported by VyOS Inc.

## Quick Reference

### Code Structure
- `Makefile` - pinned build inputs (`VYOS_BUILD_IMAGE`, `VYOS_BUILD_REF`) and entry points
- `hack/verify-pin.sh` - fails early when the pinned vyos-build ref wants a kernel the rolling mirror no longer serves
- `hack/build-iso.sh` - clones vyos-build at the pin, debrands the tree, builds in the vyos-build container
- `hack/debrand-tree.py` - edits the vyos-build checkout: os-release, boot menus, ISO labels, artwork, the EULA include
- `debrand/chroot/99-barerouter-debrand.chroot` - live-build hook that replaces the name in the text vyos-1x installs
- `debrand/NOTICE.image` - the notice the image carries at `/usr/share/vyos/EULA`, which `show license` prints
- `hack/check-branding.sh` - fails when a built ISO still shows the VyOS name or artwork
- `hack/source-manifest.py` - writes where the source of every package in the ISO is
- `hack/build-disk.sh` - installs the ISO onto a disk and adds the KubeVirt appliance from `kubevirt/`
- `kubevirt/` - the appliance layer: NoCloud config seed, config report, guest agent, bootloader lock; its contract is in [`kubevirt.md`](./docs/kubevirt.md)
- `hack/check-disk.sh` - fails when the KubeVirt disk shows the VyOS name in its boot entries, GRUB configuration or appliance files
- `hack/test-appliance.sh` - the two ways the bootloader lock can fail open silently
- `hack/check-commit-trailers.sh` - commit attribution check, the same script as in cozystack

### Conventions
- **Git Commits**: Conventional Commits (`type(scope): description`) with `--signoff`; an agent-assisted commit adds one `Assisted-by: LLM` trailer
- **Comments and commit bodies**: WHY, not a narration of WHAT; see [Review Blockers](./docs/agents/contributing.md#review-blockers-messages-trailers-comments) in contributing.md
- **Prose**: One continuous line per paragraph — see [Prose Formatting](#prose-formatting)
- **Upstream edits must fail loudly**: every replacement in `hack/debrand-tree.py` and the chroot hook states how many times it must match, so an upstream rewording breaks the build instead of shipping the name again

### What NOT to Do
- ❌ Force push to main/master
- ❌ Commit built artifacts from `_out`
- ❌ Credit an AI model or its vendor by name in a commit trailer or in an authorship line in a PR description or comment, or put a `Claude-Session:` trailer or a session URL into any of them
- ❌ Hardwrap a prose paragraph in markdown, a PR body, or an issue comment
- ❌ Ship the VyOS name, trademarks or logo artwork as this image's branding, or name an artifact after VyOS
- ❌ Change a copyright or licence notice in an upstream file; the GPL requires them kept
- ❌ Rename identifiers such as `ID=vyos`, `/usr/libexec/vyos`, package names or the `vyos` user; they are code, not branding
- ❌ Publish an image without the source bundle built from the same run

## Prose Formatting

Markdown files and everything an agent publishes to GitHub — PR bodies, issue bodies, review comments, release notes — use **one continuous line per paragraph**. Renderers collapse a soft line break into a space, so a paragraph hardwrapped at ~80 columns renders exactly like the same paragraph on one line, while breaking narrow viewports, mangling nested list and table rendering, and turning a one-word edit into a diff that reflows the entire block. The renderer decides where the line ends; the author does not.

Line breaks stay wherever they carry meaning: code fences, one item per list line, tables, headings, blockquotes, front matter, and README badge stacks. The rule governs prose paragraphs and nothing else. Commit messages are the deliberate exception — `git log` and the tooling around it still expect a body wrapped at ~72 columns.

This is the rule LLM agents break most often, because an ~80-column wrap is correct in the places they spend most of their time (Go comments, commit bodies) and the reflex leaks from there into documentation and GitHub. Treat it as a mechanical check at the moment of writing, not as a style preference to weigh: before saving a markdown file or passing `--body` to `gh`, look at each paragraph and confirm it is a single line.

The rule applies to prose this repository owns, and to new or changed paragraphs. The upstream trees the build clones into `_out/` are never committed and are not in scope.

`.claude/hooks/md-no-hardwrap.py` enforces the rule for agents that support PreToolUse hooks (registered for Claude Code in `.claude/settings.json`). It refuses a `Write`/`Edit`/`MultiEdit` that would *add* a hardwrapped paragraph, list item or blockquote to a markdown file, and a `gh` command that would publish one to GitHub. A break counts as added only when the edit wrote both lines it joins, so fixing a typo inside a paragraph that was already wrapped goes through, while rewriting the same file as fresh hardwrapped prose does not. Anything it cannot read with certainty it allows — a guard that blocks legitimate work gets switched off, and then it guards nothing — which includes a body the shell assembles at runtime, and a `--body-file` written by the very command being checked, since the file does not exist yet when the hook runs. Agents on other harnesses are held to the same rule without the hook.

`.claude/settings.json` is tracked, so keep personal Claude Code overrides in `.claude/settings.local.json`, which is not.

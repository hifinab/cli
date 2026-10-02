---
title: Set up a workstation
description: Turn a fresh Ubuntu 26.04 machine into a Hifin workstation with hi install, choosing tools from a menu, including Strix Halo GPU support and its verification report.
---

`hi install` opens a menu of the Hifin workstation software on Ubuntu 26.04.
Check what the machine should have: missing tools get installed, and installed
tools you uncheck get removed. Strix Halo machines also get AMD ROCm.

## Before you start

- Ubuntu 26.04. Strix Halo support also needs amd64.
- A user with `sudo`. Run `hi` as that user, not as root.
- About 20 minutes and a network connection.

## Install

On a new machine, install `hi` and run the setup in one command:

```sh
curl -fsSL https://hifin.sh/install.sh | sh -s -- install
```

If `hi` is already installed:

```sh
hi install
```

```text
┃ What should this machine have?
┃ Space toggles, enter continues. Unchecking an installed tool removes it.
┃ > [x] Terminal tools      tmux sessions and the btop system monitor
┃   [x] uv                  Python versions, projects, and tools
┃   [x] GitHub CLI          gh, for repositories, pull requests, and sign-in
┃   [ ] Node.js             node and npm from Ubuntu
┃   [ ] Docker              Docker Engine, Buildx, and Compose
┃   [ ] Podman              rootless containers that run as you, for hi box
┃   [x] Claude Code         Anthropic's coding agent (claude)                    installed
┃   ...
┃   [x] Strix Halo support  AMD ROCm, GPU groups, and amd-debug-tools
┃   [ ] Update everything   upgrade Ubuntu packages and reinstall checked tools
```

Installed tools start checked, as do the terminal tools, uv, and the GitHub
CLI. Strix Halo support starts checked when the machine has a Strix Halo GPU.
The Colab CLI needs uv, and the menu says so if you check one without the
other.

If anything is being installed, it offers to change the machine's hostname;
press Enter to keep it. It then shows what will be installed and removed, asks
for your `sudo` password once, and runs without further questions.

## Without the menu

Scripts and machines without a terminal name the tools instead:

```sh
hi install --list              # tool names, and which are installed
hi install claude codex gh     # install or reinstall just these
hi install --all               # every tool except Strix Halo support
hi install --all strix         # every tool, including Strix Halo support
hi uninstall docker            # remove a tool
```

## Removing tools

Unchecking an installed tool in the menu, or `hi uninstall <tool>`, removes
the program but keeps your settings and sign-ins, such as `~/.claude`,
`~/.codex`, and Docker's images in `/var/lib/docker`. The confirmation lists
what stays. System packages are removed with apt; if other packages depend on
one, apt lists them and asks before removing anything else. Removing NetBird
disconnects the machine from the team network, including SSH sessions over it.
Removing Strix Halo support keeps you in the `render` and `video` groups.

## What gets installed

| Menu item          | Name       | Installs                                                  |
|--------------------|------------|-----------------------------------------------------------|
| Terminal tools     | `terminal` | tmux, btop                                                |
| uv                 | `uv`       | uv                                                        |
| GitHub CLI         | `gh`       | `gh`                                                      |
| Node.js            | `node`     | Node.js, npm                                              |
| Docker             | `docker`   | Docker Engine, Buildx, Docker Compose                     |
| Podman             | `podman`   | Rootless Podman with `crun`, `uidmap`, and `passt`; a subordinate ID range if the user has none |
| Claude Code        | `claude`   | `claude`                                                  |
| Codex CLI          | `codex`    | `codex`                                                   |
| omp                | `omp`      | `omp`                                                     |
| herdr              | `herdr`    | `herdr`                                                   |
| NetBird            | `netbird`  | NetBird client and service                                |
| Colab CLI          | `colab`    | `colab` (needs uv)                                        |
| Hugging Face CLI   | `hf`       | `hf`                                                      |
| Strix Halo support | `strix`    | AMD ROCm 10 for `gfx1151`, amd-debug-tools, `render` and `video` group membership |
| Update everything  | `upgrade`  | All available Ubuntu upgrades; reinstalls checked tools   |

Any install also sets up curl, wget, gnupg, pipx, and wtmpdb, which are never
removed.

The AI coding tools and compute CLIs are installed for your user in
`~/.local/bin`; the rest are system packages. Open a new shell afterwards so
the new `PATH` applies.

## Strix Halo: reboot and verify

Installing Strix Halo support ends with a local report. Checks that need a new login
session are marked `PENDING`:

```text
==> Strix installation report
[PASS] Ubuntu 26.04 amd64
[PASS] ROCm gfx1151 package installed
[PASS] User configured for render and video groups
...
[PENDING] Current session needs a logout or reboot for GPU group access
...
Action: reboot, then run `hi verify strix` for the final hardware check.
```

Reboot, then:

```sh
hi verify strix
```

The final report confirms that ROCm sees the `gfx1151` GPU through both
`amd-smi` and `rocminfo`. The report runs locally and uploads nothing.

## Next

- Join the Hifin network: [NetBird network](/guide/workstation/netbird/)
- Add people to the machine: [Users](/guide/workstation/users/)
- Sign in to compute providers: [Install hi](/guide/install/#sign-in-to-a-compute-provider)

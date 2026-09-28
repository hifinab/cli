---
title: Set up a workstation
description: Turn a fresh Ubuntu 26.04 machine into a Hifin workstation with hi install, including Strix Halo GPU support and its verification report.
---

`hi install` installs the Hifin workstation software on Ubuntu 26.04 in one
unattended pass. `hi install strix` adds AMD ROCm for Strix Halo machines.

## Before you start

- Ubuntu 26.04. The Strix profile also needs amd64.
- A user with `sudo`. Run `hi` as that user, not as root.
- About 20 minutes and a network connection.

## Install

On a new machine, install `hi` and run the setup in one command:

```sh
curl -fsSL https://hifin.sh/install.sh | sh -s -- install strix   # Strix Halo
curl -fsSL https://hifin.sh/install.sh | sh -s -- install         # any other machine
```

If `hi` is already installed:

```sh
hi install          # or: hi install strix
```

It first offers to change the machine's hostname; press Enter to keep it. It
then asks for your `sudo` password once and installs everything without
further questions.

## What gets installed

| Group             | Tools                                                                 |
|-------------------|-----------------------------------------------------------------------|
| AI coding tools   | Claude Code (`claude`), Codex CLI (`codex`), omp, herdr               |
| Compute providers | Colab CLI (`colab`), Hugging Face CLI (`hf`)                          |
| Python            | uv, pipx                                                              |
| JavaScript        | Node.js, npm                                                          |
| Development       | GitHub CLI, Docker Engine, Docker Compose                             |
| System            | NetBird, btop, tmux, and all available Ubuntu upgrades                |
| Strix only        | AMD ROCm 10 for `gfx1151`, amd-debug-tools, `render` and `video` group membership |

The AI coding tools and compute CLIs are installed for your user in
`~/.local/bin`; the rest are system packages. Open a new shell afterwards so
the new `PATH` applies.

## Strix Halo: reboot and verify

`hi install strix` ends with a local report. Checks that need a new login
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

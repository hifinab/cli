# Roadmap

Implementation order for `hi`. Completed releases use `[x]`; planned work uses
`[ ]`. Detailed behavior and acceptance criteria live in `docs/specs/`.

Only work with an accepted specification in `specs/approved/` is listed below.
Drafts in `specs/ideas/` remain outside the roadmap until approved.

## Released

### v0.1.0 — Initial workstation support

- [x] Install the CLI from checksummed amd64 and arm64 release assets.
- [x] Configure and verify an Ubuntu Strix Halo workstation.
- [x] Create users with render and video access.

### v0.2.0 — General workstation installation

- [x] Install the non-hardware workstation software through `hi install`.

### v0.3.0 — NetBird enrollment

- [x] Connect a machine with `hi net <setup-key>`.

### v0.4.0 — NetBird device naming

- [x] Prompt for the NetBird device hostname and show the machine hostname as
  the default.

### v0.5.0 — NetBird security and lifecycle

- [x] Secure NetBird enrollment and add lifecycle commands.
  Approved spec: [hi_net.md](specs/approved/hi_net.md).

### v0.5.1 — AI coding tools

- [x] Install Claude Code, Codex CLI, and herdr, and verify their commands.

### v0.5.2 — Unattended installation

- [x] Install Codex without its interactive launch prompt.
- [x] Run `hi` straight from the installer with `sh -s -- <command>`.

## Planned

### v0.6.0 — Colab compute

- [x] Start, list, inspect, reach, and stop Colab instances with `hi compute`:
  `up`, `ls`, `status`, `ssh`, `tunnel`, `logs`, `stop`, `hardware`, and
  `providers`, plus a guided menu for bare `hi compute`.
- [x] Enforce a maximum lifetime with a detached watcher and at every
  `hi compute ls`; confirm paid hardware; validate hardware names before
  calling Colab.
- [x] Run Python scripts to completion with `hi compute run`.
- [x] Serve a GGUF model with llama.cpp and tunnel its OpenAI-compatible API
  with `hi compute serve`, including the tested Qwen3.8-Flash-Next recipe.
- [x] Install the Colab CLI with `hi install`.
- [ ] Verify against a live Colab account before release.
  Approved spec: [hi_compute.md](specs/approved/hi_compute.md).

Dependency: the Colab CLI (`google-colab-cli`) and a Colab Pro or Pro+ plan,
which SSH requires.

### v0.7.0 — Hugging Face compute

- [ ] Install the `hf` CLI and add `hi login hf`.
- [ ] Run, list, follow, wait for, and stop Hugging Face Jobs with
  `hi compute run`, calling the Jobs REST API directly.
- [ ] Start and reach Hugging Face instances with `hi compute up`, `ssh`,
  `tunnel`, and `serve`.
  Approved spec: [hi_compute.md](specs/approved/hi_compute.md).

Dependencies: the v0.6.0 driver interface, state, and SSH layer; the
token-read exception in [hi_login.md](specs/approved/hi_login.md).

### v0.8.0 — RunPod compute

- [ ] Add a RunPod driver through its REST API, with the on-instance watchdog
  RunPod needs for lifetime limits.
  Approved spec: [hi_compute.md](specs/approved/hi_compute.md).

Dependency: extends the v0.6.0 and v0.7.0 driver interface.

### v0.9.0 — Updates and authentication

- [ ] Add atomic, checksummed self-updates.
  Approved spec: [hi_update.md](specs/approved/hi_update.md).
- [ ] Delegate GitHub and OMP authentication to their native tools.
  Approved spec: [hi_login.md](specs/approved/hi_login.md).

Dependencies: `hi update` relies on the existing release assets and checksum
pipeline. Login requires the corresponding installed CLI.

### v0.10.0 — Project bootstrap

- [ ] Add the interactive `hi init` helper and deterministic Python template.
  Approved spec: [hi_init.md](specs/approved/hi_init.md).

Dependency: the Python template requires `uv` and establishes the metadata and
safe-generation contract used by later templates.

### v0.11.0 — Specialized project templates

- [ ] Add the quantitative-research template.
  Approved spec: [hi_init.md](specs/approved/hi_init.md).
- [ ] Add the deterministic web-application template.
  Approved spec: [hi_init.md](specs/approved/hi_init.md).

Dependency: both templates extend the v0.10.0 planner, conflict detection,
metadata, agent instructions, and documentation structure.

### v0.12.0 — Focused machine operations

- [ ] Read and change the machine hostname independently of installation.
  Approved spec: [hi_hostname.md](specs/approved/hi_hostname.md).
- [ ] Inspect users and login history; manage accounts, groups, and sudo access.
  Approved spec: [hi_user.md](specs/approved/hi_user.md).
- [ ] Install `hi` and the AI and developer tools for all users, including
  accounts created later. Review the caveats before starting.
  Approved spec: [hi_install_shared.md](specs/approved/hi_install_shared.md).

Dependency: shared tools no longer update themselves, so they rely on
rerunning `hi install` or on the v0.9.0 `hi update` to stay current. A shared
`hi` also changes how `hi update` replaces the binary.

### Deferred — Modal compute

- [ ] Add a Modal driver through Modal's official Go SDK.
  Approved spec: [hi_compute.md](specs/approved/hi_compute.md).

Waiting until Modal's Go SDK leaves beta and its package path settles. Not
scheduled to a version yet.

The TUI, compute files, and templates in the same spec are still marked draft;
v0.6.0 ships a numbered menu instead of the full TUI.

# Shared tool installation specification

Status: Approved

Dependencies: existing `hi install` script; Anthropic's Claude Code apt
repository; npm; pipx 1.5 or newer with `--global`.

## Goal

Make every tool installed by `hi install` usable by every account on the
machine, including accounts created later, instead of only by the user who ran
the installer.

## Current state

apt packages are already system-wide: Node.js and npm, GitHub CLI, Docker,
NetBird, btop, tmux, and ROCm. The remaining tools use per-user installers that
write to the installing user's `~/.local/bin` and home directory.

## Installation changes

| Tool            | Shared installation                                                   | Location                |
|-----------------|-----------------------------------------------------------------------|-------------------------|
| Claude Code     | Anthropic apt repository, `stable` channel, package `claude-code`     | apt                     |
| Codex CLI       | `sudo npm install -g @openai/codex`                                   | `/usr/local/bin/codex`  |
| omp             | Installer with `sudo`, `PI_INSTALL_DIR=/usr/local/bin`, `--binary`    | `/usr/local/bin/omp`    |
| herdr           | Installer with `sudo`, `HERDR_INSTALL_DIR=/usr/local/bin`             | `/usr/local/bin/herdr`  |
| uv              | Installer with `sudo`, `UV_UNMANAGED_INSTALL=/usr/local/bin`          | `/usr/local/bin/uv`     |
| amd-debug-tools | `sudo pipx install --global amd-debug-tools`                          | `/usr/local/bin`        |

The Claude Code apt key must match fingerprint
`31DDDE24DDFAB679F42D7BD2BAA929FF1A7ECACE` before the repository is added.

Per-user settings, credentials, and history stay in each home directory
(`~/.claude`, `~/.codex`, and so on); every user signs in separately.

## Migration

When `hi install` finds a per-user copy that it previously installed for the
running user, it removes only the program files so the shared copy is not
shadowed by `~/.local/bin`, which precedes `/usr/local/bin` on `PATH`:

- `~/.local/bin/claude` and `~/.local/share/claude`
- `~/.local/bin/codex`, `~/.local/bin/codex-code-mode-host`, and
  `~/.codex/packages/standalone`
- `~/.local/bin/omp`, `~/.local/bin/herdr`, `~/.local/bin/uv`, and
  `~/.local/bin/uvx`
- the per-user pipx `amd-debug-tools` environment

It never removes `~/.claude`, `~/.codex` configuration, credentials, or
history. It lists what it removed.

## Updates

Shared tools cannot update themselves for non-root users. Rerunning
`hi install` upgrades all of them; Claude Code also upgrades with normal apt
upgrades. `hi update` may later refresh the shared tools as well.

## Verification

`hi verify strix` checks that each command resolves outside any home
directory, and reports a shadowing per-user copy as a failure.

## Caveats — review before starting

1. **No self-updates.** Claude Code, Codex, omp, herdr, and uv update
   themselves today. Shared copies can only be updated by an administrator,
   so machines drift unless `hi install` or `hi update` is rerun. Decide the
   update cadence before shipping.
2. **Claude Code runs a week behind.** The apt `stable` channel is typically
   about a week old and skips releases with regressions. The `latest` channel
   removes the delay but not the manual upgrade.
3. **Update prompts will mislead users.** Tools that detect a new version may
   tell a normal user to run an update command that fails without `sudo`.
   Claude Code shows a one-time notice for unwritable installs; Codex, omp, and
   herdr may not. Consider disabling their update checks where supported.
4. **Migration deletes files in home directories.** Removing per-user copies
   must be limited to the exact paths above, must never touch settings or
   credentials, and only applies to the user running `hi install`. Other
   accounts that installed their own copies keep them and shadow the shared
   version until cleaned up.
5. **Codex depends on npm's global prefix.** It relies on Ubuntu's Node.js 22
   and `/usr/local` as the npm prefix. A user-level npm prefix or nvm
   installation would hide or break the shared copy.
6. **Installers may change their options.** `PI_INSTALL_DIR`,
   `HERDR_INSTALL_DIR`, `UV_UNMANAGED_INSTALL`, and `--binary` are installer
   internals, not stable interfaces. Pin or re-check them at implementation
   time, and fail loudly if a binary lands anywhere other than
   `/usr/local/bin`.
7. **Docker access is not part of this.** Every user can run the `docker`
   command, but only `docker` group members can use it without `sudo`. Group
   membership is root-equivalent and stays a deliberate per-user choice.
8. **`hi` itself stays per-user.** Only administrators can run `hi install`,
   so `hi` remains in the installer's `~/.local/bin`.

## Acceptance criteria

1. A user created after `hi install` can run `claude`, `codex`, `omp`,
   `herdr`, `uv`, and `amd-debug-tools` without any per-user setup.
2. Every one of these commands resolves under `/usr/bin` or `/usr/local/bin`.
3. Rerunning `hi install` upgrades the shared tools.
4. Migration removes the running user's per-user program copies and leaves
   their settings and credentials unchanged.
5. `hi verify strix` fails when a per-user copy shadows a shared tool.

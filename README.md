# hi

`hi` prepares Hifin Linux machines and project folders. It is a small, static
Go binary so installation does not require a language runtime.

## Install

Releases install per user to `~/.local/bin/hi`:

```sh
curl -fsSL https://hifin.sh/install.sh | sh
```

Until `hifin.sh` serves the installer, use the repository copy:

```sh
curl -fsSL https://raw.githubusercontent.com/hifinab/cli/main/install.sh | sh
```

The installer supports Linux on amd64 and arm64, verifies the release checksum,
and adds `~/.local/bin` to `PATH` in `~/.profile` when needed. Set
`HI_INSTALL_DIR` to override the destination.

## Commands

```text
hi adduser <name>  Create a user with render and video access
hi install         Install general workstation software
hi install strix   Install software and Strix Halo hardware support
hi net             Securely enroll this machine with NetBird
hi net status      Show NetBird connection status
hi net down        Disconnect NetBird
hi net reconnect   Reconnect an enrolled NetBird peer
hi verify strix    Check an installed Strix Halo workstation
hi version         Print the installed version
hi help            Show help
```

`hi install` first offers to change the current hostname; pressing Enter keeps
it unchanged. It then asks for `sudo` once before installing the general
workstation software. Open a new shell afterward to apply `PATH` changes.

`hi install strix` installs the same software plus the Strix Halo hardware
support. It ends with a local report covering packages, commands, services,
group membership, GPU devices, and ROCm detection. No report data is uploaded.
Checks that require the new login session are marked pending; after reboot, run
`hi verify strix` for the final hardware report.

`hi adduser <name>` runs Ubuntu's interactive `adduser`, then adds the new user
to the `render` and `video` groups. Usernames must follow Ubuntu's conventional
lowercase format and may contain digits, hyphens, and underscores.

## NetBird

After installing the workstation software, run `hi net`. It reads the setup key
from a no-echo terminal prompt, then explains that the next prompt controls the
NetBird device hostname. Press Enter to accept the machine hostname or enter a
different valid hostname.

`hi` writes the key to a temporary `0600` file and delegates to:

```sh
netbird up --setup-key-file "$TEMPORARY_KEY_FILE" --hostname "$DEVICE_HOSTNAME"
```

The temporary file is removed after success, failure, or interruption. The key
is never placed in process arguments or command output. The old
`hi net <setup-key>` form is intentionally rejected.

For automation, provide either a protected file or an environment value:

```sh
chmod 600 "$NETBIRD_SETUP_KEY_FILE"
hi net --setup-key-file "$NETBIRD_SETUP_KEY_FILE"

HI_NETBIRD_SETUP_KEY="$NETBIRD_SETUP_KEY" hi net
```

`hi net status` preserves `netbird status` output. `hi net down` disconnects
the peer. `hi net reconnect` runs `netbird down` followed by `netbird up`,
reusing the peer's stored enrollment without requesting another setup key.
Running any command without NetBird installed reports that `hi install` is
required.

## Workstation setup

Both installation profiles currently require Ubuntu 26.04. `hi install`
installs:

- uv and pipx
- Node.js and npm
- GitHub CLI
- Docker Engine and Docker Compose
- Claude Code, Codex CLI, and herdr
- NetBird, OMP, btop, and tmux
- Available Ubuntu package upgrades

`hi install strix` installs everything above and adds:

- AMD ROCm 10 for `gfx1151`
- amd-debug-tools
- `render` and `video` group membership

The Strix profile requires amd64 and a reboot.
It has been exercised on a physical AMD Ryzen AI Max+ 395 machine with Radeon
8060S graphics; the final report detected the GPU through both `amd-smi` and
`rocminfo`.

## Development and releases

Build with Go 1.24 or newer:

```sh
go build -o hi .
```

Pushing a `v*` tag builds amd64 and arm64 Linux binaries, writes SHA-256
checksums, and publishes them in a GitHub release. The setup script is embedded
in each binary, so script changes ship with the next release.
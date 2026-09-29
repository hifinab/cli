# hi

`hi` prepares Hifin Linux machines and project folders. It is a small, static
Go binary so installation does not require a language runtime.

**Guide:** step-by-step instructions, examples, and every option at
<https://hifin.sh/guide/>.

## Install

Releases install per user to `~/.local/bin/hi`:

```sh
curl -fsSL https://hifin.sh/install.sh | sh
```

To set up a new machine in one step, pass the `hi` command after `sh -s --`:

```sh
curl -fsSL https://hifin.sh/install.sh | sh -s -- install strix
```

The installer supports Linux on amd64 and arm64, verifies the release checksum,
and adds `~/.local/bin` to `PATH` in `~/.profile` when needed. That change
applies to new shells; until then the installer prints the full path to run, such
as `~/.local/bin/hi install strix`. Set `HI_INSTALL_DIR` to override the
destination.

To update later, run `hi update` (v0.7.1 and newer).

## Commands

```text
hi adduser <name>  Create a user with render and video access
hi install         Install general workstation software
hi install strix   Install software and Strix Halo hardware support
hi net             Securely enroll this machine with NetBird
hi net status      Show NetBird connection status
hi net down        Disconnect NetBird
hi net reconnect   Reconnect an enrolled NetBird peer
hi compute         Start, reach, and stop remote GPU machines (Colab, Hugging Face, RunPod, Shadeform)
hi login <hf|colab|runpod|shadeform>  Sign in to a compute provider
hi connect <server>         Join a team's hi server, which approves and pays for compute
hi disconnect               Leave it; hi compute uses your own keys again
hi server          Run the server that brokers compute for a team (hi server help)
hi skill           Teach coding agents to use hi (writes SKILL.md)
hi verify strix    Check an installed Strix Halo workstation
hi update          Update hi to the latest release (--check, --version)
hi version         Print the installed version
hi help            Show help
```

`hi install` first offers to change the current hostname; pressing Enter keeps
it unchanged. It then asks for `sudo` once before installing the general
workstation software. Every installer runs unattended; Codex is installed with
`CODEX_NON_INTERACTIVE=1` so its "Start Codex now?" prompt cannot stall setup.
Open a new shell afterward to apply `PATH` changes.

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

## Remote compute

`hi compute` rents a remote machine, lets you use it as if it were local, and
gives it back, with the same commands on every provider: Colab (through its
CLI), Hugging Face Jobs, and RunPod (through their APIs). Run `hi compute`
in a terminal for a guided menu that prints the equivalent command for every
step.

When both providers are signed in, `hi` picks the one that offers the
hardware you ask for (`G4` is Colab, `a10g-small` is Hugging Face). Otherwise
pass `--on colab` or `--on hf`, or set `HI_COMPUTE_PROVIDER`.

```sh
hi compute providers                      # which providers are signed in?
hi compute hardware                       # GPUs, prices, and your Colab balance
hi compute up --gpu T4 --name play        # start a machine (stops after 4h)
hi compute ssh play                       # shell on it
hi compute tunnel play 8000               # its port 8000 on 127.0.0.1:8000
hi compute ls                             # what is running and when it stops
hi compute stop play                      # or just `hi compute stop` to pick from a list
```

Serve a model with llama.cpp and get an OpenAI-compatible API on
`http://127.0.0.1:8080/v1`:

```sh
hi compute serve qwen3.8-flash-next --on colab          # tested recipe, Colab G4
hi compute serve unsloth/Qwen3-8B-GGUF --quant Q4_K_M --gpu L4
hi compute logs <name> --follow                         # build, download, server
```

Run a Python script to completion on a fresh machine:

```sh
hi compute run --gpu T4 --max 2h train.py -- --epochs 3
```

Every machine has a maximum lifetime (`--max`, in hours such as `2` or with a
unit such as `30m`; default 4 hours for instances and 1 hour for runs, at most
24 hours on Colab). `--max none` removes the limit after a warning. Hugging Face enforces it
itself; for Colab, `hi` runs a detached background watcher and checks again on
every `hi compute ls`. Paid hardware asks for confirmation; pass `--yes` in
scripts. `--dry-run` shows the provider command or API request without
starting anything.

### Hugging Face Jobs

```sh
hi login hf                                              # once
hi compute hardware --on hf                              # flavors and $/hour
hi compute run --gpu a10g-small train.py -- --epochs 3   # uv script, logs streamed
hi compute run --on hf python:3.12 -- python -c 'print(1)'
hi compute run --gpu a10g-small --detach --secret WANDB_API_KEY train.py
hi compute logs <name> --follow
hi compute wait <name>
hi compute up --gpu a10g-small --name box                # SSH-able GPU box
hi compute serve qwen3.8-flash-next --on hf              # RTX PRO 6000, HTTPS URL
```

Jobs need pre-paid credits on the account that pays. `hi` bills your own
account if it can pay, otherwise your only organization that can, and names
the payer before every paid start. `hi compute billing` lists your accounts
and which can pay; `hi compute billing ORG` saves a choice, `--namespace ORG`
overrides it for one command. `--secret` sends a
value from your environment as an encrypted job secret. `ssh` and `tunnel`
need an SSH key registered at https://huggingface.co/settings/keys. A served
model is reachable at `https://<job>--8000.hf.jobs/v1` with your Hugging Face
token as the API key.

### RunPod

```sh
hi login runpod                                  # paste an API key from console.runpod.io
hi compute hardware --on runpod                  # GPUs such as rtx-4090, with prices
hi compute up --gpu rtx-4090 --name box --max 2  # SSH-able pod
hi compute ssh box
hi compute stop box                              # terminates the pod
```

RunPod pods have no built-in time limit, so `hi` installs a watchdog on the
pod that terminates it at `--max`, besides its local watcher. `run` is not
supported on RunPod yet.

### Colab

Colab setup, once per workstation:

1. `hi install` installs the Colab CLI (`google-colab-cli`).
2. Run `hi login colab` and sign in with the Colab Pro or Pro+ account; SSH and
   tunnels need a paid plan.
3. Make sure `~/.ssh/id_ed25519` exists (`ssh-keygen -t ed25519`).

Colab's terms allow SSH on paid plans but forbid public web services, so
tunnels bind to localhost only. Stop machines when you are done; a G4 uses
about 9 compute units per hour.

## Agent skill

`hi skill` teaches coding agents such as Claude Code and Codex how to use
`hi`, including the rules for remote compute: ask before spending, show
`--dry-run` first, always set `--max`, stop what they start, and never print
tokens.

```sh
cd my-project && hi skill    # .agents/skills/hi/SKILL.md, linked from .claude/skills/hi
hi skill --global            # the same under ~, for every project
hi skill --print             # just show it
```

The skill is written once in the Agent Skills folder that Codex and others
read. Claude Code reads only `.claude/skills`, so `hi skill` makes that a
relative symlink to the same folder. Commit both so the whole team's agents
get it, and rerun `hi skill` after updating `hi` to refresh it.

## Workstation setup

Both installation profiles currently require Ubuntu 26.04. `hi install`
installs:

- AI coding tools, installed per user in `~/.local/bin`:
  - [Claude Code](https://code.claude.com/docs) (`claude`)
  - [Codex CLI](https://developers.openai.com/codex/cli) (`codex`)
  - [omp](https://omp.sh) (`omp`)
  - [herdr](https://herdr.dev) (`herdr`)
  - [Colab CLI](https://github.com/googlecolab/google-colab-cli) (`colab`) and
    [Hugging Face CLI](https://huggingface.co/docs/huggingface_hub/guides/cli)
    (`hf`), used by `hi compute` and `hi login`
- [uv](https://docs.astral.sh/uv/) and [pipx](https://pipx.pypa.io)
- [Node.js](https://nodejs.org) and npm
- [GitHub CLI](https://cli.github.com)
- [Docker Engine](https://docs.docker.com/engine/) and Docker Compose
- [NetBird](https://netbird.io), [btop](https://github.com/aristocratos/btop),
  and [tmux](https://github.com/tmux/tmux)
- Available Ubuntu package upgrades

`hi install strix` installs everything above and adds:

- [AMD ROCm](https://rocm.docs.amd.com) 10 for `gfx1151`
- [amd-debug-tools](https://pypi.org/project/amd-debug-tools/)
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

## Website

GitHub Pages serves the repository root at <https://hifin.sh>. `index.html` is
the landing page and `install.sh` is the CLI installer; both go live when
pushed to `main`, without a release.
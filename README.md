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
curl -fsSL https://hifin.sh/install.sh | sh -s -- install
```

The installer supports Linux on amd64 and arm64, verifies the release checksum,
and adds `~/.local/bin` to `PATH` in `~/.profile` when needed. That change
applies to new shells; until then the installer prints the full path to run, such
as `~/.local/bin/hi install`. Set `HI_INSTALL_DIR` to override the
destination.

To update later, run `hi update` (v0.7.1 and newer).

## Commands

```text
hi adduser <name>  Create a user with render and video access
hi install         Choose workstation software to install or remove
hi install <tool>  Install the named tools (--all for every tool, --list to list them)
hi uninstall <tool>  Remove the named tools
hi net             Securely enroll this machine with NetBird
hi net status      Show NetBird connection status
hi net down        Disconnect NetBird
hi net reconnect   Reconnect an enrolled NetBird peer
hi net expose 3000 Put a local service on a temporary public address (hi net expose help)
hi init            Start a project from a template (python, web, service, pipeline, ml, and your server's own)
hi compute         Start, reach, and stop remote GPU machines (Colab, Hugging Face, RunPod, Shadeform)
hi login <hf|colab|runpod|shadeform>  Sign in to a compute provider
hi connect <server>         Join a team's hi server, which approves and pays for compute
hi disconnect               Leave it; hi compute uses your own keys again
hi data            Download the team's Hugging Face datasets, models, and buckets through it (hi data help)
hi server          Run the server that brokers compute and serves templates for a team (hi server help)
hi agent [task]    Hand a task to Claude Code, Codex, or Hermes in a box and get its report (hi agent help)
hi box shell       Run a shell or a command in a rootless box (hi box help)
hi bundle ls       Bundles of skills and tools for hi agent --bundle
hi q [request]     Ask an AI model for a shell command, or chat with it (hi q --setup first)
hi skills          Find, install, and update agent skills from skills.sh; hi skill add hi teaches agents hi
hi verify strix    Check an installed Strix Halo workstation
hi update          Update hi to the latest release, and restart a hi server service on the old one (--check, --version, --restart)
hi version         Print the installed version
hi help            Show help
```

`hi install` opens a menu with a checkbox and a short description for every
tool. Tools that are already installed start checked, as do the terminal tools,
uv, and the GitHub CLI; Strix Halo support starts checked when the machine has a
Strix Halo GPU. Checking a missing tool installs it, and unchecking an installed
tool removes it, so the same menu is also the uninstaller. Removing a tool
deletes the program but keeps its settings and sign-ins, such as `~/.claude`
and `~/.codex`. Checked tools that are already installed are left alone unless
"Update everything" is checked too, which reinstalls them and upgrades Ubuntu
packages.

When anything is being installed, the menu offers to change the hostname
(Enter keeps it), shows what will change, and asks for `sudo` once. Every
installer runs unattended; Codex is installed with `CODEX_NON_INTERACTIVE=1` so
its "Start Codex now?" prompt cannot stall setup. Open a new shell afterward to
apply `PATH` changes.

Without a terminal, name the tools instead: `hi install claude codex gh`,
`hi install --all` (every tool except Strix Halo support), or
`hi uninstall docker`. `hi install --list` shows the names and what is
installed. `hi install strix` still works; in a terminal it opens the menu with
Strix Halo support checked.

Installing Strix Halo support ends with a local report covering packages,
commands, services, group membership, GPU devices, and ROCm detection. No report
data is uploaded. Checks that require the new login session are marked pending;
after reboot, run `hi verify strix` for the final hardware report.

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

`hi net expose <port>` puts a service on this machine, even one on
`localhost`, on a temporary public HTTPS address through NetBird's reverse
proxy: protected by a generated password unless you pass `--pin`,
`--groups`, or `--public`, ending after `--max` (default 1h), with a request
log; `--detach`, `ls`, and `stop` for background use. See
[Share a local service](https://hifin.sh/guide/workstation/expose/).

## Remote compute

`hi compute` rents a remote machine, lets you use it as if it were local, and
gives it back, with the same commands on every provider: Colab (through its
CLI), Hugging Face Jobs, RunPod, and Shadeform (through their APIs). Run `hi compute`
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

## Projects

`hi init` creates a repository that coding agents work well in: a
`make check` that runs format, lint, types, and tests, an `AGENTS.md` with
short rules (read by Claude Code through `CLAUDE.md`), Claude Code settings
that keep it out of `.env` and `secrets/`, the `hi` skill, and CI.

```sh
hi init                         # choose a template, name, and directory
hi init python pricing-tools    # Python: uv, ruff, pyrefly, pytest
hi init web dashboard           # React and TypeScript on Vite: npm, Biome, Vitest
hi init service orders-api      # FastAPI, SQLAlchemy and Alembic, Dockerfile
hi init pipeline prices-feed    # an idempotent run --date: land raw, parse, load
hi init ml forecaster           # PyTorch; ROCm on Strix Halo, CUDA on NVIDIA, else CPU
hi init --list                  # every template and where it comes from
```

It shows every file and command first, never overwrites a file with other
content, and records what it generated in `.hifin/template.json`.

Later, `hi init --update` brings a project up to the current templates: it
replaces what `hi` owns, reports hand edits as conflicts, and writes changes
to the project's own files to `docs/upgrades/` for an agent to apply.
`hi init --update --check` does the same comparison for CI.
`hi init --adopt <template>` brings an existing repository under a template
without touching its code.

The built-in templates are public and generic; they live in `templates/` and
are embedded in the binary. Private templates, such as quant `research`,
live in a private repository that a `hi server` mirrors
(`hi server templates add`) and serves, signed, to connected devices. Template
authors can try a folder of layers with `HI_TEMPLATES_DIR=<path> hi init …`.

## Terminal helper

`hi q` turns a request in plain words into a shell command, and shows it
before anything runs:

```sh
hi q move all the md files here into a new folder called notes
hi q                                  # a chat: ask, run, follow up
hi q -c and now zip them              # continue the last conversation
make test 2>&1 | hi q why does this fail
hi q --explain 'tar -xzvf x.tar.gz -C /tmp'
hi q --print find files over 1 GB in my home folder
```

Enter runs the command in your shell, `e` edits it, `c` copies it, `?`
explains it, and Esc cancels; if it fails, the model gets the error and
proposes a fix. Before proposing, the model can list files, read files in
the current folder, read `--help`, and run read-only commands. `hi` parses
every command itself and marks it read-only, changing files, or dangerous;
dangerous ones, such as `sudo`, `rm -r` outside the current folder, or
`curl … | sh`, need `yes` typed, and globs in `mv`, `cp`, and `rm` show what
they match first.

`hi q --setup` chooses the model (the connected hi server's, Claude Code, OpenRouter, any
OpenAI-compatible endpoint including Ollama and `hi compute serve`, or an
Anthropic key) and offers to add shell integration to `~/.bashrc` or
`~/.zshrc`. With it, `hi q` sees the shell's current history and last exit
status, `q count lines (and subfolders)` works without quotes, commands it
runs land in your history, and `cd` or `export` run in your shell. Each
question carries a short, redacted context, which `hi q --context` shows.
Commands that run are logged to `~/.local/state/hi/q/log.jsonl`, without
their output.

## Agents and boxes

`hi agent` hands a task to Claude Code, Codex, or Hermes, which works without
permission prompts in a box, and waits for one report, the same for every
agent: its final message, whether it worked, and the files changed.
`hi box` is the box itself, for any code: a rootless Podman container that
holds the project, optionally on a new git worktree, and nothing else from
the home folder.

```sh
hi agent "make the flaky test reliable"        # the first agent signed in; waits for the report
hi agent codex --json "review this branch"     # the report as JSON, for scripts and agents
hi agent claude brief.md                       # the task from a markdown file
hi agent --bundle web,office "compare GPU clouds in a Word document"   # skills and tools for work that isn't code
hi agent claude                                # interactive, in a box
hi box shell                                   # or hi box run -- make test
hi box diff myproject-1                        # what it changed, flagging files that run on the host
```

The box's only way out is hi's proxy, which allows, by default, GitHub and
package registries, plus the agent's own hosts (`--network locked|dev|open`,
`hi box allow`). Claude Code's token never enters the box: the proxy swaps a
placeholder for it. Git hooks and config are read-only, `--gpu` passes the
Strix Halo in, and `devcontainer.json`'s image, environment, and
`postCreateCommand` are used, with hi's settings under `customizations.hi`,
where `"bundles": ["data"]` gives every box and agent in the project those
bundles.

## Team data

`hi data` downloads the private Hugging Face datasets, models, and buckets
of the team's organizations through the hi server, with the official `hf`
tool and no Hugging Face token on the machine:

```sh
hi data                        # search by words, pick one, download it
hi data ls                     # what you may download
hi data get hifinab/bars-1d    # into ./data/bars-1d (--to, --revision, --include, --exclude)
```

The server keeps one read token per organization (`hi server data add
<org>`) and passes on only the read calls of one repository or bucket; file
contents come straight from Hugging Face's CDN. In a project, `hi data get`
records the commit it fetched in `.hifin/data.json`; `hi data run --
python train.py` lets `load_dataset` and `from_pretrained` read the team's
repositories directly, and `hi box --data` gives a box the same.
`hi compute run --data hifinab/bars-1d train.py` gives a cloud job the data:
on Hugging Face Jobs the server opens a narrow public address with
`netbird expose` for the run, so the script can also call `load_dataset`;
elsewhere it sends signed download links. `hi compute up --data
hifinab/bars-1d` gives a RunPod or Shadeform machine `HF_ENDPOINT` and a
token for those repositories in its shells. See
[Download the team's data](https://hifin.sh/guide/data/).

## Agent skills

`hi skill` finds, installs, and updates [skills](https://agentskills.io)
for coding agents, from [skills.sh](https://skills.sh) and any git
repository, and writes the `hi` skill, which teaches agents how to use `hi`:
ask before spending, show `--dry-run` first, always set `--max`, stop what
they start, and never print tokens.

```sh
hi skills                                    # in a terminal: search, browse, and pick
hi skill add hi                              # the hi skill, into .agents/skills, linked from .claude/skills
hi skill add anthropics/skills --skill pdf   # a skill from GitHub, at its current commit
hi skill update                              # newer commits, with the change and audits shown first
hi skill ls
```

Skills go where `npx skills` puts them, `.agents/skills/<name>` linked
from `.claude/skills/<name>`, and are recorded with their commit in the
same `skills-lock.json`. Each stays at its commit until `hi skill update`.
skills.sh's security audits are shown, and a skill rated `high` or
`critical` needs a yes. `hi` sends skills.sh no install events.

Using Codex or another agent? Run `hi skill add hi --global` once per
machine. An agent that doesn't have `hi` yet can start from
<https://hifin.sh/llms.txt>, which tells it to install `hi` and follow the
skill.

## Workstation setup

`hi install` currently requires Ubuntu 26.04. It offers these tools, each
under the name in brackets for `hi install <tool>` and `hi uninstall <tool>`:

- AI coding tools, installed per user in `~/.local/bin`:
  - [Claude Code](https://code.claude.com/docs) [`claude`]
  - [Codex CLI](https://developers.openai.com/codex/cli) [`codex`]
  - [omp](https://omp.sh) [`omp`]
  - [herdr](https://herdr.dev) [`herdr`]
  - [Hermes Agent](https://github.com/NousResearch/hermes-agent) [`hermes`]
  - [Colab CLI](https://github.com/googlecolab/google-colab-cli) [`colab`] and
    [Hugging Face CLI](https://huggingface.co/docs/huggingface_hub/guides/cli)
    [`hf`], used by `hi compute` and `hi login`. The Colab CLI needs uv.
- [uv](https://docs.astral.sh/uv/) [`uv`]
- [Node.js](https://nodejs.org) and npm [`node`]
- [GitHub CLI](https://cli.github.com) [`gh`]
- [Docker Engine](https://docs.docker.com/engine/) and Docker Compose [`docker`]
- [Podman](https://podman.io), rootless, with `crun` [`podman`]
- [NetBird](https://netbird.io) [`netbird`]
- [btop](https://github.com/aristocratos/btop),
  [tmux](https://github.com/tmux/tmux), and
  [timg](https://github.com/hzeller/timg), which shows images and videos in
  the terminal [`terminal`]
- Strix Halo support [`strix`]: [AMD ROCm](https://rocm.docs.amd.com) 10 for
  `gfx1151`, [amd-debug-tools](https://pypi.org/project/amd-debug-tools/), and
  `render` and `video` group membership. It requires amd64 and a reboot.
- Available Ubuntu package upgrades [`upgrade`]

Any install also sets up a few base packages that are never removed: `curl`,
`wget`, `gnupg`, [pipx](https://pipx.pypa.io), and `wtmpdb`.

Strix Halo support has been exercised on a physical AMD Ryzen AI Max+ 395
machine with Radeon 8060S graphics; the final report detected the GPU through both `amd-smi` and
`rocminfo`.

## Development and releases

Build with Go 1.24 or newer:

```sh
go build -o hi .
go test ./...
HI_TEMPLATE_CHECK=1 go test -run Template .   # generate each template and run its make check
```

Pushing a `v*` tag builds amd64 and arm64 Linux binaries, writes SHA-256
checksums, and publishes them in a GitHub release. The setup script is embedded
in each binary, so script changes ship with the next release.

## Website

GitHub Pages serves the repository root at <https://hifin.sh>. `index.html` is
the landing page, `install.sh` is the CLI installer, and `llms.txt` points
coding agents to the skill; all go live when
pushed to `main`, without a release.
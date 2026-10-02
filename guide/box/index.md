---
title: Run agents in a box
description: Run Claude Code or Codex without permission prompts in a rootless container with hi box, with no credentials, a network that only reaches allowed hosts, and the Strix Halo GPU.
---

Coding agents are most useful when they can work without asking before
every command, and that is only safe inside a boundary. `hi box` gives an
agent a rootless container with the project in it and nothing else from
your home folder: no SSH keys, no GitHub token, no cloud keys. Its network
reaches only what is allowed.

```sh
hi box claude "make the flaky test in tests/test_sync.py reliable"
hi box ls
hi box diff myproject-1
```

The agent works on its own git branch, `hi-box/<name>`, in a new worktree,
and leaves its work there for you to review and merge.

## Start a box

```sh
hi box claude [prompt]       # Claude Code, without permission prompts
hi box codex [prompt]        # Codex, the same
hi box shell                 # a shell in a box for this project
hi box run -- make test      # one command; exits with its status
```

With a prompt, the agent runs on its own in the background, and `hi box
attach <name>` follows it. Without one, you work with it in your terminal.
Agents always get a new worktree; `shell` and `run` work in the project
folder itself unless you add `--worktree`. `--here` puts an agent in the
project folder instead.

The box is named after the project and a number, such as `myproject-1`, or
`--name`. The first box builds hi's image, Ubuntu with git, Python, uv,
Node.js, and the usual tools, which takes a few minutes once. The agents come
from your machine, mounted read-only, so the box runs the versions you have.

## What the box holds

- The project, or its new worktree, at the same path as on your machine.
- The repository's `.git`, so the agent can commit, with `.git/hooks` and
  `.git/config` read-only: it can't plant a hook that runs on your machine.
- A home folder of its own, kept with the box.
- Nothing else from your home folder.

The box runs as you, with no extra privileges, at most 4,096 processes, and
16 GB of memory (`--memory`).

## Sign-ins

**Claude Code** never sees your token. The box gets a placeholder, and hi's
proxy outside the box swaps in your Claude sign-in for requests to
Anthropic. The proxy reads the sign-in from Claude Code on your machine, so
it stays fresh while you use Claude there. For long unattended runs, store a
token that lasts a year:

```sh
claude setup-token          # prints a token
hi box token claude         # paste it; kept in ~/.config/hi, never in a box
```

**Codex** gets a copy of its sign-in (`~/.codex/auth.json`) in the box's home
folder for now.

## Network

The box has no route out. Its only way out is hi's proxy, which allows a
list of hosts and logs every connection:

| `--network`   | Allows                                                                   |
|---------------|--------------------------------------------------------------------------|
| `locked`      | The agents' own hosts (for Codex: `chatgpt.com` and OpenAI's)            |
| `dev` (default) | `locked`, plus GitHub, PyPI, npm, Go modules, crates.io, PyTorch, and Hugging Face |
| `open`        | Everything, still logged                                                 |

When the agent is refused something it needs:

```sh
hi box allow myproject-1                     # what was refused
hi box allow myproject-1 files.example.com   # allow it; no restart needed
```

Name lookups inside the box fail, so tools that ignore the proxy can't get
out. The allowlist checks host names, so it stops accidents and casual
leaks, not a determined agent: it can still send the project to a host it
is allowed to reach.

## devcontainer.json

If the project has `.devcontainer/devcontainer.json`, `hi box` uses its
`image` or `build.dockerfile`, `containerEnv`, and `postCreateCommand`. It
ignores the fields that would run on your machine or widen the box, such as
`initializeCommand`, `runArgs`, `mounts`, and `privileged`, and says so.

Settings for hi go under `customizations.hi`:

```json
{
  "image": "mcr.microsoft.com/devcontainers/python:3.12",
  "customizations": {
    "hi": { "network": "dev", "gpu": true, "domains": ["data.example.com"] }
  }
}
```

The repository writes `domains`, so `hi box` asks once before allowing them,
and again when the list changes.

## The GPU

`--gpu`, or `"gpu": true`, passes the Strix Halo's GPU into the box. Tested
on the Radeon 8060S (gfx1151) with rootless Podman: ROCm's `rocminfo` in
the box finds it. A box with the GPU shares the host's kernel and GPU driver, so
it is a weaker boundary than one without.

## Review and clean up

```sh
hi box ls                    # every box, its state, branch, and changes
hi box diff myproject-1      # commits and files; --full for the whole diff
hi box stop myproject-1
hi box rm myproject-1        # keeps the branch if it has commits
```

`diff` marks files that run on your machine later, such as a `Makefile`,
`package.json`, `.envrc`, CI workflows, and editor tasks. Read those before
you run anything from the branch. Merge the work in the project with
`git merge hi-box/<name>`.

## Podman or Docker

`hi box` uses rootless Podman, which `hi install podman` sets up: an escape
from the box lands as you, not as root. Without Podman it uses Docker and
says that Docker's daemon runs as root, so an escape would be root on the
machine.

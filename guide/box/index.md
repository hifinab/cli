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

## The team's data

`--data`, or `"data": true`, lets `hf` and Python code in the box download
the team's Hugging Face datasets, models, and buckets through the hi server
(see [Download the team's data](/guide/data/)). The box gets
`HF_ENDPOINT` and a placeholder `HF_TOKEN`; hi's proxy adds a hi data token,
which stays on the host, and allows Hugging Face's download hosts. The
machine must be connected to a hi server.

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
hi box rm --all              # every box, after one question
```

`rm --all` lists what it will remove and asks once. Boxes with uncommitted
work are kept unless you add `--force`, and branches with commits are kept.
Without a terminal it needs `--yes`. Use `--all` rather than `*`, which the
shell would turn into the names of files in the current folder.

`diff` marks files that run on your machine later, such as a `Makefile`,
`package.json`, `.envrc`, CI workflows, and editor tasks. Read those before
you run anything from the branch. Merge the work in the project with
`git merge hi-box/<name>`.

## Podman or Docker

`hi box` uses rootless Podman, which `hi install podman` sets up: an escape
from the box lands as you, not as root. Without Podman it uses Docker and
says that Docker's daemon runs as root, so an escape would be root on the
machine.

## How it works, step by step

This follows one run from start to finish:

```sh
cd ~/projects/myproject
hi box claude "make the flaky test in tests/test_sync.py reliable"
hi box ls
hi box diff myproject-1
```

### Starting the box

`hi box claude` sets the box up, starts Claude Code in it, and returns at
once:

1. **It picks the engine**: rootless Podman, or Docker with a warning when
   Podman is missing.
2. **It reads the project's settings** from `devcontainer.json`, if there is
   one: the image, environment, `postCreateCommand`, and
   `customizations.hi`. Without one, the defaults are the `dev` network, no
   GPU, and hi's image.
3. **It names the box** after the folder and a number, `myproject-1`, and
   keeps its state in `~/.local/state/hi/box/myproject-1/`.
4. **It gives the agent its own copy of the code.** `git worktree add -b
   hi-box/myproject-1 … HEAD` checks out your last commit into a new folder
   on a new branch. Your own working folder isn't touched, and uncommitted
   changes aren't in the box; hi says so when there are some.
5. **It writes the allowlist**, the hosts the box may reach, from the
   network preset, `--allow`, and the project's domains if you allowed them.
6. **It makes sure the image exists.** hi's image is built once per machine
   and tagged by a hash of its definition, so a change to it builds a new
   one. It contains no agent.
7. **It creates a private network**, `hi-box-myproject-1`, with no route out
   and no name lookups.
8. **It starts the proxy**, `hi-box-myproject-1-proxy`, the box's only way
   out. It is a second container on both the private network and a normal
   one, running your `hi` binary read-only. It allows connections only to
   the allowlist and writes every decision to `network.log`. It is the only
   container that can read your Claude sign-in, on a port of its own for
   Claude's requests.
9. **It starts the box**, `hi-box-myproject-1`, in the background:
   - It runs as you, without extra privileges (`--cap-drop=ALL`,
     `no-new-privileges`), at most 4,096 processes, and 16 GB of memory.
   - It sees the worktree; your repository's `.git`, so it can commit, with
     `hooks` and `config` read-only; an empty home folder of its own; and
     the `claude` binary from your machine, read-only. Nothing else from
     your home folder.
   - `HTTPS_PROXY` points at the proxy. The Claude token in the box is the
     word `hi-box-placeholder`, and `ANTHROPIC_BASE_URL` points at the
     proxy's Claude port. Your git name and email are passed in, so its
     commits carry them.
   - The command is `claude -p "<your prompt>"
     --dangerously-skip-permissions`: Claude Code works without asking for
     permission, which the box is there to make safe.

### While the agent works

Claude Code reads and edits files in the worktree and runs commands there,
such as the tests. Its requests to the model go to the proxy, which puts
your real token in place of the placeholder on the way to Anthropic.
Anything else on the internet goes through the allowlist, and what it
refuses shows up in `hi box allow myproject-1`. Its commits land on the
branch `hi-box/myproject-1` in your repository.

### Checking on it

`hi box ls` lists every box: whether it is running or has exited, its
branch, its changes against the commit it started from (committed,
uncommitted, and new files), and its age. It also stops the proxies of boxes
that have finished.

`hi box attach myproject-1` shows a background agent's output. With a
prompt, Claude Code only prints its final answer, so there is nothing to see
until it is done. To watch an agent work, start it without a prompt and work
with it in your terminal.

### Reviewing the work

`hi box diff myproject-1` changes nothing. It shows the commits on
`hi-box/myproject-1` since the start, and the files changed, including
uncommitted and new ones. Files that run on your machine later get a ⚠:
`Makefile`, `package.json`, `.envrc`, CI workflows, and editor tasks. Read
those before you run anything from the branch. `--full` adds the whole diff.

To keep the work, merge it in the project:

```sh
git merge hi-box/myproject-1
```

`hi box rm myproject-1` then removes the containers, the network, and the
worktree. It keeps the branch if it has commits, and refuses while the
worktree has uncommitted work, unless you add `--force`.

### Giving it a good task

A box runs unattended, so give it a task it can finish and check on its own:
"make `make check` pass", "add tests for `parse_config` and fix what they
find", or "upgrade FastAPI and fix what breaks". A vague task such as "fix
something" gives a vague result.

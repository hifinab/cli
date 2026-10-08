---
title: Run code in a box
description: Run a shell or a command in a rootless container with hi box, with no credentials, a network that only reaches allowed hosts, and the Strix Halo GPU.
---

`hi box` runs code somewhere it can't hurt you: a rootless container with
the project in it and nothing else from your home folder: no SSH keys, no
GitHub token, no cloud keys. Its network reaches only what is allowed. Use
it to try a teammate's branch or a downloaded repository, to reproduce a
bug from a clean environment, or to train on the GPU without the script
seeing your files.

```sh
hi box shell                          # a shell in a box for this project
hi box run -- make test               # one command; exits with its status
hi box run --gpu -- python train.py   # with the Strix Halo GPU
```

Coding agents run in boxes too: [`hi agent`](/guide/agent/) starts Claude
Code or Codex in one, without permission prompts, and reports what it did.

## Start a box

```sh
hi box shell                 # a shell; another hi box shell opens a second one
hi box run -- <command>      # one command, then the box stops
```

`shell` and `run` work in the project folder itself. Add `--worktree` to
work on a new git worktree and the branch `hi-box/<name>` instead, so the
box's changes stay out of your folder until you merge them.

The box is named after the project and a number, such as `myproject-1`, or
`--name`. The first box builds hi's image, Ubuntu with git, Python, uv,
Node.js, and the usual tools, which takes a few minutes once.

## What the box holds

- The project, or its new worktree, at the same path as on your machine.
- The repository's `.git`, so the box can commit, with `.git/hooks` and
  `.git/config` read-only: it can't plant a hook that runs on your machine.
- A home folder of its own, kept with the box.
- Nothing else from your home folder.

The box runs as you, with no extra privileges, at most 4,096 processes, and
16 GB of memory (`--memory`).

## Network

The box has no route out. Its only way out is hi's proxy, which allows a
list of hosts and logs every connection:

| `--network`   | Allows                                                                   |
|---------------|--------------------------------------------------------------------------|
| `locked`      | Nothing                                                                  |
| `dev` (default) | GitHub, PyPI, npm, Go modules, crates.io, PyTorch, and Hugging Face    |
| `open`        | Everything, still logged                                                 |

When the box is refused something it needs:

```sh
hi box allow myproject-1                     # what was refused
hi box allow myproject-1 files.example.com   # allow it; no restart needed
```

Name lookups inside the box fail, so tools that ignore the proxy can't get
out. The allowlist checks host names, so it stops accidents and casual
leaks, not determined code: it can still send the project to a host it is
allowed to reach.

## devcontainer.json

If the project has `.devcontainer/devcontainer.json`, `hi box` uses its
`image` or `build.dockerfile` (with `build.context` and `build.args`),
`containerEnv`, and `postCreateCommand`. It
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

A Dockerfile image is built once, and again when the Dockerfile, its
`build.args`, or a file it copies in (`COPY uv.lock …`) changes. So an image
that installs the project's locked dependencies stays in step with
`uv.lock`, and boxes start at once, offline. The
[autoresearch templates](/guide/autoresearch/templates/) work this way.

`"bundles": ["data"]` gives every box in the project the skills and tools
of those bundles, as `--bundle` does (see
[Hand tasks to agents](/guide/agent/#bundles-skills-and-the-tools-they-need)).
They build their image on hi's own base image, so they don't go with
`image` or `build.dockerfile` yet.

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

## Under the hood

`hi box` keeps each box's state in `~/.local/state/hi/box/<name>/`: its
settings, its worktree, its home folder, its allowlist, and `network.log`.
It starts two containers: the box, `hi-box-<name>`, on a private network
with no route out and no name lookups, and its proxy,
`hi-box-<name>-proxy`, on both that network and a normal one. The proxy
runs your `hi` binary read-only, allows connections only to the allowlist,
and writes every decision to `network.log`. The box runs as you, with
`--cap-drop=ALL` and `no-new-privileges`, and gets `HTTPS_PROXY` pointing
at the proxy and your git name and email for its commits.

---
title: All commands
description: Every hi command and option in one place.
---

## Overview

| Command                          | Does                                                   |
|----------------------------------|--------------------------------------------------------|
| `hi compute …`                   | Rent and use remote machines; see below                |
| `hi login <hf\|colab\|runpod\|shadeform>` | Sign in to a compute provider               |
| `hi connect <server>`            | Join a hi server; `status`, `key`                      |
| `hi disconnect`                  | Leave it; use your own keys again                      |
| `hi data …`                      | Download the team's Hugging Face datasets, models, and buckets; see below |
| `hi server …`                    | Run the server that brokers compute; see below         |
| `hi init [<template> <dir>]`     | Start a project from a template; `--update`, `--adopt` |
| `hi q [<what you want>]`         | Ask for a shell command, or chat; see below            |
| `hi shell-init bash\|zsh`        | Shell integration for `hi q`                           |
| `hi agent …`                     | Hand a task to Claude Code or Codex in a box; see below |
| `hi box …`                       | Run a shell or a command in a rootless box; see below  |
| `hi bundle …`                    | Bundles of skills for `hi agent --bundle`; see below   |
| `hi skill …`                     | Find, install, and update agent skills; see below      |
| `hi install`                     | Choose workstation software from a menu                |
| `hi uninstall <tool>…`           | Remove workstation software                            |
| `hi verify strix`                | Check a Strix Halo workstation                         |
| `hi net`                         | Enroll in NetBird; `status`, `down`, `reconnect`       |
| `hi net expose <port>`           | A local service on a temporary public address; see below |
| `hi adduser <name>`              | Create a user with GPU access                          |
| `hi update [--check] [--version <tag>] [--restart\|--no-restart]` | Update `hi`; offer to restart a `hi server` service still on the old version |
| `hi version`, `hi help`          | Version and help                                       |

## hi compute

| Command                                              | Does                                                  |
|------------------------------------------------------|-------------------------------------------------------|
| `hi compute`                                         | Guided menu in a terminal; help otherwise             |
| `hi compute providers`                               | Which providers are signed in, and what is missing     |
| `hi compute hardware [--on <provider>] [--community]` | Hardware, memory, and prices; `--community` lists RunPod Community Cloud |
| `hi compute billing [<account> \| --clear]`          | Who pays on Hugging Face, and the Colab balance       |
| `hi compute up [options]`                            | Start an instance                                      |
| `hi compute run [options] <script.py> [-- args]`     | Run a Python script to completion                      |
| `hi compute run [options] <image> -- <command>`      | Run a container command to completion (Hugging Face)   |
| `hi compute serve [options] <recipe \| owner/repo-GGUF>` | Serve a GGUF model with an OpenAI-compatible API   |
| `hi compute ls`                                      | Everything running, with limits                        |
| `hi compute status <name>`                           | One run or instance in detail                          |
| `hi compute ssh <name> [-- <command>]`               | Shell, or one command                                  |
| `hi compute tunnel <name> <port>[:<local>]`          | Forward a remote port to `127.0.0.1`                   |
| `hi compute logs <name> [--follow] [-n <lines>]`     | Output and setup logs                                  |
| `hi compute wait <name>…`                            | Wait for runs to end; exits with their status          |
| `hi compute stop [<name> \| --all] [--yes]`          | Stop and release; without a name, pick from a list    |
| `hi compute proxy <name>`                            | The SSH transport, for `ProxyCommand` (Colab)          |
| `hi compute requests [<id> [--wait] [--timeout <d>]] [--json]` | Your requests to the hi server; exit 3 pending, 4 denied |
| `hi compute extend <name> <duration> [--reason <r>] [--no-wait]` | Ask the hi server for more time |
| `hi compute live`                                    | Live view of your managed machines                     |

### Options for up, run, and serve

| Option              | up | run | serve | Meaning                                              |
|---------------------|----|-----|-------|------------------------------------------------------|
| `--on <provider>`   | ✓  | ✓   | ✓     | `colab`, `hf`, `runpod`, or `shadeform`              |
| `--gpu <hardware>`  | ✓  | ✓   | ✓     | Hardware name from `hi compute hardware`             |
| `--name <name>`     | ✓  | ✓   | ✓     | Name for later commands                              |
| `--max <duration>`  | ✓  | ✓   | ✓     | Maximum lifetime: hours (`2`, `1.5`), `30m`, `2d`, or `none` (asks first) |
| `--yes`             | ✓  | ✓   | ✓     | Skip the cost confirmation                           |
| `--dry-run`         | ✓  | ✓   | ✓     | Show the request; start nothing                      |
| `--reason <text>`   | ✓  |     | ✓     | Why you need it; approvers see it (managed providers) |
| `--no-wait`         | ✓  |     |       | Return while approval is pending; exit 3 (managed providers) |
| `--namespace <ns>`  | ✓  | ✓   | ✓     | Hugging Face account to bill this once               |
| `--high-mem`        | ✓  | ✓   |       | High-RAM machine (Colab)                             |
| `--image <image>`   | ✓  |     |       | Container image (Hugging Face, RunPod)               |
| `--env KEY=VALUE`   |    | ✓   |       | Environment variable; repeatable                     |
| `--secret KEY`      |    | ✓   |       | Encrypted secret from your shell (Hugging Face)      |
| `--data <org>/<name>[/<pattern>]` | ✓ | ✓ | | run: the team's data into `data/<name>` first. up (RunPod, Shadeform; whole repositories): `HF_ENDPOINT` and `HF_TOKEN` in the machine's shells (`hi data`) |
| `--detach`          |    | ✓   |       | Return after starting (Hugging Face)                 |
| `--quant <quant>`   |    |     | ✓     | GGUF quantization, such as `Q4_K_M`                  |
| `--ctx <tokens>`    |    |     | ✓     | Context length                                       |
| `--alias <id>`      |    |     | ✓     | Model name in the API                                |
| `--args "<args>"`   |    |     | ✓     | Extra `llama-server` arguments                       |
| `--port <port>`     |    |     | ✓     | Local port for the Colab tunnel; default 8080        |

Defaults: `--gpu` is the provider's cheapest CPU; `--max` is `4h` for `up` and
`serve` and `1h` for `run`, at most `24h` on Colab. Flags may come before or
after names for `logs`, `serve`, `stop`, and `billing`.

## hi connect and hi server

See [Managed compute for a team](/guide/compute/managed/).

| Command                                             | Does                                                 |
|-----------------------------------------------------|------------------------------------------------------|
| `hi connect <server> [--user <name>] [--no-wait]`   | Enroll this device and wait for approval             |
| `hi connect <server> --agent <name> --owner <user>` | Enroll an agent that runs on its own                 |
| `hi connect status`                                 | Server, user, group, and managed providers           |
| `hi connect key`                                    | This device's public key, for an admin to pre-approve |
| `hi connect slack <code>`                           | Link your Slack account, with the code from `/hi link` |
| `hi disconnect`                                     | Forget the server and delete the device key          |
| `hi server init [--listen <addr>]`                  | Create the server's state                            |
| `hi server run`                                     | Serve clients                                        |
| `hi server provider add\|remove\|list [<provider>]` | Provider keys (RunPod)                              |
| `hi server user add <u> --group <g> [--key <k>]`    | Add a user; `--key` pre-approves a device            |
| `hi server user remove <u>`, `user list`            | Remove a user and their devices; list users          |
| `hi server requests [--all]`                        | What is waiting for a decision                       |
| `hi server approve <id> [--group <g>]`              | Approve an enrollment or compute request             |
| `hi server deny <id> [--reason <r>]`                | Deny a request                                       |
| `hi server ls`                                      | Everything running, for every user                   |
| `hi server stop <name> \| --user <u> \| --all`      | Stop instances                                       |
| `hi server audit [--since <d>]`                     | The audit log                                        |
| `hi server slack setup`, `slack manifest`           | Connect a Slack app; print its manifest              |
| `hi server approvers add <id> --name <user>`        | Let a Slack member approve and stop; `remove`, `list` |
| `hi server policy show\|edit\|example\|check`       | Group limits, auto-approve, and budgets              |
| `hi server spend [--since <d>]`                     | Spend per user and group, compute and models         |
| `hi server ai [set [--url <u>] [--model <m>] [--no-key]\|off\|remove]` | Pass `hi q`'s requests to OpenRouter with the team's key |
| `hi server live [--wall] [--names full] [--reasons]` | Live dashboard; `--wall` is read-only               |
| `sudo hi server wall setup\|add\|remove\|list`       | Show the wall on screens over SSH                    |
| `hi server viewer add <name> --key <k>`             | A device that may only watch                         |
| `hi server templates add <name> <git-url> [--ref <r>]` | Serve a private repository's templates and skills |
| `hi server templates list\|sync\|remove [<name>]`    | Show, fetch now, or stop serving template sources  |
| `hi server templates rename <name> <new-name>`      | Rename a source; keeps its token and history         |
| `hi server data`                                    | Menu: Hugging Face organizations for `hi data`       |
| `hi server data add <org>... [--token-file <f>]`    | Serve organizations' datasets, models, and buckets; asks for a read token |
| `hi server data list\|test\|remove [<org>]`          | Show, check, or stop serving organizations           |
| `hi server expose [stop]`                           | The public address cloud runs reach `hi data` through, and the runs using it; close it |
| `hi server expose revoke <run>`                     | End one cloud run's token now                                             |

## hi data

Needs a connected server with organizations added, and `hf`
(`pip install -U huggingface_hub`). The Hugging Face token stays on the
server; `hf` runs with `HF_ENDPOINT` pointing to it.

| Command                                             | Does                                                 |
|-----------------------------------------------------|------------------------------------------------------|
| `hi data`                                           | Search by words, pick a dataset or model, and download it |
| `hi data ls [<org>] [--kind dataset\|model\|bucket] [--json]` | What you may download                   |
| `hi data info <org>/<name>`                         | Size, largest files, and last update                 |
| `hi data get <org>/<name> [--to <dir>]`             | Download it with `hf` (buckets with `hf buckets sync`); default folder `./data/<name>` |
| `  --revision <rev>`, `--include <glob>`, `--exclude <glob>` | A branch, tag, or commit; filters, repeatable |
| `  --no-record`                                     | Don't record it in the project's `.hifin/data.json`  |
| `hi data get`                                       | In a project: everything `.hifin/data.json` records, at the recorded commits |
| `hi data run -- <command>`                          | Run a command that reads the team's data directly    |
| `hi data env`                                       | `HF_ENDPOINT` and `HF_TOKEN` for `eval` in a shell    |

Put `dataset:`, `model:`, or `bucket:` in front when several share a name.
Buckets have no revisions. A group's
`data` patterns in policy (`["hifinab/*"]`) limit what it sees; without
them it sees everything.

## hi login

```sh
hi login hf       # hf auth login, then hf auth whoami
hi login colab    # the Colab CLI's browser sign-in, then the balance
hi login runpod   # asks for the API key, checks it, saves it in ~/.runpod/config.toml
hi login shadeform # asks for the API key, checks it, saves it in ~/.config/hi/shadeform_key
```

## hi init

```sh
hi init                          # choose a template, name, and directory
hi init python pricing-tools     # create ./pricing-tools from the python template
hi init web . --name dashboard   # apply the web template to this directory
hi init --list                   # the templates
hi init --update                 # bring this repository up to the current templates
hi init --update --check         # change nothing; fail when it is behind (for CI)
hi init --adopt python           # bring this existing repository under a template
```

| Option                  | Meaning                                              |
|-------------------------|------------------------------------------------------|
| `--name <name>`         | Project name; by default the directory's name        |
| `--dry-run`             | Print the plan; write nothing                        |
| `--yes`                 | Do not ask for confirmation                          |
| `--no-setup`            | Write the files; run no setup command                |
| `--github <owner/repo>` | Also create a private GitHub repository and push     |
| `--force`               | With `--update` or `--adopt`: overwrite files hi owns even when edited by hand |
| `--strict`              | With `--check`: fail when private layers can't be checked |

Templates: `python`, `web`, `service`, `pipeline`, `ml`, and on a connected
device the team's private ones.

## hi q

| Command                          | Does                                                         |
|----------------------------------|--------------------------------------------------------------|
| `hi q`                           | Chat: ask, run, and follow up; `/clear`, `/context`, `/model`, `/exit` or Ctrl-D |
| `hi q <what you want>`           | Propose one command; Enter runs it, e edits, c copies, ? explains, Esc cancels |
| `hi q -c [question]`             | Continue the last conversation                               |
| `<cmd> \| hi q <question>`       | Ask about piped output, up to 32 KB                          |
| `hi q --explain '<command>'`     | Explain a command without running it                         |
| `hi q --print <question>`        | Print only the command, for scripts                          |
| `hi q --setup`                   | Choose the model (Claude Code, OpenRouter, an OpenAI-compatible endpoint, or an Anthropic key), and set up the shell |
| `hi q --status`                  | The model in use, where it came from, and the shell integration |
| `hi q --context`                 | What is sent with each question                              |
| `hi shell-init bash\|zsh`        | The shell integration script, for `eval` in an rc file       |

Everything from the first word that doesn't start with `-` is the
question, so `hi q status of the log file` asks the model. hi's own options
go before it; put `--` before a question that starts with a dash.

Options: `--provider server|openai|openrouter|anthropic|claude` and `--model <name>`
for one question or chat, `--no-context` to send only the question, and
`--yes` to run read-only commands without asking. Dangerous commands always
need `yes` typed. See [Ask for a command](/guide/q/).

## hi agent

| Command                                  | Does                                                     |
|------------------------------------------|----------------------------------------------------------|
| `hi agent [claude\|codex] "<task>"`      | Run a task in a box on a new worktree (outside git, in the folder itself) and wait for the report; without a name, the first agent installed and signed in |
| `hi agent [claude\|codex] <brief.md>`    | The task is the file's contents; `-` reads it from stdin |
| `hi agent claude\|codex`                 | An interactive session in a box, without permission prompts |
| `hi agent wait <name> [--json]`          | Wait for a run and print its report                      |
| `hi agent token claude`                  | Store a long-lived token from `claude setup-token`       |

Options: `--detach` to start and return, `--json` for the report as JSON on
stdout, `--task-file <path>` for a task file whose name doesn't end in
`.md`, `--model <model>` for the agent's model (without it, the one in your
own Claude Code or Codex settings, else `opus` for Claude Code),
`--bundle a,b` for bundles of skills and the tools they need, and the box
options below. A brief's front matter can set `agent`, `model`, `bundles`,
`network`, `allow`, `data`, and `gpu`. Exits with 0 when the agent is done and
1 when it failed. See [Hand tasks to agents](/guide/agent/).

## hi box

| Command                              | Does                                                     |
|--------------------------------------|----------------------------------------------------------|
| `hi box shell`                       | A shell in a box for this project                        |
| `hi box run -- <command>`            | One command; exits with its status                       |
| `hi box ls`                          | Boxes, state, branch, and changes                        |
| `hi box attach <name>`               | Follow an agent, or take over a box's terminal           |
| `hi box diff <name> [--full]`        | Commits and files, flagging files that run on the host   |
| `hi box allow <name> [<domain>]`     | Allow a domain, or list what was refused                 |
| `hi box stop\|rm <name> [--force]`   | Stop, or remove the box and its worktree                 |
| `hi box rm --all [--force] [--yes]`  | Remove every box after one question                      |

Options when starting: `--name`, `--network locked|dev|open`,
`--allow <domain>`, `--gpu`, `--data` (the team's data through the hi server),
`--worktree`, `--here`, `--image`, `--memory`, and `--bundle a,b`. A
project's `devcontainer.json` can set `network`, `domains`, `gpu`, `data`,
and `bundles` under `customizations.hi`. See
[Run code in a box](/guide/box/).

## hi bundle

| Command                              | Does                                                     |
|--------------------------------------|----------------------------------------------------------|
| `hi bundle ls [--json]`              | Bundles for `--bundle`, and where each comes from        |
| `hi bundle show <name>`              | Its skills, at which commit, what each needs, and its network |
| `hi bundle prune [--all]`            | Remove bundle images unused for 30 days (`--all`: every unused one) |

Bundles come from hi itself, the team's template sources on a hi server,
and `~/.local/share/hi/bundles` (or `HI_BUNDLES_DIR`); a later one wins on
a name. See [Bundles](/guide/agent/#bundles-skills-and-the-tools-they-need).

## hi skill

| Command                              | Does                                                     |
|--------------------------------------|----------------------------------------------------------|
| `hi skills`                          | In a terminal: search skills.sh, browse, and pick        |
| `hi skill find <query>`              | Search skills.sh: installs, makers, and audits           |
| `hi skill add <source>`              | Install from `owner/repo`, `owner/repo/skill`, a git URL, or a folder |
| `hi skill add hi`                    | Write the `hi` skill (plain `hi skill` does too, without a terminal) |
| `hi skill ls`                        | Installed skills, their source, commit, and state        |
| `hi skill show <name\|source>`       | Description, license, files, and audits                  |
| `hi skill update [name…] [--check]`  | Move skills to their source's newest commit              |
| `hi skill update --bundles`          | Move the skills in `bundles/<name>.json` to newer commits |
| `hi skill rm <name>…`                | Remove skills                                            |
| `hi skill --print`                   | Print the `hi` skill; write nothing                      |

Options: `--global` (your home folder), `--skill a,b`, `--yes`,
`--accept-risk`, `--ref`, `--force`, and `--json`. `hi skills` is the same
command. See [Skills](/guide/reference/skill/).

## hi install and hi verify

```sh
hi install                     # menu: check to install, uncheck to remove
hi install --list              # tool names, and which are installed
hi install claude gh           # install or reinstall the named tools
hi install --all [strix]       # every tool; Strix Halo support only when named
hi install strix               # the menu with Strix Halo support checked
hi uninstall docker            # remove the named tools
hi verify strix                # the Strix report again, after a reboot
```

## hi net

```sh
hi net                                   # enroll; prompts for the key
hi net --setup-key-file <file>           # enroll from a file only you can read
HI_NETBIRD_SETUP_KEY=… hi net            # enroll from the environment
hi net status | down | reconnect
```

| Command                                             | Does                                                 |
|-----------------------------------------------------|------------------------------------------------------|
| `hi net expose <port>`                              | Expose `localhost:<port>` until `--max` or Ctrl+C    |
| `  --max <duration>`                                | How long; default `1h`, at most `24h`                |
| `  --password <text>`, `--pin <6 digits>`, `--groups <g1,g2>`, `--public` | The lock: a generated password by default |
| `  --name <prefix>`, `--host <addr>`                | The start of the address; where the service listens  |
| `  --detach`, `--json`                              | Run in the background; print JSON                    |
| `hi net expose ls [--json]`                         | What this machine is exposing                        |
| `hi net expose stop <name> \| --all`                | Stop it now                                          |

See [Share a local service](/guide/workstation/expose/).

## hi adduser

```sh
hi adduser <name>    # Ubuntu adduser, plus the render and video groups
```

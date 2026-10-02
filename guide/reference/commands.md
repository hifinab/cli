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
| `hi server …`                    | Run the server that brokers compute; see below         |
| `hi init [<template> <dir>]`     | Start a project from a template; `--update`, `--adopt` |
| `hi q [<what you want>]`         | Ask for a shell command, or chat; see below            |
| `hi shell-init bash\|zsh`        | Shell integration for `hi q`                           |
| `hi skill`                       | Write the agent skill                                  |
| `hi install`                     | Choose workstation software from a menu                |
| `hi uninstall <tool>…`           | Remove workstation software                            |
| `hi verify strix`                | Check a Strix Halo workstation                         |
| `hi net`                         | Enroll in NetBird; `status`, `down`, `reconnect`       |
| `hi adduser <name>`              | Create a user with GPU access                          |
| `hi update [--check] [--version <tag>]` | Update `hi` to the latest or a given release     |
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

## hi skill

| Option       | Meaning                                           |
|--------------|---------------------------------------------------|
| (none)       | Write into the current folder                     |
| `--global`   | Write into your home folder                       |
| `--print`    | Print the skill; write nothing                    |
| `--force`    | Replace a `hi` skill that `hi` did not write      |

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

## hi adduser

```sh
hi adduser <name>    # Ubuntu adduser, plus the render and video groups
```

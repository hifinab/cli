---
title: All commands
description: Every hi command and option in one place.
---

## Overview

| Command                          | Does                                                   |
|----------------------------------|--------------------------------------------------------|
| `hi compute …`                   | Rent and use remote machines; see below                |
| `hi login <hf\|colab\|runpod>`   | Sign in to a compute provider                          |
| `hi connect <server>`            | Join a hi server; `status`, `key`                      |
| `hi disconnect`                  | Leave it; use your own keys again                      |
| `hi server …`                    | Run the server that brokers compute; see below         |
| `hi skill`                       | Write the agent skill                                  |
| `hi install [strix]`             | Install the workstation software                       |
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
| `hi compute hardware [--on <provider>]`              | Hardware, memory, and prices                           |
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

### Options for up, run, and serve

| Option              | up | run | serve | Meaning                                              |
|---------------------|----|-----|-------|------------------------------------------------------|
| `--on <provider>`   | ✓  | ✓   | ✓     | `colab`, `hf`, or `runpod`                           |
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
| `hi connect status`                                 | Server, user, group, and managed providers           |
| `hi connect key`                                    | This device's public key, for an admin to pre-approve |
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

## hi login

```sh
hi login hf       # hf auth login, then hf auth whoami
hi login colab    # the Colab CLI's browser sign-in, then the balance
hi login runpod   # asks for the API key, checks it, saves it in ~/.runpod/config.toml
```

## hi skill

| Option       | Meaning                                           |
|--------------|---------------------------------------------------|
| (none)       | Write into the current folder                     |
| `--global`   | Write into your home folder                       |
| `--print`    | Print the skill; write nothing                    |
| `--force`    | Replace a `hi` skill that `hi` did not write      |

## hi install and hi verify

```sh
hi install           # workstation software
hi install strix     # plus AMD ROCm for Strix Halo
hi verify strix      # the Strix report again, after a reboot
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

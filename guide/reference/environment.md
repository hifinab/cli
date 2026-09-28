---
title: Environment and files
description: The environment variables hi reads and the files it writes.
---

## Environment variables

| Variable                | Used for                                                          |
|-------------------------|-------------------------------------------------------------------|
| `HI_COMPUTE_PROVIDER`   | Default provider for `hi compute`: `colab` or `hf`                |
| `HI_HF_NAMESPACE`       | Hugging Face account to bill; overrides `hi compute billing`      |
| `HF_TOKEN`              | Hugging Face token; otherwise read from the token file            |
| `HF_TOKEN_PATH`         | Where to find the Hugging Face token file                         |
| `HF_HOME`               | Hugging Face folder; the token is `$HF_HOME/token`                |
| `HF_ENDPOINT`           | Another Hugging Face API endpoint                                 |
| `HI_NETBIRD_SETUP_KEY`  | Setup key for `hi net` in scripts                                 |
| `HI_INSTALL_DIR`        | Installer: where to put `hi`                                      |
| `HI_VERSION`            | Installer: which release to install                               |
| `XDG_CONFIG_HOME`       | Base of `hi`'s config folder; default `~/.config`                 |
| `XDG_STATE_HOME`        | Base of `hi`'s state folder; default `~/.local/state`             |

## Files hi writes

| Path                                        | Contents                                             |
|---------------------------------------------|------------------------------------------------------|
| `~/.local/bin/hi`                           | The `hi` binary                                      |
| `~/.config/hi/compute.json`                 | The account chosen with `hi compute billing`         |
| `~/.local/state/hi/compute/instances.json`  | Instances `hi` started: name, provider, start, limit |
| `~/.local/state/hi/compute/<name>.watch.log` | Log of the Colab lifetime watcher                   |
| `.agents/skills/hi/SKILL.md`, `.claude/skills/hi` | The agent skill, from `hi skill`               |

## Files hi reads from other tools

| Path                                        | Owner                                                |
|---------------------------------------------|------------------------------------------------------|
| `~/.cache/huggingface/token`                | The `hf` CLI's sign-in                               |
| `~/.config/colab-cli/token.json`            | The Colab CLI's sign-in; `hi` only checks it exists  |
| `~/.ssh/id_ed25519`, `~/.ssh/id_ecdsa`      | Your SSH key, for shells and tunnels                 |

`hi` never copies, prints, or stores provider tokens.

## On remote machines

`hi compute serve` keeps its state on the machine in `~/.hi/state` and its
logs in `~/.hi/logs/`, which `hi compute logs` shows. On Colab, the model and
the llama.cpp build live in `/content/hi/`.

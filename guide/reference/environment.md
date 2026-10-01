---
title: Environment and files
description: The environment variables hi reads and the files it writes.
---

## Environment variables

| Variable                | Used for                                                          |
|-------------------------|-------------------------------------------------------------------|
| `HI_COMPUTE_PROVIDER`   | Default provider for `hi compute`: `colab`, `hf`, or `runpod`     |
| `HI_HF_NAMESPACE`       | Hugging Face account to bill; overrides `hi compute billing`      |
| `HF_TOKEN`              | Hugging Face token; otherwise read from the token file            |
| `HF_TOKEN_PATH`         | Where to find the Hugging Face token file                         |
| `HF_HOME`               | Hugging Face folder; the token is `$HF_HOME/token`                |
| `HF_ENDPOINT`           | Another Hugging Face API endpoint                                 |
| `RUNPOD_API_KEY`        | RunPod API key; otherwise read from `~/.runpod/config.toml`       |
| `HI_NETBIRD_SETUP_KEY`  | Setup key for `hi net` in scripts                                 |
| `HI_TEMPLATES_DIR`      | `hi init`: a folder of extra template layers, for template authors |
| `HI_INSTALL_DIR`        | Installer: where to put `hi`                                      |
| `HI_VERSION`            | Installer: which release to install                               |
| `XDG_CONFIG_HOME`       | Base of `hi`'s config folder; default `~/.config`                 |
| `XDG_STATE_HOME`        | Base of `hi`'s state folder; default `~/.local/state`             |
| `XDG_CACHE_HOME`        | Base of `hi`'s cache folder; default `~/.cache`                   |

## Files hi writes

| Path                                        | Contents                                             |
|---------------------------------------------|------------------------------------------------------|
| `~/.local/bin/hi`                           | The `hi` binary                                      |
| `~/.config/hi/compute.json`                 | The account chosen with `hi compute billing`         |
| `~/.runpod/config.toml`                     | The RunPod API key from `hi login runpod` (`apiKey`, mode 0600), shared with runpodctl |
| `~/.local/state/hi/compute/instances.json`  | Instances `hi` started: name, provider, start, limit |
| `~/.local/state/hi/compute/<name>.watch.log` | Log of the Colab lifetime watcher                   |
| `.agents/skills/hi/SKILL.md`, `.claude/skills/hi` | The agent skill, from `hi skill`               |
| `~/.config/hi/shadeform_key`                | The Shadeform API key from `hi login shadeform` (mode 0600) |
| `~/.config/hi/server.json`, `~/.config/hi/device_key` | The hi server this device joined, its stored key, and this device's private key (`hi connect`) |
| `~/.cache/hi/templates/<source>/<commit>/`  | Server templates, cached after their signature was checked; deleted by `hi disconnect` |
| `.hifin/template.json` (in a project)       | What `hi init` generated: template, versions or commits, and file hashes; no paths or names |

## Files hi reads from other tools

| Path                                        | Owner                                                |
|---------------------------------------------|------------------------------------------------------|
| `~/.cache/huggingface/token`                | The `hf` CLI's sign-in                               |
| `~/.config/colab-cli/token.json`            | The Colab CLI's sign-in; `hi` only checks it exists  |
| `~/.runpod/config.toml`                     | runpodctl's `apiKey`                                 |
| `~/.ssh/id_ed25519`, `~/.ssh/id_ecdsa`      | Your SSH key, for shells and tunnels                 |

`hi` never prints provider tokens. It stores them only for providers without
a sign-in tool of their own: RunPod, in RunPod's standard file, and
Shadeform, in `~/.config/hi/shadeform_key`.

## On a hi server

The server keeps everything in its state folder (`hi server --dir`, default
`~/.local/state/hi/server`), readable only by its account:

| File                       | Contents                                                       |
|----------------------------|----------------------------------------------------------------|
| `config.json`              | The listen address and server settings                         |
| `state.json`               | Users, devices, requests, leases, and template sources         |
| `keys.json`                | Provider keys and template source tokens                       |
| `server_key`               | The key the server signs template bundles with                 |
| `policy.json`              | Groups, limits, budgets, and template access                   |
| `audit.jsonl`              | Every request, decision, start, stop, and template change      |
| `templates/<source>.git`   | Mirrors of the template sources                                |

## On remote machines

`hi compute serve` keeps its state on the machine in `~/.hi/state` and its
logs in `~/.hi/logs/`, which `hi compute logs` shows. On Colab, the model and
the llama.cpp build live in `/content/hi/`.

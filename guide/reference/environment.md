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
| `HF_ENDPOINT`           | Another Hugging Face API endpoint; `hi data run` and `env` set it to the server's proxy |
| `HI_DATA_ENDPOINT`, `HI_DATA_TOKEN` | In a `hi compute run --data` job: the server's public data proxy and the run token (an encrypted secret); the script gets them as `HF_ENDPOINT` and `HF_TOKEN` |
| `HI_BOX`                | Set inside a `hi box`; with `--data`, the box also has `HF_ENDPOINT` and a placeholder `HF_TOKEN` |
| `RUNPOD_API_KEY`        | RunPod API key; otherwise read from `~/.runpod/config.toml`       |
| `HI_NETBIRD_SETUP_KEY`  | Setup key for `hi net` in scripts                                 |
| `HI_Q_BASE_URL`, `HI_Q_MODEL`, `HI_Q_API_KEY` | `hi q`: an OpenAI-compatible endpoint, its model, and key |
| `OPENAI_API_KEY`, `OPENAI_BASE_URL` | `hi q`: OpenAI, or another endpoint with an OpenAI key |
| `OPENROUTER_API_KEY`    | `hi q`: OpenRouter                                                |
| `ANTHROPIC_API_KEY`     | `hi q`: the Anthropic API                                         |
| `SHELL`, `HISTFILE`     | `hi q`: the shell that runs commands, and its history file        |
| `HI_Q_STATE`, `HI_Q_SHELL`, `HI_Q_PID` | Set by the shell integration; `hi q` trusts the state file only inside `~/.local/state/hi/q/` |
| `TMUX`, `TMUX_PANE`     | `hi q`: inside tmux, the pane's last 100 lines are context        |
| `VISUAL`, `EDITOR`      | `hi q`: the editor behind `e`                                     |
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
| `~/.config/hi/q.json`, `~/.config/hi/q-key` | The model chosen with `hi q --setup`, and its key (mode 0600) |
| `~/.local/state/hi/q/log.jsonl`             | Commands `hi q` ran: time, folder, prompt, class, exit status; no output |
| `~/.local/state/hi/q/server.json`           | Whether the connected hi server serves a model, checked at most hourly, and a failure for five minutes |
| `~/.local/state/hi/q/notes.json`            | For each `.hifin/q.md`, the content you allowed or refused |
| `.hifin/q.md` (in a project)                | Notes for `hi q`, used after you allow them             |
| `~/.local/state/hi/box/<name>/`             | A box: `box.json`, its worktree (`work/`), home folder, allowlist (`allow`), and `network.log` |
| `~/.local/state/hi/box/domains.json`        | Projects' extra domains you allowed or refused          |
| `~/.config/hi/box-claude-token`             | The token from `hi box token claude` (mode 0600); only the box proxy reads it |
| `~/.local/state/hi/q/last.json`             | The last conversation, for `hi q -c`; mode 0600       |
| `~/.local/state/hi/q/shell-<pid>`           | With shell integration: that shell's last 30 commands and exit status, and commands handed between it and `hi q`; removed after the shell exits |
| `~/.bashrc`, `~/.zshrc`                     | Two lines for the shell integration, added by `hi q --setup` after asking |
| `~/.cache/hi/templates/<source>/<commit>/`  | Server templates, cached after their signature was checked; deleted by `hi disconnect` |
| `.hifin/template.json` (in a project)       | What `hi init` generated: template, versions or commits, and file hashes; no paths or names |
| `docs/upgrades/<version>.md` (in a project) | From `hi init --update`: template changes to the project's own files, for an agent to apply; delete it afterward |

In a project, `hi data get` records its downloads in `.hifin/data.json`:
each repository, the commit, the folder, and the filters. Commit it with
the code that uses the data.

## Files hi reads from other tools

| Path                                        | Owner                                                |
|---------------------------------------------|------------------------------------------------------|
| `~/.cache/huggingface/token`                | The `hf` CLI's sign-in                               |
| `~/.config/colab-cli/token.json`            | The Colab CLI's sign-in; `hi` only checks it exists  |
| `~/.runpod/config.toml`                     | runpodctl's `apiKey`                                 |
| `~/.ssh/id_ed25519`, `~/.ssh/id_ecdsa`      | Your SSH key, for shells and tunnels                 |

`hi` never prints provider tokens. It stores them only for providers without
a sign-in tool of their own: RunPod, in RunPod's standard file,
Shadeform, in `~/.config/hi/shadeform_key`, and the `hi q` model's key, in
`~/.config/hi/q-key`.

## On a hi server

The server keeps everything in its state folder (`hi server --dir`, default
`~/.local/state/hi/server`), readable only by its account:

| File                       | Contents                                                       |
|----------------------------|----------------------------------------------------------------|
| `config.json`              | The listen address and server settings, such as `expose_listen` |
| `state.json`               | Users, devices, requests, leases, and template sources         |
| `keys.json`                | Provider keys, the model key (`ai`), template source tokens, and Hugging Face tokens (`data:<org>`) |
| `data_usage.jsonl`         | One line per `hi data` download or Xet grant: user, device, repository, file, size |
| `ai.json`                  | `hi server ai`: the upstream URL, default model, keyless or not, and on or off |
| `ai_usage.jsonl`           | One line per model request: user, device, model, tokens, cost, status; no messages |
| `server_key`               | The key the server signs template bundles with                 |
| `policy.json`              | Groups, limits, budgets, template access, and data patterns    |
| `audit.jsonl`              | Every request, decision, start, stop, and template change      |
| `templates/<source>.git`   | Mirrors of the template sources                                |

## On remote machines

`hi compute serve` keeps its state on the machine in `~/.hi/state` and its
logs in `~/.hi/logs/`, which `hi compute logs` shows. On Colab, the model and
the llama.cpp build live in `/content/hi/`.

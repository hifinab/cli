# `hi compute` specification

Status: Draft

Dependencies: `hi login` delegation; the provider layer from
[hi_job.md](../approved/hi_job.md); each provider's native CLI; OpenSSH;
the recipe catalog from [hi_model.md](hi_model.md) for `serve`.

## Goal

Rent an interactive remote machine, use it as if it were local, and give it
back, with the same commands on every provider. Colleagues should not have to
learn Hugging Face, Modal, Colab, RunPod, and SkyPilot separately just to get
a GPU, open a shell on it, reach a port from their laptop, or try a model.

`hi job` covers work that runs to completion and exits. `hi compute` covers
machines that stay up until you are done with them: notebooks, debugging
sessions, model servers, and experiments.

As with `hi job` and `hi login`, `hi` composes native commands. Provider CLIs
keep authentication, billing, scheduling, and the real instance state.

## Commands

```text
hi compute                              Step-by-step TUI over everything below
hi compute <file.yaml>                  Start what a compute file describes
hi compute templates                    List ready-made compute templates
hi compute new <template> [<file>]      Write a template to a compute file to edit
hi compute providers                    Installed, authenticated, and capable providers
hi compute gpus [--on <provider>]       Hardware flavors and hourly prices
hi compute up [<file.yaml>] [options]   Start an instance
hi compute ls                           Instances started through hi, with cost so far
hi compute ssh <name> [-- <command>]    Open a shell or run one command
hi compute tunnel <name> <port>[:<local>]
                                        Forward a remote port to localhost
hi compute push <name> <path> [<dest>]  Copy files to the instance
hi compute pull <name> <path> [<dest>]  Copy files back
hi compute serve [<recipe>] [options]   Start an instance, serve a model, tunnel it
hi compute down <name> | --all          Stop and release instances
```

`up` and `serve` options:

```text
--on <provider>      hf, modal, colab, runpod, sky, ...; default from config
--gpu <flavor>       Provider flavor, or a generic size such as 24gb or a100
--name <name>        Local handle; generated when omitted
--image <image>      Container image where the provider supports one
--idle <duration>    Stop after this long without SSH, tunnel, or HTTP traffic
--max <duration>     Hard lifetime limit, always enforced
--set <key>=<value>  Override any compute-file field, such as ports.0=8080
--yes                Skip the cost confirmation
--dry-run            Print the resolved setup and native commands; start nothing
```

## Three ways in

Every `up` and `serve` is resolved from the same settings, which can come from
three places. They mix freely, and the result is identical whichever way the
settings were supplied.

1. **Guided TUI.** Bare `hi compute` walks through each decision step by step.
2. **Flags.** `hi compute up --on hf --gpu a10g-small --idle 30m` for quick,
   scriptable one-liners.
3. **Compute files.** `hi compute modal.yaml` for setups too involved for
   flags: images, setup commands, volumes, secrets, several ports, a model to
   serve.

Precedence, highest first: flags and `--set`, then the compute file, then
`~/.config/hi/compute.toml` defaults, then provider defaults. When required
settings are still missing and a terminal is attached, the TUI opens at the
first missing step with everything else prefilled. Without a terminal, `hi`
never prompts; it fails and lists the missing settings and the flags that
supply them.

### Guided TUI

Bare `hi compute` starts with running instances, if any, so the first choice
is to reconnect, tunnel, or stop something already costing money. Starting
something new then goes step by step:

1. **What for:** shell, serve a model, notebook, or start from a template or
   compute file.
2. **Provider:** only installed providers are selectable; unauthenticated ones
   offer the `hi login` step inline.
3. **Hardware:** flavors the provider offers, with memory and hourly price,
   filtered to what the chosen model or template needs.
4. **Image and setup:** a sensible default per purpose, or a custom image.
5. **Limits:** idle and maximum lifetime, prefilled from config.
6. **Review:** the full setup, estimated cost per hour and at the maximum
   lifetime, and the equivalent flag command and compute file.

The review step offers **Start**, **Save as compute file**, **Copy command**,
and **Back**. Saving writes the same YAML a template would, so a setup built
once in the TUI becomes a file that can be committed and shared. Every screen
works with arrow keys and Enter, shows its keyboard shortcuts, and supports
Esc to go back. Printing the flag command at the end teaches the non-TUI form.

### Compute files

A compute file is versioned YAML describing one instance. Only `provider` and
`hardware` are required; everything else has defaults.

```yaml
# modal.yaml
version: 1
name: qwen-playground
provider: modal
hardware:
  gpu: a100-40gb
image: vllm/vllm-openai:v0.10.0
limits:
  idle: 30m
  max: 4h
env:
  HF_HUB_ENABLE_HF_TRANSFER: "1"
secrets:
  - HF_TOKEN            # names only; values come from the local environment
volumes:
  - name: hf-cache
    path: /root/.cache/huggingface
setup:
  - pip install hf_transfer
serve:
  recipe: qwen3-8b      # or command: [vllm, serve, Qwen/Qwen3-8B]
  port: 8000
  health: /v1/models
tunnels:
  - 8000
sync:
  push:
    - ./prompts:/workspace/prompts
provider_options:       # passed through to this provider only
  modal:
    region: us-east
```

Rules:

- Secret values never appear in a compute file. `hi` rejects files containing
  keys that look like credentials.
- `provider_options` is the escape hatch for provider features `hi` does not
  model. Fields for other providers are ignored, so one file can carry options
  for several providers and switch with `--on`.
- Fields a provider cannot honor fail validation before anything starts.
- `hi compute <file> --dry-run` prints the fully resolved setup and the
  native commands, which makes files easy to review in pull requests.
- A JSON Schema is published with each release for editor completion and
  validation.

### Templates

Ready-made compute files ship with `hi` for common tasks:

| Template           | What it sets up                                               |
|--------------------|---------------------------------------------------------------|
| `shell-gpu`        | A plain GPU box with SSH and the repo pushed to it            |
| `serve-vllm`       | An OpenAI-compatible vLLM server tunnelled to localhost        |
| `serve-llama-cpp`  | A llama.cpp server for GGUF models on smaller GPUs            |
| `jupyter`          | JupyterLab tunnelled to localhost with a persistent volume    |
| `finetune-unsloth` | An Unsloth fine-tuning environment with a dataset volume      |
| `colab-playground` | A Colab runtime with the playground notebook tools            |

`hi compute templates` lists them with the providers each supports.
`hi compute new serve-vllm qwen.yaml` writes a template out for editing, with
comments explaining every field. `hi compute serve-vllm` runs a template
directly with its defaults and opens the TUI for anything it cannot default,
such as which model to serve.

Teams can add their own templates by placing compute files in
`~/.config/hi/compute/templates/` or in a project's `.hi/compute/` directory;
project templates take precedence and are listed first.

## Providers

Each provider declares which capabilities it supports. `hi` rejects a command
the provider cannot honor instead of approximating it.

| Provider        | Native tool            | Shell         | Port tunnel            | Auto-stop           |
|-----------------|------------------------|---------------|------------------------|---------------------|
| Hugging Face    | `hf jobs` (+ `ssh`)    | SSH           | SSH `-L`               | Job timeout         |
| Modal           | `modal`                | `modal shell` | Modal tunnel / web URL | Container timeout   |
| Google Colab    | Colab CLI              | To verify     | To verify              | Colab runtime limits|
| RunPod          | `runpodctl`            | SSH           | SSH `-L` or proxy URL  | `hi` watchdog       |
| SkyPilot        | `sky`                  | SSH           | SSH `-L`               | `sky autostop`      |

SkyPilot also reaches Lambda, AWS, GCP, Azure, and Kubernetes, so it covers
most clouds without separate adapters. Every row must be verified against the
current CLI before it becomes an approved provider.

Instance names are local handles mapped to provider IDs, such as
`hf/68498e23…` or `sky/hi-ana-01`. `hi compute ls` reconciles the local list
with the provider on every call; the provider is the source of truth.

## Connectivity

Preferred order, per instance:

1. **SSH.** `hi` writes a `Host hi-<name>` block to a managed include file
   (`~/.ssh/config.d/hi`) so `ssh hi-<name>`, `scp`, `rsync`, and VS Code
   Remote-SSH work directly. `tunnel` uses `ssh -N -L`.
2. **Provider tunnel.** For providers without SSH, use the provider's own
   port forwarding or HTTPS URL and print it.
3. **NetBird (optional).** `--mesh` enrolls the instance as an ephemeral
   NetBird peer so teammates on the Hifin network reach it by name without
   tunnels. It reuses `hi net`'s key handling; the setup key must be
   single-use and ephemeral.

Tunnels bind to `127.0.0.1` by default. `push` and `pull` use `rsync` over SSH
where available, and the provider's file commands otherwise.

## Serving models

`hi compute serve <recipe>` combines `up`, model download, server start,
health check, and `tunnel`, then prints an OpenAI-compatible base URL on
localhost:

```text
$ hi compute serve qwen3-8b --on hf --gpu a10g-small --idle 30m
Serving qwen3-8b on hf/68498e23… (a10g-small, <price>/h, stops after 30m idle)
OpenAI base URL: http://127.0.0.1:8000/v1
```

Recipes are shared with `hi model`, so the same name serves locally on a Strix
machine or remotely on a rented GPU. The API key, when set, is passed to the
server through the provider's secret mechanism and never printed.

## Cost safety

- Every instance has `--max`; `--idle` defaults to 30 minutes and `--max` to
  4 hours unless configured otherwise.
- `up` and `serve` show provider, flavor, hourly price, and limits, and ask
  for confirmation on paid hardware.
- `hi compute ls` shows running time and estimated cost.
- Any `hi` command prints a one-line reminder while instances started through
  `hi` are still running.
- Where the provider has no native auto-stop, `hi` installs a watchdog on the
  instance that shuts it down after the idle or max limit, so a closed laptop
  does not leave a GPU running.

## Configuration

Defaults live in `~/.config/hi/compute.toml`: default provider, GPU, idle and
max limits, and per-provider settings. Projects keep their setups as compute
files under `.hi/compute/`, which the TUI offers first when run inside the
project.

## Relationship to other commands

- **`hi job`:** shares providers, identifiers, login, and cost confirmation.
  A later version may let `hi job run --on` target a running `hi compute`
  instance.
- **`hi model`:** shares the recipe catalog and health checks; `hi model`
  serves locally, `hi compute serve` remotely.
- **`hi net`:** supplies the optional mesh connectivity.

## Open questions

1. Which Colab CLI to support, and whether it offers SSH, port forwarding, or
   only notebook execution.
2. Whether `hi job` and `hi compute` should merge into one command with
   `run` and `up` subcommands.
3. Shared team defaults and budgets: per-user config only, or a checked-in
   team config with spend limits.
4. How to price generic sizes such as `--gpu 24gb` across providers without
   silently picking an expensive flavor.
5. Whether the watchdog should be opt-out, given it runs code on the
   instance.
6. TUI library: a Go framework such as Bubble Tea keeps `hi` a single static
   binary; confirm size and accessibility before choosing.
7. Whether compute files should allow several instances, such as a server
   plus a client, or stay one instance per file.

## Acceptance criteria

1. `hi compute up --on hf --gpu a10g-small` starts an instance, prints its
   name, price, and limits, and `hi compute ssh <name>` opens a shell on it.
2. `hi compute tunnel <name> 8000` makes the remote port reachable only on
   `127.0.0.1:8000`.
3. `hi compute serve <recipe>` ends with a passing health check and a local
   OpenAI-compatible URL.
4. Every instance stops by itself after its idle or max limit, including when
   the laptop is offline.
5. `hi compute ls` and `down` see only instances started through `hi` and agree
   with the provider's own listing.
6. `--dry-run` prints the exact native commands and starts nothing.
7. Credentials and API keys never appear in `hi` output, arguments, or files.
8. Adding a provider requires no change to the `hi compute` command surface.
9. Bare `hi compute` in a terminal guides a first-time user from nothing to a
   running instance without any flags, and its review step shows the
   equivalent flag command and compute file.
10. The same setup started from the TUI, from flags, and from a compute file
    produces identical native commands in `--dry-run`.
11. Without a terminal, missing settings fail with the list of required flags
    instead of prompting.
12. Every built-in template validates, and `hi compute new` writes a file that
    starts successfully after filling only the fields it marks as required.

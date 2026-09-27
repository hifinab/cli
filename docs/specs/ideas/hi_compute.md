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
hi compute                              Interactive menu over the commands below
hi compute providers                    Installed, authenticated, and capable providers
hi compute gpus [--on <provider>]       Hardware flavors and hourly prices
hi compute up [options]                 Start an instance
hi compute ls                           Instances started through hi, with cost so far
hi compute ssh <name> [-- <command>]    Open a shell or run one command
hi compute tunnel <name> <port>[:<local>]
                                        Forward a remote port to localhost
hi compute push <name> <path> [<dest>]  Copy files to the instance
hi compute pull <name> <path> [<dest>]  Copy files back
hi compute serve <recipe> [options]     Start an instance, serve a model, tunnel it
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
--yes                Skip the cost confirmation
--dry-run            Print the native commands without running them
```

Bare `hi compute` opens a small menu: pick a provider, a GPU, and an action,
with running instances listed first. It is the same flow as scripted shell
menus colleagues build today, but backed by the commands above.

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
max limits, and per-provider settings. A project may add a `[compute]` table
to the metadata written by `hi init` for its usual image and GPU.

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

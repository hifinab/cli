# `hi compute` specification

Status: Draft

Dependencies: the provider layer defined here and shared with
[hi_job.md](../approved/hi_job.md); OpenSSH; `hi login`; the recipe catalog
from [hi_model.md](hi_model.md) for `serve`.

## Goal

Rent an interactive remote machine, use it as if it were local, and give it
back, with the same commands on every provider. Colleagues should not have to
learn Hugging Face, Colab, RunPod, and Modal separately just to get a GPU, open
a shell on it, reach a port from their laptop, or try a model.

`hi job` covers work that runs to completion and exits. `hi compute` covers
machines that stay up until you are done with them: debugging sessions, model
servers, notebooks, and experiments.

## Lessons from colab-runner

[quantbert/colab-runner](https://github.com/quantbert/colab-runner) took a
fresh Colab G4 to an OpenAI-compatible Qwen3.8-Flash-Next API on `localhost`
in about seven minutes. It is the prototype for this command, and it shaped
the design:

- **The provider CLI as an SSH `ProxyCommand` is the key trick.**
  `ssh -o ProxyCommand="colab ssh --proxy-mode -s NAME" -L 8080:127.0.0.1:8000`
  gave ordinary port forwarding through Google's own tunnel, with no NetBird,
  cloudflared, or ngrok. `hi` generalizes this: every instance becomes an SSH
  host, whatever the transport underneath.
- **A menu over subcommands works.** The whiptail menu with a plain-prompt
  fallback, where every menu action is also a subcommand with arguments, is
  the model for the TUI and flags below.
- **Remote work needs a small contract.** Long work started over `exec` timed
  out even when it finished. What worked was an idempotent script started in
  the background that writes a one-line state file (`building…`, `ready`,
  `failed: …`) and logs, polled with short, time-limited calls.
- **Provider CLIs are fragile to script.** `colab sessions` output had to be
  parsed by pattern, an unknown `--gpu` silently fell back to an A100, and
  `colab exec` sometimes hung past its own timeout. `hi` validates every input
  before calling out, wraps every call in its own timeout, and prefers APIs
  with structured responses.
- **Cost is not always money.** Colab bills compute units from a prepaid
  balance (a G4 is about 8.9 units/hour); others bill dollars per minute. The
  cost model supports both.
- **Providers have house rules.** Colab allows SSH only on paid tiers, reserves
  port 8080 on the VM, has no `/dev/net/tun`, and bans public web services.
  Provider drivers carry these constraints so `hi` can refuse or adapt.
- **Serving recipes are model × hardware.** Qwen3.8-Flash-Next only fit a G4
  with its 28.8 GB n-gram table pinned to host RAM (`-ot …=CPU`,
  `--lazy-mode off`). Recipes must record hardware-specific flags and the
  measured result, not just a model name.
- **Startup time is mostly setup.** Seven minutes was mostly a llama.cpp build
  and a 90 GB download. Prebuilt images, where the provider allows them, and
  cached builds matter more than instance start time.
- **Guard the destructive paths.** A menu test stopped every session by
  mistake; "stop all" now confirms. After a tunnel closes, colab-runner offers
  to stop the instance so it does not keep billing.

## How far to unify

Unify what is the same everywhere, and stop there:

| Unified by `hi`                                         | Left to each provider                                   |
|---------------------------------------------------------|---------------------------------------------------------|
| Start, list, stop, and limits                           | Authentication and accounts (`hi login` delegates)      |
| SSH access, tunnels, file copy (all over SSH)           | Hardware names; `hi` shows them, never renames them     |
| The remote state contract and serving recipes           | Billing units and prices; shown, not converted          |
| Idle and maximum lifetime, enforced on every provider   | Persistent endpoints, autoscaling, scheduled jobs        |
| Cost confirmation and running-cost display              | Provider extras, through `provider_options`             |

Two things are deliberately not unified. Hardware names stay native
(`a10g-small`, `G4`, `NVIDIA A40`) because a false common name silently picks
different or pricier hardware; `--gpu 24gb` is only a filter that shows which
flavor it resolved to before starting. Persistent, autoscaling endpoints such
as Hugging Face Inference Endpoints or `modal deploy` are a different product
from an interactive box and belong in a later command, if ever.

## Provider integration

Each provider is a driver behind one Go interface. A driver implements only
the provider-specific part:

```text
Hardware()        flavors, memory, and price per hour (money or units)
Create(spec)      start an instance from a resolved setup
Get(id), List()   state, SSH transport, and exposed URLs
Stop(id)          stop and release
Capabilities()    SSH, custom image, public ports, native idle/max limits,
                  reserved ports, policy notes
```

Everything else is built once on top of SSH: shells, tunnels, file copy,
running setup scripts, polling state, and serving.

### API, SDK, or CLI

Prefer the provider's documented API, then an official SDK, and fall back to
its CLI only when nothing else exists. APIs give typed state and stable
errors, need no Python on the workstation, and avoid parsing text that changes
between releases.

| Provider     | Integrate through                                   | SSH transport                                                    | Native limits            |
|--------------|-----------------------------------------------------|------------------------------------------------------------------|--------------------------|
| Hugging Face | Jobs REST API (`huggingface.co/api/jobs`, OpenAPI)  | `ssh <job>@ssh.hf.jobs`; public key registered on the Hub       | Max lifetime (timeout)   |
| Colab        | `colab` CLI (`google-colab-cli`); no public API     | `colab ssh --proxy-mode` as `ProxyCommand`; Pro+ only          | Colab's session limits   |
| RunPod       | REST API (`rest.runpod.io/v1`)                      | Direct SSH on exposed port 22                                    | None; `hi` watchdog      |
| Modal        | Official Go SDK, Sandboxes (beta)                   | No sshd; SDK exec with PTY, or sshd over a raw TCP tunnel       | Idle and max lifetime    |
| Lambda       | REST API (`cloud.lambda.ai/api/v1`, OpenAPI)        | Direct SSH to the VM                                             | None; terminate via API  |
| Vast.ai      | REST API (`console.vast.ai/api/v0`)                 | Direct or proxied SSH                                            | None; `hi` watchdog      |
| SkyPilot     | `sky` CLI; its REST server is not a stable contract | `ssh <cluster>` via its SSH config                               | Idle autostop only       |

On Hugging Face, an interactive box is a Job started with SSH enabled, exposed
ports, and a long-running command, stopped by cancelling the Job. Exposed
ports also get HTTPS URLs (`https://<job>--<port>.hf.jobs`) that require a
Hugging Face token, which suits sharing a served model with a colleague
without a tunnel.

### Credentials

`hi login <provider>` delegates to the provider's own login where one exists
(`hf auth login`, `colab`'s OAuth flow). For API providers, `hi` reads the
token from the provider's standard location or environment variable
(`HF_TOKEN`, then `~/.cache/huggingface/token`; `RUNPOD_API_KEY`; and so on)
in memory for each call. It never copies, prints, or stores it. This relaxes
the `hi login` rule that `hi` never parses tokens, and needs that spec updated
before the first API driver ships.

### Phasing

1. **Hugging Face** through its API. It is already first for `hi job`, bills
   per minute, and has SSH and HTTPS ports.
2. **Colab** through its CLI. The team already has Pro+ units and the proven
   colab-runner flow; this ports it into `hi`.
3. **RunPod** through its API. It has the cheapest broad GPU range and needs
   the `hi` watchdog, which Lambda and Vast reuse later.
4. **Modal** through its Go SDK. It is on the roadmap, but implementation
   waits until the SDK leaves beta and its package path settles.
5. **Lambda, Vast.ai, and SkyPilot** on demand. SkyPilot overlaps with the
   drivers above, so it is only worth adding for clouds `hi` does not cover.

## Commands

```text
hi compute                              Step-by-step TUI over everything below
hi compute <file.yaml>                  Start what a compute file describes
hi compute templates                    List ready-made compute templates
hi compute new <template> [<file>]      Write a template to a compute file to edit
hi compute providers                    Installed, authenticated, and capable providers
hi compute gpus [--on <provider>]       Hardware flavors and prices
hi compute up [<file.yaml>] [options]   Start an instance
hi compute ls                           Instances started through hi, with cost so far
hi compute ssh <name> [-- <command>]    Open a shell or run one command
hi compute tunnel <name> <port>[:<local>]
                                        Forward a remote port to localhost
hi compute push <name> <path> [<dest>]  Copy files to the instance
hi compute pull <name> <path> [<dest>]  Copy files back
hi compute logs <name> [--follow]       Setup and server logs from the state contract
hi compute serve [<recipe>] [options]   Start an instance, serve a model, tunnel it
hi compute down <name> | --all          Stop and release instances
```

`up` and `serve` options:

```text
--on <provider>      hf, colab, runpod, modal, ...; default from config
--gpu <flavor>       Provider flavor, or a size filter such as 24gb
--name <name>        Local handle; generated when omitted
--image <image>      Container image where the provider supports one
--idle <duration>    Stop after this long without SSH, tunnel, or HTTP traffic
--max <duration>     Hard lifetime limit, always enforced
--set <key>=<value>  Override any compute-file field, such as ports.0=8080
--yes                Skip the cost confirmation
--dry-run            Print the resolved setup and provider calls; start nothing
```

Instance names are local handles mapped to provider IDs, such as
`hf/68498e23…` or `colab/qwen`. `hi compute ls` reconciles with the provider on
every call; the provider is the source of truth.

## Three ways in

Every `up` and `serve` is resolved from the same settings, which can come from
three places. They mix freely, and the result is identical whichever way the
settings were supplied.

1. **Guided TUI.** Bare `hi compute` walks through each decision step by step.
2. **Flags.** `hi compute up --on hf --gpu a10g-small --idle 30m` for quick,
   scriptable one-liners.
3. **Compute files.** `hi compute qwen.yaml` for setups too involved for flags:
   images, setup commands, volumes, secrets, several ports, a model to serve.

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
2. **Provider:** only configured providers are selectable; unauthenticated
   ones offer the `hi login` step inline, and missing prerequisites such as an
   SSH key registered on the Hub are explained there.
3. **Hardware:** flavors the provider offers, with memory and price, filtered
   to what the chosen recipe or template needs.
4. **Image and setup:** a sensible default per purpose, or a custom image.
5. **Limits:** idle and maximum lifetime, prefilled from config.
6. **Review:** the full setup, cost per hour and at the maximum lifetime, and
   the equivalent flag command and compute file.

The review step offers **Start**, **Save as compute file**, **Copy command**,
and **Back**. Saving writes the same YAML a template would, so a setup built
once in the TUI becomes a file that can be committed and shared. Every screen
works with arrow keys and Enter, shows its shortcuts, and supports Esc to go
back. Printing the flag command at the end teaches the non-TUI form.

### Compute files

A compute file is versioned YAML describing one instance. Only `provider` and
`hardware` are required; everything else has defaults.

```yaml
# qwen.yaml
version: 1
name: qwen-playground
provider: hf
hardware:
  gpu: rtx-pro-6000
image: ghcr.io/ggml-org/llama.cpp:server-cuda
limits:
  idle: 30m
  max: 4h
env:
  HF_XET_HIGH_PERFORMANCE: "1"
secrets:
  - HF_TOKEN            # names only; values come from the local environment
volumes:
  - name: models
    path: /models
serve:
  recipe: qwen3.8-flash-next
  variant: UD-Q3_K_XL
  port: 8000
tunnels:
  - 8000:8080           # remote:local
sync:
  push:
    - ./prompts:/workspace/prompts
provider_options:       # passed through to this provider only
  colab:
    high_mem: true
```

Rules:

- Secret values never appear in a compute file. `hi` rejects files containing
  keys that look like credentials.
- `provider_options` is the escape hatch for provider features `hi` does not
  model. Options for other providers are ignored, so one file can carry
  options for several providers and switch with `--on`.
- Fields a provider cannot honor fail validation before anything starts, such
  as `image` on Colab or port 8080 where the provider reserves it.
- `--dry-run` prints the fully resolved setup and the provider calls, which
  makes files easy to review in pull requests.
- A JSON Schema is published with each release for editor completion and
  validation.

### Templates

Ready-made compute files ship with `hi` for common tasks:

| Template          | What it sets up                                               |
|-------------------|---------------------------------------------------------------|
| `shell-gpu`       | A plain GPU box with SSH and the current repo pushed to it    |
| `serve-llama-cpp` | A llama.cpp server for GGUF models, tunnelled to localhost    |
| `serve-vllm`      | An OpenAI-compatible vLLM server, tunnelled to localhost      |
| `jupyter`         | JupyterLab tunnelled to localhost with a persistent volume    |

`hi compute templates` lists them with the providers each supports.
`hi compute new serve-vllm qwen.yaml` writes a template out for editing, with
comments explaining every field. `hi compute serve-vllm` runs a template
directly and opens the TUI for anything it cannot default, such as the model.

Teams add their own templates in `~/.config/hi/compute/templates/` or a
project's `.hi/compute/` directory; project templates are listed first.

## Connectivity

Every instance is reachable as an SSH host. `hi` writes a `Host hi-<name>`
block to a managed include file (`~/.ssh/config.d/hi`) whose `ProxyCommand` is
`hi compute proxy <name>`. That proxy picks the provider's transport: a direct
TCP connection, `ssh.hf.jobs`, or `colab ssh --proxy-mode`. Plain `ssh`,
`scp`, `rsync`, and VS Code Remote-SSH then work directly, and `tunnel`,
`push`, and `pull` are ordinary SSH on top.

- Tunnels bind to `127.0.0.1` by default and use keep-alives so an idle
  tunnel is not dropped.
- When a foreground tunnel or `serve` session ends, `hi` asks whether to stop
  the instance and says how to reconnect if not.
- `--mesh` optionally enrolls the instance as an ephemeral NetBird peer so
  teammates reach it by name. It is unavailable where the provider has no
  `/dev/net/tun`, such as Colab, unless NetBird runs in netstack mode.

## Remote state contract

Setup and serving on the instance follow one contract, taken from
colab-runner's `remote/serve.sh`:

- `hi` uploads a script and starts it detached, so a dropped connection or a
  timed-out call never kills it.
- The script is idempotent: rerunning it skips finished steps, and a
  healthy server short-circuits to `ready`.
- It writes one line to `~/.hi/state` (`building`, `downloading 42 GB`,
  `loading`, `ready`, or `failed: <reason>`) and appends logs to `~/.hi/logs/`.
- `hi` polls the state with short, time-limited SSH calls, shows progress
  lines as they change, and treats a failed poll as "no update".
- `hi compute logs` shows these logs, so failures are debuggable without
  opening a shell.

## Serving models

`hi compute serve <recipe>` combines `up`, setup, the health check, and
`tunnel`, then prints an OpenAI-compatible base URL on localhost:

```text
$ hi compute serve qwen3.8-flash-next --on colab --gpu G4
Starting colab/qwen on G4 (~8.9 units/h, stops after 30m idle)
  building llama.cpp and downloading UD-Q3_K_XL
  loading model
OpenAI base URL: http://127.0.0.1:8080/v1   model: qwen3.8-flash-next
```

Recipes are shared with `hi model`, so the same name serves locally on a Strix
machine or remotely. Each recipe lists variants per hardware with their exact
engine flags and, once tested, the measured result:

```yaml
name: qwen3.8-flash-next
source: unsloth/Qwen3.8-Flash-Next-GGUF
engine: llama.cpp
variants:
  - hardware: [colab/G4, hf/rtx-pro-6000]
    quant: UD-Q3_K_XL
    context: 131072
    args: [-ot, 'per_layer_token_embd\.weight=CPU', --lazy-mode, "off",
           --temp, "1.0", --top-p, "0.95", --top-k, "20", --min-p, "0.0"]
    tested: {on: colab/G4, date: 2026-09-27, tokens_per_second: 103,
             vram_gb: 62.9, ready_minutes: 7}
```

`hi compute serve` refuses a recipe with no variant for the chosen hardware
rather than guessing flags. An API key, when set, is passed through the
provider's secret mechanism and never printed.

## Cost safety

- Every instance has a maximum lifetime; `--idle` defaults to 30 minutes and
  `--max` to 4 hours unless configured otherwise.
- `up` and `serve` show provider, flavor, price per hour, and limits, and ask
  for confirmation on paid hardware. Prices come from the provider's API where
  available and are shown in the provider's own unit.
- `hi compute ls` shows running time and estimated cost so far.
- Any `hi` command prints a one-line reminder while instances started through
  `hi` are still running.
- Where the provider has no native limit, `hi` installs a small watchdog on the
  instance that stops it through the provider API after the idle or maximum
  limit, so a closed laptop never leaves a GPU running. `hi compute ls` also
  stops instances whose limits have passed.
- `down --all` lists what it will stop and asks for confirmation.

## Configuration

Defaults live in `~/.config/hi/compute.toml`: default provider, GPU, idle and
maximum limits, and per-provider settings. Projects keep their setups as
compute files under `.hi/compute/`, which the TUI offers first when run inside
the project.

## Relationship to other commands

- **`hi job`:** shares drivers, identifiers, credentials, and cost
  confirmation. A job is an instance that runs one command and stops itself.
- **`hi model`:** shares recipes and health checks; `hi model` serves locally,
  `hi compute serve` remotely.
- **`hi net`:** supplies the optional mesh connectivity.
- **`hi login`:** supplies provider logins; its token rule needs the change
  described under Credentials.

## Open questions

1. Whether `hi job` and `hi compute` should merge into one command with `run`
   and `up` subcommands, now that both share one driver layer.
2. Whether the Hugging Face SSH gateway works on every flavor and forwards
   arbitrary ports; the docs show `ssh -L` but do not list restrictions.
3. How Colab drives setup inside a runtime that cannot run a custom image:
   build on first use and cache on Google Drive, or download prebuilt binaries.
4. Shared team defaults and budgets: per-user config only, or a checked-in
   team config with spend limits.
5. Whether the watchdog should be opt-out, given it runs code on the instance
   with a provider API key that can stop it.
6. TUI library: a Go framework such as Bubble Tea keeps `hi` a single static
   binary; confirm size and accessibility before choosing.
7. Whether compute files should allow several instances, such as a server plus
   a client, or stay one instance per file.

## Acceptance criteria

1. `hi compute up --on hf --gpu a10g-small` starts an instance, prints its
   name, price, and limits, and `hi compute ssh <name>` opens a shell on it.
2. `ssh hi-<name>`, `rsync`, and VS Code Remote-SSH work for every provider
   that supports SSH, without provider-specific setup by the user.
3. `hi compute tunnel <name> 8000` makes the remote port reachable only on
   `127.0.0.1:8000`.
4. `hi compute serve qwen3.8-flash-next --on colab --gpu G4` reproduces the
   colab-runner result: a local OpenAI-compatible URL after a passing health
   check.
5. A dropped connection during setup does not stop setup, and rerunning
   `serve` resumes rather than restarts it.
6. Every instance stops by itself after its idle or maximum limit, including
   when the laptop is offline.
7. `hi compute ls` and `down` see only instances started through `hi` and
   agree with the provider's own listing.
8. An unknown hardware name fails before any provider call.
9. The same setup started from the TUI, from flags, and from a compute file
   produces identical provider calls in `--dry-run`.
10. Without a terminal, missing settings fail with the list of required flags
    instead of prompting.
11. Credentials and API keys never appear in `hi` output, arguments, or files.
12. Adding a provider requires a new driver only, with no change to the
    `hi compute` command surface.

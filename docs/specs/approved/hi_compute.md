# `hi compute` specification

Status: Approved for runs, instances, SSH, tunnels, logs, and serving on
Colab, Hugging Face, and RunPod, in that order. Colab (v0.6.0) and Hugging
Face (v0.7.0) are released, and so is RunPod (v0.8.0).
Everything marked **(draft)** is not yet approved: the full TUI, compute files,
templates, file copy, SSH config integration, idle limits, and further
providers.

Dependencies: provider APIs and SDKs as listed under Provider integration;
the `hf` CLI for login only; `hi login`; `uv` for script runs; OpenSSH for
interactive instances; the recipe catalog from
[hi_model.md](../ideas/hi_model.md) for `serve`.

## Goal

Use rented remote compute from a workstation with one command set on every
provider. Colleagues should not have to learn Hugging Face, Colab, RunPod, and
Modal separately just to run a training script on a GPU, open a shell on one,
reach a port from their laptop, or try a model.

`hi compute` covers two kinds of work with the same commands:

- **Runs** execute one command or script and stop by themselves when it
  finishes: training, evaluation, batch inference, data processing.
  `hi compute run` starts one.
- **Instances** stay up until you stop them or a limit is reached: debugging
  sessions, model servers, notebooks, experiments. `hi compute up` starts one.

Both appear in the same `ls`, share `logs`, `wait`, and `stop`, and obey the
same limits and cost confirmation. A run is an instance whose lifetime is its
command.

The name `compute` was chosen over `job`, which fits only runs, and over
`remote`, `cloud`, and `gpu`, which clash with git and SSH vocabulary, suggest
infrastructure management, or exclude CPU work.

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

## How far to unify (draft)

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
| Shadeform    | REST API (`api.shadeform.ai/v1`, `X-API-KEY`)       | Direct SSH to the VM as `shadeform`; key registered via API      | `auto_delete` at `--max` |
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

`hi install` installs the `hf` CLI. `hi login hf` runs `hf auth login` and
then `hf auth whoami`; `hi login colab` runs `colab usage`, which starts
Colab's browser sign-in. `hi login <provider>` delegates to the
provider's own login where one exists (`hf auth login`, then `hf auth whoami`;
`colab`'s OAuth flow). For API providers, `hi` reads the token from the
provider's standard location or environment variable in memory for each
request, under the exception in [hi_login.md](hi_login.md). It never copies,
prints, or stores it.

- Hugging Face: `HF_TOKEN`, then the file named by `HF_TOKEN_PATH`, then
  `$HF_HOME/token` (default `~/.cache/huggingface/token`), matching
  `huggingface_hub`.
- RunPod: `RUNPOD_API_KEY`, or the key `runpodctl` stores.

`--secret KEY` reads `KEY` from the local environment and sends it in the
provider's encrypted secrets field. The value never appears in `hi` arguments,
output, or files.

### Colab CLI

Colab has no public API, so its driver runs `google-colab-cli`, which
`hi install` installs with `uv tool install`. It is ready when `colab` is on
`PATH` or in `~/.local/bin` and `~/.config/colab-cli/token.json` exists; the
first `colab usage` signs in through a browser.

| `hi compute`   | Colab CLI                                                   |
|----------------|-------------------------------------------------------------|
| `up`           | `colab new -s NAME [--gpu X \| --tpu X] [--high-mem]`       |
| `ls`, `status` | `colab sessions`, parsed only from its documented line format |
| `stop`         | `colab stop -s NAME`, treating "not found" as an error      |
| `run`          | `colab run --timeout SECONDS [--env K=V] script.py args`    |
| `hardware`     | Built-in list with measured rates, plus `colab usage`       |
| SSH transport  | `colab ssh --proxy-mode -s NAME` as `ProxyCommand`          |

Colab-specific rules:

- Hardware is checked against `cpu`, `T4`, `L4`, `G4`, `A100`, `H100`, `v5e1`,
  and `v6e1` before any call, and the started session's hardware is checked
  again afterwards, because the CLI has silently substituted an A100.
- Every CLI call has its own timeout; `colab new` gets 10 minutes.
- Port 8080 on the VM belongs to Colab's proxy and is refused for tunnels.
- `--max` is at most 24 hours, Colab's own session limit.
- `run` accepts Python scripts only. `--secret` is refused, because Colab's
  only way to pass values is `--env` on the command line; `--detach` is
  refused, because `colab run` cannot detach.
- SSH needs Colab Pro or Pro+ and a local `~/.ssh/id_ed25519` or
  `id_ecdsa`; `hi compute providers` says which is missing.
- Colab's terms forbid public web services; tunnels bind to localhost only.

### RunPod REST API v2

RunPod is called through `https://api.runpod.io/v2` with a Bearer API key;
REST v1 and GraphQL are deprecated (v1 retires on 2026-11-15). The key comes
from `RUNPOD_API_KEY`, then `apiKey` in runpodctl's `~/.runpod/config.toml`.

| `hi compute` | RunPod API v2                                                         |
|--------------|-----------------------------------------------------------------------|
| `hardware`   | `GET /catalog/gpus` (secure-cloud price per hour) and `/catalog/cpus` |
| `up`         | `POST /pods` with `gpu` or `cpu`, `ports: ["22/tcp"]`, `startSsh`, and `env.PUBLIC_KEY` set to the user's key |
| `ls`         | `GET /pods`, skipping `TERMINATED`; `env.HI_MANAGED=1` marks hi's pods |
| `ssh`        | `ssh.direct` host and port once the pod is `RUNNING`                  |
| `stop`       | `DELETE /pods/{id}` (terminate); a stopped pod would still bill disk  |

Hardware names are short slugs of RunPod's names (`rtx-4090`, `a100-pcie`);
CPU pods use 2 vCPUs. `up` waits until SSH is reachable, then installs a
watchdog that terminates the pod at `--max` through the pod-scoped
`RUNPOD_API_KEY`; the local watcher also runs, because the pod key's
permissions are undocumented. `run` and `wait` are not supported yet. Errors
map 402 to balance, 400 to no capacity (with up to three free alternatives
of at least the same memory), and 403 to key permissions. The catalog is read
with `include=AVAILABILITY&product=POD&cloud=SECURE`; GPUs with free units are
listed first.

Verified live on 2026-09-28: `up` on an RTX PRO 4000 (about 30 seconds to
SSH), `ssh`, `tunnel`, `ls`, and the watchdog: the pod-scoped key can
terminate its own pod with the bundled (older) `runpodctl remove pod`, and a
CPU pod with `--max 3m` and no local watcher terminated itself on time. The
bundled runpodctl lacks `pod delete`, so the watchdog tries `remove pod`
first. Most cheap secure-cloud GPUs had no capacity during the test.

### Hugging Face Jobs API

Hugging Face is called through its REST API at
`https://huggingface.co/api/jobs` rather than by wrapping the `hf` CLI, so it
needs neither Python nor the CLI once a token exists. The request and
response shapes follow the `huggingface_hub` 1.32 source, and tests pin them
with a fake API.

| `hi compute`      | Hugging Face Jobs API                                            |
|-------------------|------------------------------------------------------------------|
| `run <image>`     | `POST /api/jobs/{namespace}` with `dockerImage` and `command`    |
| `run <script.py>` | Same, with the uv image running the script shipped in the job    |
| `up`              | Same, with SSH enabled and `sleep infinity`                      |
| `serve`           | Same, with llama.cpp's CUDA image running the serve script       |
| `ls`              | `GET /api/jobs/{namespace}`, keeping jobs that have not finished |
| `status`, `wait`  | `GET /api/jobs/{namespace}/{id}` until a final stage             |
| `logs`            | `GET /api/jobs/{namespace}/{id}/logs` (Server-Sent Events)       |
| `stop`            | `POST /api/jobs/{namespace}/{id}/cancel`                         |
| `hardware`        | `GET /api/jobs/hardware`, prices converted to dollars per hour   |

Rules:

- Every job gets `timeoutSeconds` from `--max`, so Hugging Face enforces the
  limit itself and no local watcher runs. Jobs are labelled
  `name=<hi name>` and `managed-by=hi`; `ls`, `stop`, and `logs` find jobs by
  that label or by job ID (`hf/<id>`).
- The account that pays is `--namespace`, then `HI_HF_NAMESPACE`, then the
  account saved with `hi compute billing ACCOUNT` (in
  `~/.config/hi/compute.json`), then the user if `whoami` reports `canPay`,
  then the user's only organization that can pay, else the user. The API
  reports `canPay` per account but not the credit balance. Every paid start
  line names the payer. `hi compute billing` lists the accounts, which can
  pay, and why one was chosen; `--clear` returns to automatic.
- `ls` covers the user, the billed account, `HI_HF_NAMESPACE`, and every
  namespace `hi` started an instance in.
- Scripts are shipped base64-encoded in the job's environment and decoded by
  the job's command, instead of uploading them to a bucket as the `hf` CLI
  does. Scripts are limited to 96 KB.
- `--secret KEY` sends `$KEY` in the job's `secrets` field, which Hugging Face
  encrypts. `--dry-run` shows the request with the script and secret values
  replaced by placeholders, and never contacts the API.
- Jobs map to exit codes: completed is 0, canceled is 130, and a failed job
  returns its own exit code when Hugging Face reports it ("Job failed with
  exit code: 3"), otherwise 1.
- `ls` marks jobs labelled `managed-by=hi` as started by `hi` and shows their
  limit from the job's `createdAt` and `timeout`; `status`, `logs`, and
  `wait` also find finished jobs by name.
- `ssh` waits for the job to run, then connects to the `sshUrl` the API
  reports. It needs an SSH public key registered at
  https://huggingface.co/settings/keys; when the gateway refuses the key, `hi`
  says so.
- HTTP 402 means the namespace has no pre-paid Jobs credits; `hi` says where
  to add them and suggests `--namespace` for an organization.
- Machines above $0.10 per hour ask for confirmation.

`hi compute serve` on Hugging Face starts one job that is the server: the
`ghcr.io/ggml-org/llama.cpp:server-cuda` image runs the serve script in the
foreground, which uses the prebuilt `llama-server` and its `-hf` downloader.
The image has Python but not the `hf` CLI, so a prebuilt server always
downloads with `-hf`. Port 8000 is exposed at
`https://<job>--8000.hf.jobs`, which requires a Hugging Face token with read
access; OpenAI clients pass it as the API key. Progress comes from the
script's `hi-state:` lines in the job logs. `hi compute tunnel` still gives a
localhost URL over SSH.

Verified live on 2026-09-27 in the `hifinab` organization: image and script
runs with arguments, environment, secrets, and exit codes; `--detach`,
`logs --follow`, `wait`, and `status` on finished jobs; `up` and `stop`; and
`serve unsloth/Qwen3-0.6B-GGUF --quant Q4_K_M` on a `t4-small`, ready one
minute after scheduling at about 220 tokens/s. A job's `command` replaces the
image's entrypoint, `llama-server -hf` downloads inside the image, and the
exposed endpoint answers 401 without a token. The whole test cost under
$0.05.

On 2026-09-28, with the user's key registered: `ssh` through the Jobs SSH
gateway and a `tunnel` to a web server on the job both work. The gateway keeps
answering keep-alives after a job is cancelled, so `tunnel` also checks every
30 seconds that the instance still runs and closes itself (26 seconds after
`stop` in the live test). Automatic billing chose `hifinab`, the only account
that can pay.

### Phasing

1. **Colab** through its CLI. The team already has Pro+ units and the proven
   colab-runner flow; Colab suits instances more than runs.
2. **Hugging Face** through its API: runs and instances. It bills per minute
   and has SSH and HTTPS ports.
3. **RunPod** through its API: runs, then instances. It has a broad GPU range
   and needs the `hi` watchdog, which Lambda and Vast reuse later.
4. **Modal** through its Go SDK. It is on the roadmap, but implementation
   waits until the SDK leaves beta and its package path settles.
5. **Lambda, Vast.ai, and SkyPilot** on demand **(draft)**. SkyPilot overlaps
   with the drivers above, so it is only worth adding for clouds `hi` does not
   cover.

## Commands

```text
hi compute                              Guided menu in a terminal, else help
hi compute providers                    Configured and authenticated providers
hi compute hardware [--on <provider>]   Hardware flavors and prices
hi compute up [options]                 Start an instance
hi compute run [options] <script.py> [-- args...]
hi compute run [options] <image> -- <command> [args...]
                                        Run to completion, streaming logs
hi compute ls                           Runs and instances, with limits
hi compute status <name>                State, hardware, and limits
hi compute ssh <name> [-- <command>]    Open a shell or run one command
hi compute tunnel <name> <port>[:<local>]
                                        Forward a remote port to localhost
hi compute logs <name> [--follow]       Setup and server logs on the instance
hi compute serve <recipe | owner/repo-GGUF --quant Q> [options]
                                        Serve a model and tunnel its API here
hi compute stop <name> | --all          Stop runs or instances
hi compute wait <name>...               Wait until runs finish (API providers)
hi compute proxy <name>                 The provider's SSH transport, for ProxyCommand
```

Draft commands:

```text
hi compute <file.yaml>                  Start what a compute file describes  (draft)
hi compute templates                    List ready-made compute templates    (draft)
hi compute new <template> [<file>]      Write a template to edit             (draft)
hi compute push <name> <path> [<dest>]  Copy files to the instance           (draft)
hi compute pull <name> <path> [<dest>]  Copy files back                      (draft)
```

Until the full TUI is approved, bare `hi compute` in a terminal opens a
numbered menu over the commands above that prints the equivalent command for
every action. Without a terminal it prints help.

Options for `run` and `up`:

```text
--on <provider>      colab, hf, runpod; default: $HI_COMPUTE_PROVIDER, else the
                     one ready provider that offers --gpu, else the only ready one
--gpu <flavor>       Provider flavor, such as a10g-small or cpu-basic
--name <name>        Local handle; generated when omitted
--env KEY=VALUE      Plain environment variable; repeatable
--secret KEY         Secret read from the local environment; repeatable
--max <duration>     Hard lifetime limit, such as 30m or 4h; always enforced
--detach             run only: return after submission instead of streaming logs
--yes                Skip the cost confirmation
--dry-run            Print the resolved setup and provider requests; start nothing
--high-mem           Colab only: request a high-RAM machine
--image <image>      up only: container image where supported             (draft)
--idle <duration>    up only: stop after this long without traffic        (draft)
--set <key>=<value>  Override a compute-file field                        (draft)
```

For `run`, a `.py` argument runs as a uv script, so its inline dependencies
are installed remotely. Anything else is a container image, and the command
after `--` runs inside it. `run` streams logs unless `--detach` is given and
exits with the run's final status. `--max` is the run's timeout; without it
the provider's default applies and is shown.

Names are local handles mapped to provider IDs, such as `hf/68498e23…` or
`runpod/qwen`, so follow-up commands need no `--on`. `hi compute ls`
reconciles with the provider on every call; the provider is the source of
truth. It shows runs and instances together, with a kind column.

## Three ways in (draft)

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

`ssh`, `tunnel`, `logs`, and `serve` all run the system `ssh` with the
provider's transport. **(draft):** `hi` also writes a `Host hi-<name>` block
to a managed include file (`~/.ssh/config.d/hi`) whose `ProxyCommand` is
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

v0.6.0 builds recipes into `hi` and serves any other GGUF repository with
`hi compute serve owner/Model-GGUF --quant Q4_K_M --gpu L4`. The remote script
builds llama.cpp with CUDA and downloads the quant in parallel, then starts
`llama-server` on `127.0.0.1:8000`; `hi` tunnels it to local port 8080 unless
`--port` says otherwise. The YAML recipe format above and API keys are still
draft.

## Cost safety

- Every run and instance has a maximum lifetime: `--max` defaults to 4 hours
  for instances and 1 hour for runs. `--idle` (default 30 minutes) is draft.
  `--yes` and `--detach` never remove a limit.
- `run`, `up`, and `serve` show provider, flavor, price per hour, and limits,
  and ask for confirmation on paid hardware. Prices come from the provider's API where
  available and are shown in the provider's own unit.
- `hi compute ls` shows running time and estimated cost so far.
- Any `hi` command prints a one-line reminder while instances started through
  `hi` are still running. **(draft)**
- Where the provider has no native limit, `hi` installs a small watchdog on the
  instance that stops it through the provider API after the idle or maximum
  limit, so a closed laptop never leaves a GPU running. `hi compute ls` also
  stops instances whose limits have passed.
- Colab instances cannot stop themselves from inside, so `hi compute up`
  starts a detached local watcher (`hi compute __watch`) that checks the wall
  clock every 30 seconds, surviving closed terminals and suspend. It does not
  survive a reboot or a powered-off laptop; `hi compute ls` and Colab's own
  24-hour session limit are the backstops.
- `stop --all` lists what it will stop and asks for confirmation.

## Errors and output

Unknown providers, unknown hardware, and malformed `--env` values fail before
any provider request. A missing or rejected token names the `hi login` command
to run. Every provider call has its own time limit, and a failed poll is
retried rather than reported as a failed run.

## Configuration

Defaults live in `~/.config/hi/compute.toml`: default provider, GPU, idle and
maximum limits, and per-provider settings. Projects keep their setups as
compute files under `.hi/compute/`, which the TUI offers first when run inside
the project.

## Relationship to other commands

- **`hi model`:** shares recipes and health checks; `hi model` serves locally,
  `hi compute serve` remotely.
- **`hi net`:** supplies the optional mesh connectivity.
- **`hi login`:** supplies provider logins; its token rule needs the change
  described under Credentials.

## Open questions

1. Whether the Hugging Face SSH gateway works on every flavor and forwards
   arbitrary ports; the docs show `ssh -L` but do not list restrictions.
2. How Colab drives setup inside a runtime that cannot run a custom image:
   build on first use and cache on Google Drive, or download prebuilt binaries.
3. Shared team defaults and budgets: per-user config only, or a checked-in
   team config with spend limits.
4. Whether the watchdog should be opt-out, given it runs code on the instance
   with a provider API key that can stop it.
5. TUI library: a Go framework such as Bubble Tea keeps `hi` a single static
   binary; confirm size and accessibility before choosing.
6. Whether compute files should allow several instances, such as a server plus
   a client, or stay one instance per file.

## Acceptance criteria

Runs (approved):

1. `hi compute run python:3.12 -- python -c "print(1)"` runs on Hugging Face
   and exits with the run's status.
2. `hi compute run train.py --gpu a10g-small` runs as a uv script on an A10G.
3. `--dry-run` prints the resolved request and the equivalent `hf jobs`
   command, and starts nothing.
4. Paid hardware requires confirmation unless `--yes` is given.
5. `ls`, `status`, `logs --follow`, `wait`, and `stop` accept the name
   returned by `run`.
6. Secret values and tokens never appear in process arguments, output, or
   files written by `hi`.
7. A missing or rejected token produces the `hi login` command and a non-zero
   exit.
8. Runs work on a workstation without Python or the `hf` CLI once a token is
   available.
9. An unknown hardware name fails before any provider request.
10. Adding a provider requires a new driver only, with no change to the
    command surface.

Instances:

11. `hi compute up --on hf --gpu a10g-small` starts an instance, prints its
    name, price, and limits, and `hi compute ssh <name>` opens a shell on it.
12. `ssh hi-<name>`, `rsync`, and VS Code Remote-SSH work for every provider
    that supports SSH, without provider-specific setup by the user.
13. `hi compute tunnel <name> 8000` makes the remote port reachable only on
    `127.0.0.1:8000`.
14. `hi compute serve qwen3.8-flash-next --on colab --gpu G4` reproduces the
    colab-runner result: a local OpenAI-compatible URL after a passing health
    check.
15. A dropped connection during setup does not stop setup, and rerunning
    `serve` resumes rather than restarts it.
16. Every instance stops by itself after its idle or maximum limit, including
    when the laptop is offline.
17. `ls` and `stop` agree with the provider's own listing; instances not
    started through `hi` are listed and marked, and have no limits.
18. **(draft)** The same setup started from the TUI, from flags, and from a compute file
    produces identical provider requests in `--dry-run`.
19. Without a terminal, missing settings fail with the list of required flags
    instead of prompting.

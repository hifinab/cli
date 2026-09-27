# `hi job` specification

Status: Approved

Dependencies: Hugging Face Jobs REST API; `hf` CLI for login only;
`hi login` delegation; `uv` for script jobs. Later providers use the driver
layer described in [hi_compute.md](../ideas/hi_compute.md).

## Goal

Run a container or Python script on rented remote compute from a workstation
with one command, then list, follow, wait for, and cancel it the same way on
every provider. `hi` calls the provider's documented API where one exists;
the provider keeps ownership of accounts, billing, scheduling, and job state.

## Commands

```text
hi job run [options] <image> -- <command> [args...]
hi job run [options] <script.py> [-- args...]
hi job ls [--all]
hi job status <job>
hi job logs <job> [--follow]
hi job wait <job>...
hi job cancel <job>
hi job hardware [--on <provider>]
hi job providers
```

Run options:

```text
--on <provider>     Provider to run on; default is hf
--gpu <flavor>      Hardware flavor, such as a10g-small or cpu-basic
--name <name>       Job name shown by the provider and hi job ls
--timeout <dur>     Maximum run time, such as 30m or 4h
--env KEY=VALUE     Plain environment variable; repeatable
--secret KEY        Secret read from the local environment; repeatable
--detach            Return after submission instead of streaming logs
--dry-run           Print the resolved job and API request without sending it
--yes               Skip the cost confirmation for paid hardware
```

A `.py` argument runs as a uv script, so its inline dependencies are
installed remotely. Anything else is treated as a container image and the
command after `--` runs inside it.

## Providers

`hi job providers` lists every known provider, whether it is configured,
and whether it is authenticated. Job identifiers are prefixed with the
provider, such as `hf/68498e23210b3a4f4e6e2a23`, so follow-up commands need no
`--on`.

The first provider is Hugging Face Jobs, called through its REST API at
`https://huggingface.co/api/jobs` (documented with an OpenAPI spec) rather
than by wrapping the `hf` CLI. The API returns typed job state, needs no
Python on the workstation, and avoids parsing CLI output that changes between
releases.

| `hi job`          | Hugging Face Jobs API                              |
|-------------------|----------------------------------------------------|
| `run <image>`     | `POST /api/jobs/{namespace}` with `dockerImage`    |
| `run <script.py>` | `POST /api/jobs/{namespace}` running `uv run`      |
| `ls`              | `GET /api/jobs/{namespace}`                        |
| `status`          | `GET /api/jobs/{namespace}/{id}`                   |
| `logs`            | `GET /api/jobs/{namespace}/{id}/logs` (SSE stream) |
| `wait`            | Poll `status` until a final stage                  |
| `cancel`          | `POST /api/jobs/{namespace}/{id}/cancel`           |
| `hardware`        | `GET /api/jobs/hardware`, including prices         |

The API documentation is thinner than the Python client's, so request and
response shapes are pinned in tests against the OpenAPI spec, with the
`huggingface_hub` source as the reference. Log streams send keep-alives and
may be empty early in a job's life; both are handled without error.
`--dry-run` also prints the equivalent `hf jobs` command for users who want to
run it by hand.

Later providers follow the order and API-first rule in
[hi_compute.md](../ideas/hi_compute.md): RunPod through its REST API, then
Modal through its official Go SDK. Each provider maps the same flags to its
own options and rejects flags it cannot honor rather than ignoring them.

## Installation and login

`hi install` installs the `hf` CLI. `hi login hf` delegates to
`hf auth login` and reports `hf auth whoami`.

For API calls, `hi` reads the token the same way `huggingface_hub` does:
`HF_TOKEN`, then the file named by `HF_TOKEN_PATH`, then `$HF_HOME/token`
(default `~/.cache/huggingface/token`). The token is held in memory for the
request only; `hi` never copies, prints, or stores it.

`--secret KEY` reads `KEY` from the local environment and sends it in the
request's encrypted `secrets` field, which the provider encrypts server side.
The value never appears in `hi` arguments, output, or files.

## Cost safety

Before starting a job on paid hardware, `hi job run` shows the provider,
flavor, hourly price when the provider reports one, and the timeout, then asks
for confirmation. `--yes` and `--detach` do not remove the timeout. When no
`--timeout` is given, the provider's default applies and is shown.

## Errors and output

`hi job run` streams the job's logs unless `--detach` is given and exits with
the job's final status. A missing or rejected token names the `hi login`
command to run. Unknown
providers, flavors the provider rejects, and malformed `--env` values fail
before anything is submitted.

## Acceptance criteria

1. `hi job run python:3.12 -- python -c "print(1)"` runs on Hugging Face and
   exits with the job's status.
2. `hi job run train.py --gpu a10g-small` runs as a uv script on an A10G.
3. `--dry-run` prints the resolved request and equivalent `hf jobs` command
   and submits nothing.
4. Paid hardware requires confirmation unless `--yes` is given.
5. `ls`, `status`, `logs --follow`, `wait`, and `cancel` accept the prefixed
   job identifier returned by `run`.
6. Secret values never appear in process arguments, output, or files written
   by `hi`.
7. A missing or rejected token produces the `hi login hf` command and a
   non-zero exit.
8. Adding the RunPod or Modal provider requires a new driver only, with no
   change to the `hi job` command surface.
9. Jobs work on a workstation without Python or the `hf` CLI once a token is
   available.

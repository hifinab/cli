# `hi job` specification

Status: Approved

Dependencies: Hugging Face `hf` CLI with Jobs support; `hi login` delegation;
`uv` for script jobs. Later providers require their own native CLI.

## Goal

Run a container or Python script on rented remote compute from a workstation
with one command, then list, follow, wait for, and cancel it the same way on
every provider. `hi` chooses and composes the provider's native command; the
provider CLI keeps ownership of authentication, billing, scheduling, and
job state.

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
--dry-run           Print the native command without running it
--yes               Skip the cost confirmation for paid hardware
```

A `.py` argument runs as a uv script, so its inline dependencies are
installed remotely. Anything else is treated as a container image and the
command after `--` runs inside it.

## Providers

`hi job providers` lists every known provider, whether its CLI is installed,
and whether it is authenticated. Job identifiers are prefixed with the
provider, such as `hf/68498e23210b3a4f4e6e2a23`, so follow-up commands need no
`--on`.

The first provider is Hugging Face Jobs:

| `hi job`          | Native command                    |
|-------------------|-----------------------------------|
| `run <image>`     | `hf jobs run`                     |
| `run <script.py>` | `hf jobs uv run`                  |
| `ls`              | `hf jobs ps`                      |
| `status`          | `hf jobs inspect`                 |
| `logs`            | `hf jobs logs`                    |
| `wait`            | `hf jobs wait`                    |
| `cancel`          | `hf jobs cancel`                  |
| `hardware`        | `hf jobs hardware`                |

A later provider adds SkyPilot, which reaches RunPod, Lambda, AWS, GCP,
Azure, and Kubernetes through `sky jobs launch`, `sky jobs queue`,
`sky jobs logs`, and `sky jobs cancel`. Each provider maps the same flags to
its own options and rejects flags it cannot honor rather than ignoring them.

## Installation and login

`hi install` installs the `hf` CLI for all users. `hi login hf` delegates to
`hf auth login` and reports `hf auth whoami`, following the `hi login` rules:
`hi` never reads, stores, or prints tokens.

`--secret KEY` passes the name to the provider's encrypted secret mechanism,
such as `hf jobs run --secrets KEY`. The value is read by the provider CLI from
the environment and never appears in `hi` arguments, output, or files.

## Cost safety

Before starting a job on paid hardware, `hi job run` shows the provider,
flavor, hourly price when the provider reports one, and the timeout, then asks
for confirmation. `--yes` and `--detach` do not remove the timeout. When no
`--timeout` is given, the provider's default applies and is shown.

## Errors and output

Streaming output, exit codes, and interactive prompts come from the native
CLI. A missing CLI names the `hi install` or install command that provides it.
An unauthenticated provider names the `hi login` command to run. Unknown
providers, flavors the provider rejects, and malformed `--env` values fail
before anything is submitted.

## Acceptance criteria

1. `hi job run python:3.12 -- python -c "print(1)"` runs on Hugging Face and
   exits with the job's status.
2. `hi job run train.py --gpu a10g-small` runs as a uv script on an A10G.
3. `--dry-run` prints the exact native command and submits nothing.
4. Paid hardware requires confirmation unless `--yes` is given.
5. `ls`, `status`, `logs --follow`, `wait`, and `cancel` accept the prefixed
   job identifier returned by `run`.
6. Secret values never appear in process arguments, output, or files written
   by `hi`.
7. A missing or unauthenticated `hf` CLI produces the install or login command
   and a non-zero exit.
8. Adding the SkyPilot provider requires no change to the `hi job` command
   surface.

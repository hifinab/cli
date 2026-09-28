---
title: How hi compute works
description: The ideas behind hi compute - providers, runs and instances, hardware names, names, and the limits that keep costs in check.
---

`hi compute` rents a machine from a provider, lets you use it as if it were
local, and gives it back. It hides each provider's own tool behind one set of
commands, but it does not pretend they are the same: hardware names and
prices stay the provider's own.

## Runs and instances

`hi compute` handles two kinds of work with the same commands:

| Kind         | Started with       | Ends when                                 | Good for                                   |
|--------------|--------------------|-------------------------------------------|--------------------------------------------|
| **Run**      | `hi compute run`   | Its script or command finishes            | Training, evaluation, batch jobs, experiments |
| **Instance** | `hi compute up`    | You stop it, or it reaches `--max`        | Shells, notebooks, web apps, model servers |

`hi compute serve` is an instance that runs a model server for you. Runs and
instances share `ls`, `status`, `logs`, `stop`, and the same limits.

## Providers

| Provider                                        | How hi talks to it           | Strengths                                                     |
|-------------------------------------------------|------------------------------|---------------------------------------------------------------|
| [Google Colab](/guide/compute/colab/)           | The Colab CLI                | Prepaid compute units on Pro+, 96 GB G4 GPUs, 176 GB RAM      |
| [Hugging Face Jobs](/guide/compute/hugging-face/) | The Jobs REST API          | Pay per minute, any container image, detached runs, secrets, HTTPS endpoints |

RunPod is next on the [roadmap](https://github.com/hifinab/cli/blob/main/docs/roadmap.md).

```sh
hi compute providers    # which providers are signed in
hi compute hardware     # every provider's hardware, memory, and price
```

## Which provider is used

`hi` picks the provider in this order:

1. `--on colab` or `--on hf` on the command.
2. The `HI_COMPUTE_PROVIDER` environment variable.
3. The hardware name: `--gpu G4` only exists on Colab, `--gpu a10g-small`
   only on Hugging Face.
4. The only provider you are signed in to.

If none of these decides, `hi` asks you to add `--on`.

## Hardware names

Use the names from `hi compute hardware`. `hi` checks the name before calling
the provider, because an unknown name can otherwise fall back to other
hardware: the Colab CLI once turned an unknown GPU into an A100.

| Memory you need | Colab   | Hugging Face                   |
|-----------------|---------|--------------------------------|
| 16 GB           | `T4`    | `t4-small`, `t4-medium`        |
| 24 GB           | `L4`    | `a10g-small`, `l4x1`           |
| 40–80 GB        | `A100`, `H100` | `l40sx1` (48 GB), `a100-large` (80 GB) |
| 96 GB           | `G4`    | `rtx-pro-6000`                 |
| More            |         | `h200` (141 GB) and multi-GPU flavors |

## Names

Every run and instance has a name. Choose one with `--name`, or `hi` makes one
from the hardware, such as `t4-3fa1`. Names use lowercase letters, digits, and
hyphens. Refer to an instance by its name or by `provider/name`, and to a
Hugging Face job also by its ID:

```sh
hi compute ssh box
hi compute ssh colab/box
hi compute logs hf/6ab97cba6b030d633f69a5ea
```

## Limits and confirmation

Every run and instance has a maximum lifetime, `--max`, in hours (`2`) or with
a unit (`30m`, `2d`). Instances default to 4 hours and runs to 1 hour; Colab
allows at most 24 hours. `--max none` means no limit, after a warning you must
confirm. Paid hardware asks
you to confirm before it starts. [Costs, limits, and billing](/guide/compute/billing/)
explains how each provider enforces the limit.

Add `--dry-run` to any `up`, `run`, or `serve` to see the exact provider
command or API request without starting anything.

## The guided menu

Run `hi compute` with no arguments in a terminal for a guided menu:

- A **Running** panel at the top shows your machines and how long each has
  left; `no time limit` is highlighted.
- Choose with the arrow keys and Enter. In long lists such as Hugging Face's
  hardware, press `/` and type to filter, for example `/a10`.
- Text answers are checked as you type them, so a lifetime like `soon` is
  flagged before anything happens.
- Each answer stays on screen as a `✔` line, so you can see the choices you
  made.
- Before anything costs money, a summary card shows the provider, hardware,
  price, who pays, the time limit, and the equivalent command. The default
  answer is **No**. Machines without a time limit get an amber warning.
- Esc goes back a step; Ctrl+C leaves the menu.

Every action prints the command it runs, so you learn the flags as you go.
When the input is not a terminal, the menu falls back to numbered questions.

## Commands at a glance

| Command                                   | Does                                           |
|-------------------------------------------|------------------------------------------------|
| `hi compute up`                           | Start an instance                              |
| `hi compute run <script.py>`              | Run a script to completion, streaming its logs |
| `hi compute run <image> -- <command>`     | Run a container command (Hugging Face)         |
| `hi compute serve <model>`                | Serve a model with an OpenAI-compatible API    |
| `hi compute ssh <name>`                   | Open a shell or run one command                |
| `hi compute tunnel <name> <port>`         | Forward a port to localhost                    |
| `hi compute ls`, `status <name>`          | What is running, and details                   |
| `hi compute logs <name>`, `wait <name>`   | Output, and waiting for runs to finish         |
| `hi compute stop <name>`                  | Stop and release                               |
| `hi compute billing`                      | Who pays, and the Colab balance                |

The [command reference](/guide/reference/commands/) lists every option.

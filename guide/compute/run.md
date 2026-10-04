---
title: Run a job to completion
description: Run a Python script or a container command on rented hardware, stream its output, and get its exit code back - with dependencies, arguments, environment, secrets, and detached runs.
---

`hi compute run` starts a fresh machine, runs one script or command, streams
its output, and releases the machine when it ends. `hi` exits with the job's
own exit code, so it works in scripts and CI:

```sh
hi compute run --gpu a10g-small --max 1h train.py -- --epochs 3 && echo trained
```

## Python scripts

A `.py` file runs with [uv](https://docs.astral.sh/uv/), so dependencies
declared inline in the script ([PEP 723](https://peps.python.org/pep-0723/))
are installed on the machine. No requirements file or image is needed:

```python
# /// script
# requires-python = ">=3.10"
# dependencies = ["torch", "rich"]
# ///
import sys
import torch
from rich import print

print(f"args={sys.argv[1:]} cuda={torch.cuda.is_available()}")
```

```sh
hi compute run --gpu a10g-small --max 30m check.py -- --verbose
```

Everything after `--` is passed to the script as its arguments.

| Provider     | How the script gets there                                   | Limits              |
|--------------|-------------------------------------------------------------|---------------------|
| Hugging Face | Sent inside the job request; runs in `ghcr.io/astral-sh/uv:python3.12-bookworm` | 96 KB per script |
| Colab        | `colab run`, on a fresh Colab runtime                       | Python scripts only |

Scripts larger than 96 KB, or code in several files, belong in a container
image or a Git repository the script clones.

## Container images (Hugging Face)

On Hugging Face you can also run any public image. Put the command after `--`:

```sh
hi compute run --on hf --max 10m python:3.12 -- python -c 'print("hello")'
hi compute run --gpu l40sx1 --max 2h pytorch/pytorch:2.6.0-cuda12.4-cudnn9-devel -- python -m my.module
```

## Environment and secrets

```sh
hi compute run --gpu a10g-small \
  --env EPOCHS=10 --env RUN_NAME=baseline \
  --secret WANDB_API_KEY --secret HF_TOKEN \
  train.py
```

- `--env KEY=VALUE` sets a plain environment variable. Values appear in the
  job's settings, so never use it for credentials.
- `--secret KEY` sends the value of `$KEY` from your shell as an encrypted
  Hugging Face job secret. It must be set in your shell first, and its value
  never appears in `hi` output or `--dry-run`.

> Colab has no secret store, so `hi` refuses `--secret` on Colab rather than
> putting the value on a command line.
{: .warning}

## The team's data

With a hi server that serves the team's Hugging Face data
([`hi data`](/guide/data/)), `--data` downloads it on the instance before
the script starts:

```sh
hi compute run --gpu a10g-small --data hifinab/bars-1d train.py
hi compute run --gpu a10g-small --data hifinab/fdb/runs/2026-09 --data hifinab/ranker train.py
```

Each repository lands in `data/<name>` next to where the script runs, as
with `hi data get`. A folder or pattern after the name (`/runs/2026-09`,
`/*.parquet`) downloads only those files.

The instance can't reach the hi server, so hi asks it for a signed
download link per file when the run starts, and sends the links with the
script; the instance never holds a token. The links last about an hour, so
the job must start within that time. Script runs only.

On Hugging Face Jobs, everything travels in one environment variable of
at most 120 KB: enough for a few hundred files. Small files that a
repository keeps in git rather than Hugging Face's file storage, such as
an older model's `tokenizer.json`, have no link and travel inside the job,
and `hi` says when they make it too large. Leave them out with a pattern,
or run on Colab, which has no such limit.

## Long runs: detach, follow, and wait (Hugging Face)

Start a run and get your terminal back with `--detach`:

```sh
hi compute run --gpu a10g-small --max 6h --detach --name finetune train.py
```

```text
Started job 6ab97cba…: https://huggingface.co/jobs/hifinab/6ab97cba…
Follow it with: hi compute logs finetune --follow
Wait for it:    hi compute wait finetune
```

Then, from anywhere:

```sh
hi compute ls                        # it is listed while it runs
hi compute logs finetune --follow    # stream output until it ends
hi compute logs finetune -n 100      # the last 100 lines, even after it ended
hi compute wait finetune             # block until it ends; exits with its code
hi compute status finetune           # state, hardware, when it started and stops
hi compute stop finetune             # cancel it
```

Colab runs cannot detach. For long work on Colab, start an
[instance](/guide/compute/instances/) and run your script inside it.

## Exit codes

| Result                             | `hi` exits with                  |
|------------------------------------|----------------------------------|
| The job finished successfully      | `0`                              |
| The job failed                     | The job's own exit code, when the provider reports it, else `1` |
| The job was stopped                | `130`                            |

## Results and files

A run's machine is gone when it ends, and so is everything written to its
disk. Save results somewhere that outlives it: print them, push them to the
Hugging Face Hub from the script (pass `--secret HF_TOKEN`), or upload them to
your own storage. [Train with uv scripts](/guide/examples/train/) shows
uploading a model to the Hub.

## Options

| Option              | Meaning                                                    |
|---------------------|------------------------------------------------------------|
| `--gpu <hardware>`  | Hardware from `hi compute hardware`; default is the cheapest CPU |
| `--max <duration>`  | Stop after this long: hours (`2`), `30m`, `2d`, or `none`; default `1h` |
| `--name <name>`     | Name for `logs`, `wait`, and `stop`                         |
| `--env KEY=VALUE`   | Environment variable; repeatable                            |
| `--data <org>/<name>[/<pattern>]` | The team's data into `data/<name>` first; repeatable |
| `--secret KEY`      | Encrypted secret from your shell (Hugging Face); repeatable |
| `--detach`          | Return after starting (Hugging Face)                        |
| `--namespace <ns>`  | Bill this Hugging Face account for this run                  |
| `--high-mem`        | High-RAM machine (Colab)                                    |
| `--on <provider>`   | `colab` or `hf`                                             |
| `--yes`             | Skip the cost confirmation                                  |
| `--dry-run`         | Show the request without starting anything                  |

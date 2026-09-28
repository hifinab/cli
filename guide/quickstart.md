---
title: Your first remote GPU
description: Five minutes from a fresh install to a shell on a rented GPU, and back again without leaving anything running.
---

This walk-through rents a small GPU, runs a command on it, and gives it back.
It assumes you have [installed hi](/guide/install/) and signed in to at least
one provider.

## 1. Check your providers

```sh
hi compute providers
```

```text
colab    ready
hf       ready
```

A provider that is not ready says what to do next, such as
`sign in with hi login hf`.

## 2. Pick hardware

```sh
hi compute hardware
```

```text
PROVIDER  HARDWARE    KIND  MEMORY      RATE
colab     cpu         CPU   12 GB RAM   ~0.08 units/h
colab     T4          GPU   15 GB VRAM  ~1.07 units/h
...
hf        t4-small    GPU   16 GB VRAM  $0.40/h
hf        a10g-small  GPU   24 GB VRAM  $1.00/h
...
```

Hardware names are the provider's own. A T4 is `T4` on Colab and `t4-small`
on Hugging Face. The name you choose also picks the provider.

## 3. Preview, then start

`--dry-run` shows exactly what would happen and starts nothing:

```sh
hi compute up --gpu T4 --name first --max 30m --dry-run
```

```text
Start colab/first on T4 (~1.07 units/h), stopping after 30m at 14:05.
Would run: colab new -s first --gpu T4
```

When it looks right, run it without `--dry-run`. `hi` asks you to confirm
paid hardware:

```sh
hi compute up --gpu T4 --name first --max 30m
```

```text
Start colab/first on T4 (~1.07 units/h), stopping after 30m at 14:05.
Start it? [y/N] y
...
first is up. Next:
  hi compute ssh first
  hi compute tunnel first 8000
  hi compute stop first
```

On Hugging Face, the same step is `--gpu t4-small`, and the start line also
names who pays, as in `($0.40/h, billed to hifinab)`.

## 4. Use it

```sh
hi compute ssh first -- nvidia-smi     # run one command
hi compute ssh first                   # or open a shell; exit when done
```

> On Hugging Face, shells need your SSH key registered on the Hub first. See
> [Hugging Face Jobs](/guide/compute/hugging-face/#add-your-ssh-key-to-the-hub).
{: .warning}

## 5. Give it back

```sh
hi compute stop first
hi compute ls
```

```text
Stopped first.
No instances are running.
```

Even if you forget, the machine stops itself at `--max` (30 minutes here).

## Next steps

- Run a training script instead of a shell: [Run a job to completion](/guide/compute/run/)
- Reach a web app or notebook on the machine: [Interactive machines](/guide/compute/instances/)
- Try a large language model: [Serve a model](/guide/compute/serve/)
- Prefer menus? Run `hi compute` with no arguments for a guided menu that
  prints the equivalent command at every step.

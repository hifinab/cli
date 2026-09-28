---
title: Hugging Face Jobs
description: Set up Hugging Face Jobs for hi compute - sign-in, credits and who pays, and registering your SSH key on the Hub so shells and tunnels work.
---

`hi` uses the [Hugging Face Jobs](https://huggingface.co/docs/huggingface_hub/guides/jobs)
REST API directly. Jobs bill US dollars per minute, run any container image,
and can expose HTTPS endpoints.

## Set it up

### 1. Sign in

```sh
hi login hf
```

This runs `hf auth login` from the `hf` CLI, which `hi install` installs, and
then shows who you are. `hi` reads the resulting token the same way the `hf`
CLI does: `$HF_TOKEN`, then `$HF_TOKEN_PATH`, then `~/.cache/huggingface/token`.
It never prints or stores it.

If you only installed `hi` itself, install the `hf` CLI first:

```sh
curl -LsSf https://hf.co/cli/install.sh | bash
```

### 2. Make sure someone can pay

Jobs need pre-paid credits on the account that pays: your own account or an
organization you belong to. `hi` bills your own account if it can pay,
otherwise your only organization that can:

```sh
hi compute billing
```

```text
Hugging Face:
ACCOUNT    TYPE  CAN PAY  PLAN  BILLED
quantbert  user  no
hifinab    org   yes      team  <- hi bills this
```

If no account can pay, add credits at
[huggingface.co/settings/billing](https://huggingface.co/settings/billing).
[Costs, limits, and billing](/guide/compute/billing/#who-pays-on-hugging-face)
explains how to choose a different account.

### 3. Add your SSH key to the Hub
{: #add-your-ssh-key-to-the-hub}

`hi compute ssh` and `tunnel` connect through the Jobs SSH gateway, which only
accepts keys registered on your Hugging Face account. Runs, `logs`, and
`serve` use the API and work without this.

1. Print your public key, creating one first if needed:
   ```sh
   ls ~/.ssh/id_ed25519.pub || ssh-keygen -t ed25519
   cat ~/.ssh/id_ed25519.pub
   ```
2. Open [huggingface.co/settings/keys](https://huggingface.co/settings/keys)
   and choose **Add SSH Key**.
3. Paste the whole line, which starts with `ssh-ed25519`, give it a name such
   as your laptop's, and save.
4. Check it with a cheap machine:
   ```sh
   hi compute up --on hf --name keytest --max 10m
   hi compute ssh keytest -- echo it works
   hi compute stop keytest
   ```

Without a registered key, `ssh` fails with `Permission denied (publickey)`
and `hi` points you back here.

## Hardware

`hi compute hardware --on hf` lists every flavor with its current price. Some
common ones:

| Flavor          | GPU                  | Memory      | Price     |
|-----------------|----------------------|-------------|-----------|
| `cpu-basic`     | none                 | 16 GB RAM   | $0.01/h   |
| `cpu-upgrade`   | none                 | 32 GB RAM   | $0.03/h   |
| `t4-small`      | NVIDIA T4            | 16 GB VRAM  | $0.40/h   |
| `l4x1`          | NVIDIA L4            | 24 GB VRAM  | $0.80/h   |
| `a10g-small`    | NVIDIA A10G          | 24 GB VRAM  | $1.00/h   |
| `l40sx1`        | NVIDIA L40S          | 48 GB VRAM  | $1.80/h   |
| `a100-large`    | NVIDIA A100          | 80 GB VRAM  | $2.50/h   |
| `rtx-pro-6000`  | NVIDIA RTX PRO 6000  | 96 GB VRAM  | $2.75/h   |
| `h200`          | NVIDIA H200          | 141 GB VRAM | $5.00/h   |

Multi-GPU flavors such as `a100x4` and `h200x8` are listed too.

## What runs where

| Command                     | Image                                              |
|-----------------------------|----------------------------------------------------|
| `run script.py`             | `ghcr.io/astral-sh/uv:python3.12-bookworm`, with the script sent inside the request |
| `run <image> -- <command>`  | The image you name                                  |
| `up`                        | `python:3.12` on CPU, `pytorch/pytorch:2.6.0-cuda12.4-cudnn9-devel` on GPU, or `--image` |
| `serve`                     | `ghcr.io/ggml-org/llama.cpp:server-cuda`            |

Images must be public on Docker Hub or another registry the job can pull
from.

## Features only Hugging Face has

- **Detached runs:** `run --detach`, then `logs --follow` and `wait`. See
  [Run a job to completion](/guide/compute/run/#long-runs-detach-follow-and-wait-hugging-face).
- **Secrets:** `--secret NAME` sends a value from your shell encrypted.
- **HTTPS endpoints:** `serve` returns a URL such as
  `https://<job>--8000.hf.jobs/v1` that works from anywhere with a Hugging Face
  token and answers `401` without one.
- **Limits enforced by Hugging Face:** `--max` becomes the job's timeout, so a
  job stops even when your laptop is off.

## Troubleshooting

| Message                                            | Fix                                                        |
|----------------------------------------------------|------------------------------------------------------------|
| `hf not signed in` or `Hugging Face rejected the token` | `hi login hf`                                          |
| `Hugging Face Jobs need pre-paid credits` (HTTP 402) | Add credits, or bill an account that can pay: `hi compute billing` |
| `Permission denied (publickey)`                    | [Add your SSH key to the Hub](#add-your-ssh-key-to-the-hub) |
| `secret NAME is not set in this shell`             | `export NAME=…` before using `--secret NAME`                |
| `is larger than 96 KB`                             | Put the code in an image or a repository the script clones  |
| `Hugging Face refused: …`                          | You lack permission on that account; check `--namespace`    |

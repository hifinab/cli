---
title: Google Colab
description: Set up Colab for hi compute - the Colab CLI, sign-in, plan requirements, SSH keys - and the Colab-specific rules worth knowing.
---

`hi` drives Colab through Google's [Colab CLI](https://github.com/googlecolab/google-colab-cli),
because Colab has no public API. You pay with the compute units of your Colab
plan.

## Set it up

### 1. Install the Colab CLI

`hi install` installs it. On a machine where you only installed `hi`:

```sh
uv tool install google-colab-cli
```

### 2. Sign in

```sh
hi login colab
```

This opens a browser for Google sign-in and asks you to paste a code back,
then shows your balance. Sign in with the Google account that has the Colab
plan. The token is kept by the Colab CLI in `~/.config/colab-cli/`.

### 3. Check your plan

- SSH, and so `hi compute ssh`, `tunnel`, and `serve`, needs **Colab Pro or
  Pro+**. The free tier forbids SSH.
- Which GPUs you can get depends on your plan and on availability. An `H100`
  is often unavailable.

### 4. Make sure you have an SSH key

`hi compute ssh` and `tunnel` use `~/.ssh/id_ed25519` or `~/.ssh/id_ecdsa`.
Colab accepts the key automatically; there is nothing to register.

```sh
ls ~/.ssh/id_ed25519 || ssh-keygen -t ed25519
```

### 5. Check

```sh
hi compute providers
hi compute billing
```

```text
colab    ready

Colab balance: 1795.89 compute units, currently using 0.00 units/h
```

## Hardware

| Name   | GPU                            | Memory      | Rate (measured)  |
|--------|--------------------------------|-------------|------------------|
| `cpu`  | none                           | 12 GB RAM   | ~0.08 units/h    |
| `T4`   | NVIDIA T4                      | 15 GB VRAM  | ~1.07 units/h    |
| `L4`   | NVIDIA L4                      | 24 GB VRAM  | unmeasured       |
| `G4`   | NVIDIA RTX PRO 6000 Blackwell  | 96 GB VRAM, 176 GB RAM, 48 CPUs | ~8.9 units/h |
| `A100` | NVIDIA A100                    | 40 GB VRAM  | unmeasured       |
| `H100` | NVIDIA H100                    | 80 GB VRAM  | unmeasured       |
| `v5e1`, `v6e1` | Google TPU (1 chip)    | 16 / 32 GB  | unmeasured       |

`--high-mem` requests a high-RAM shape for `cpu`, `T4`, and `A100`.

## Rules worth knowing

- **24 hours at most.** Colab ends sessions after 24 hours; `--max` cannot be
  longer.
- **Port 8080 is Colab's.** Colab's own proxy uses it on every machine, so
  serve on another port, such as 8000, and tunnel that.
- **No public web services.** Colab's terms forbid them, so `hi` only ever
  forwards ports to `127.0.0.1` on your laptop.
- **The disk is slow for random reads.** It is network storage. Keep
  lookup-heavy data in RAM; a `G4` has 176 GB of it.
- **No custom images.** A Colab machine is a notebook runtime with CUDA 12.8
  and Python preinstalled. `serve` builds llama.cpp on it the first time,
  which takes about two minutes on a `G4`.

## How limits work on Colab

A Colab runtime cannot stop itself, so `hi compute up` starts a small
background process on your laptop (`hi compute __watch`) that stops the
machine at `--max`. It checks the wall clock every 30 seconds, so it also
works after your laptop wakes from sleep. After a reboot it is gone, which is
why every `hi compute ls` also stops machines past their limit.

## Runs on Colab

`hi compute run` on Colab uses `colab run`: a fresh runtime runs one Python
script and is released. Runs cannot detach, and there is no secret store, so
`--detach` and `--secret` are refused. For long work, start an instance and run
the script inside it over `hi compute ssh`.

## Troubleshooting

| Message                                         | Fix                                                        |
|-------------------------------------------------|------------------------------------------------------------|
| `colab not signed in`                           | `hi login colab`                                           |
| `the Colab CLI is not installed`                | `hi install`, or `uv tool install google-colab-cli`        |
| `no SSH key found`                              | `ssh-keygen -t ed25519`                                    |
| `asked for X but Colab started Y`               | The plan does not include X right now; stop it and choose other hardware |
| `port 8080 on colab is used by Colab's own proxy` | Serve and tunnel another port                            |
| `colab … did not answer within …`               | The Colab CLI hung; retry. `hi` limits every call so it never waits forever |

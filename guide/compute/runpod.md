---
title: RunPod
description: Set up RunPod for hi compute - an API key, your SSH key, and how hi keeps RunPod pods from running past their time limit.
---

`hi` uses RunPod's [REST API v2](https://docs.runpod.io/api-reference-v2)
directly to start pods on its secure cloud, with SSH access and prices from
RunPod's catalog.

> RunPod's secure cloud often has no free units of its cheapest GPUs. The
> `NOW` column shows what is free; if a start fails, `hi` suggests free GPUs
> with at least as much memory.
{: .tip}

## Set it up

### 1. Create an API key

1. Open [console.runpod.io/user/credentials](https://console.runpod.io/user/credentials)
   and choose **API Keys**.
2. Create a key with **read and write** access; a read-only key can list
   hardware but not start pods.
3. Make sure the account has a balance; pods stop when it reaches zero.

### 2. Give the key to hi

```sh
hi login runpod
```

Paste the key when asked; nothing is shown as you type. `hi` checks the key
with RunPod, then saves it in `~/.runpod/config.toml`, readable only by you.
That is where RunPod's own `runpodctl` keeps it too, so both tools share one
key and you do not need runpodctl installed.

The guided menu does the same: choose **RunPod** as the provider in
`hi compute`, and it asks for the key the first time.

In scripts, set the key in the environment instead; it takes precedence over
the saved one:

```sh
export RUNPOD_API_KEY=...
```

### 3. Your SSH key

`hi` passes your public key, `~/.ssh/id_ed25519.pub`, to each pod it starts,
so `hi compute ssh` and `tunnel` work without registering anything on RunPod.
Create a key with `ssh-keygen -t ed25519` if you have none.

### 4. Check

```sh
hi compute providers
hi compute hardware --on runpod
```

## Hardware

RunPod's GPU names are shortened: `NVIDIA GeForce RTX 4090` becomes
`rtx-4090`, `A100 PCIe` becomes `a100-pcie`. `hi compute hardware --on runpod`
lists CPU pods with 2 vCPUs (`cpu3c`, `cpu5g`, …), then every GPU in RunPod's
secure cloud with its price per hour. GPUs with free units come first, marked
`few free`; those with none are listed last as `none free`:

```text
PROVIDER  HARDWARE      KIND  MEMORY             RATE     NOW
runpod    cpu3c         CPU   2 vCPU, 4 GB RAM   $0.06/h
runpod    rtx-4000-ada  GPU   20 GB VRAM         $0.28/h  few free
runpod    a40           GPU   48 GB VRAM         $0.49/h  few free
...
runpod    rtx-a4000     GPU   16 GB VRAM         $0.25/h  none free
```

Availability changes by the minute, so treat it as a hint.

```sh
hi compute up --gpu rtx-4090 --name box --max 2
```

## What hi starts

| Setting   | Value                                                        |
|-----------|--------------------------------------------------------------|
| Cloud     | Secure cloud, on demand                                      |
| Image     | `runpod/pytorch:1.0.2-cu1281-torch280-ubuntu2404`, or `--image` |
| Disk      | 50 GB container disk (20 GB for CPU pods), no volume        |
| Ports     | `22/tcp` for SSH                                             |

`up` waits until the pod is running and reachable over SSH, which can take a
few minutes while RunPod pulls the image.

## Time limits

RunPod pods have no built-in time limit, so `hi` enforces `--max` twice:

- A small watchdog on the pod terminates it at the limit, using the pod's own
  RunPod key and the `runpodctl` that RunPod puts in every pod. This works even
  when your laptop is off.
- The same local watcher as for Colab stops it from your laptop, and every
  `hi compute ls` checks too.

`hi compute stop` **terminates** the pod rather than pausing it, because a
paused RunPod pod still bills for its disk.

## Not yet supported on RunPod

- `hi compute run`: RunPod has no run-to-completion jobs; start a pod and run
  your script over `hi compute ssh`.
- Model recipes: `hi compute serve` works with `--gpu` and `--quant`, building
  llama.cpp on the pod over SSH as on Colab, but has not been tested on RunPod.

## Troubleshooting

| Message                                   | What to do                                                    |
|-------------------------------------------|---------------------------------------------------------------|
| `runpod not signed in`                    | `hi login runpod`, or set `RUNPOD_API_KEY`                    |
| `RunPod rejected the API key`             | Create a new key; check it is not expired or deleted          |
| `RunPod refused … read-only`              | The key needs write access                                    |
| `RunPod needs more account balance`       | Add funds at [console.runpod.io/user/billing](https://console.runpod.io/user/billing) |
| `none of that hardware is free right now` | Choose another `--gpu`; availability changes often            |
| `the pod was not reachable after 15m`     | The image may lack SSH; stop the pod and try the default image |

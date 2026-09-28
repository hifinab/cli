---
title: RunPod
description: Set up RunPod for hi compute - an API key, your SSH key, and how hi keeps RunPod pods from running past their time limit.
---

`hi` uses RunPod's [REST API v2](https://docs.runpod.io/api-reference-v2)
directly to start pods on its secure cloud, with SSH access and prices from
RunPod's catalog.

> RunPod support is new. It is tested against a simulated RunPod API; the first
> runs against a real account may still need adjustments.
{: .note}

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

With [runpodctl](https://github.com/runpod/runpodctl) installed, this runs
`runpodctl doctor`, which asks for the key and saves it in
`~/.runpod/config.toml`. Otherwise it explains the alternative: put the key in
your shell profile.

```sh
export RUNPOD_API_KEY=...
```

`hi` reads `RUNPOD_API_KEY` first, then `apiKey` in `~/.runpod/config.toml`,
and never prints or stores the key itself.

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
lists every GPU available in RunPod's secure cloud with its current price per
hour, plus CPU pods with 2 vCPUs.

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
  RunPod key. This works even when your laptop is off.
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

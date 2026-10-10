---
title: Shadeform
description: Set up Shadeform for hi compute - one API key for on-demand GPUs from many clouds, each GPU type at its cheapest free offer, and machines that delete themselves at their time limit.
---

[Shadeform](https://www.shadeform.ai) rents virtual machines from many
clouds, such as Hyperstack, Massed Compute, Lambda, Scaleway, Paperspace,
and Vultr, through one API and one bill, at the clouds' own prices. `hi`
uses its REST API directly and rents on demand only: nothing it starts can
be taken back mid-run.

## Set it up

1. Create an API key at
   [platform.shadeform.ai/settings/api](https://platform.shadeform.ai/settings/api),
   and add a payment method under billing.
2. Save it for `hi`:

   ```sh
   hi login shadeform
   ```

   The key is checked with Shadeform and saved in
   `~/.config/hi/shadeform_key`, readable only by you. In scripts, set
   `SHADEFORM_API_KEY` instead.
3. Make sure you have an SSH key (`ssh-keygen -t ed25519`). The first start
   registers your public key with Shadeform; later starts reuse it.

## Choose hardware

```sh
hi compute hardware --on shadeform
```

```text
PROVIDER   HARDWARE  KIND  MEMORY       RATE     NOW
shadeform  a4000     GPU   16 GB VRAM   $0.15/h  hyperstack, NO, Oslo
shadeform  l40s      GPU   48 GB VRAM   $0.88/h  massedcompute, US, Des Moines, IA
shadeform  a100-80g  GPU   80 GB VRAM   $1.35/h  hyperstack, CA, Montreal
shadeform  h100      GPU   80 GB VRAM   $2.73/h  massedcompute, US, Des Moines, IA
shadeform  rtx4090   GPU   24 GB VRAM   $0.60/h  excesssupply, none free
```

Several clouds offer the same GPU at different prices. Each GPU type is
listed once, at its cheapest offer that is free right now, with the cloud
and region it would run in. Sold-out types come last.

To pick a cloud yourself, add it to the name:

```sh
hi compute up --on shadeform --gpu h100@lambdalabs --max 2h
```

## Start, use, and stop

```sh
hi compute up --on shadeform --gpu a4000 --name box --max 2h
hi compute ssh box
hi compute tunnel box 8000
hi compute stop box
```

A machine takes 3-8 minutes to boot. You log in as the `shadeform` user,
with `sudo`. Machines are Ubuntu with CUDA; `--image` does not apply, and
there are no run-to-completion jobs: use `up` and `ssh`.

`--max` is enforced by Shadeform itself, which deletes the machine at that
time, so it stops even if your laptop is off. `stop` deletes the machine,
and Shadeform stops billing as soon as it is deleting.

## Through a hi server

A [hi server](/guide/compute/managed/) can hold one Shadeform key for your
team:

```sh
hi server provider add shadeform
```

Connected devices then start Shadeform machines through the server, with
approval and policy like RunPod. If an approved offer sells out, the server
starts the cheapest free offer with at least as much GPU memory within the
approval's price bound, which can be the same GPU from another cloud.

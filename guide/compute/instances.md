---
title: Interactive machines
description: Start a machine that stays up, open shells on it, forward its ports to your laptop, connect VS Code, and stop it.
---

An instance is a machine that stays up until you stop it or it reaches its
maximum lifetime. Use one for shells, notebooks, debugging, and anything you
want to reach from your laptop.

## Start one

```sh
hi compute up --gpu T4 --name box --max 2h
```

| Option             | Meaning                                                       |
|--------------------|---------------------------------------------------------------|
| `--gpu <hardware>` | Hardware from `hi compute hardware`; default is the cheapest CPU |
| `--name <name>`    | Name to use in later commands; generated if omitted           |
| `--max <duration>` | Stop after this long: hours (`2`, `1.5`), `30m`, `2d`, or `none`; default `4h`, at most `24h` on Colab |
| `--image <image>`  | Container image (Hugging Face); default `python:3.12` on CPU and `pytorch/pytorch:2.6.0-cuda12.4-cudnn9-devel` on GPU |
| `--high-mem`       | High-RAM machine for `cpu`, `T4`, or `A100` (Colab Pro)        |
| `--namespace <ns>` | Bill this Hugging Face account                                |

## Open a shell

```sh
hi compute ssh box                       # interactive shell
hi compute ssh box -- nvidia-smi         # run one command and return
hi compute ssh box -- 'cd /tmp && ls'    # quote commands with shell syntax
```

Shells use your SSH key, `~/.ssh/id_ed25519` (or `id_ecdsa`). Create one with
`ssh-keygen -t ed25519` if you have none. On Hugging Face, the public key must
also be [registered on the Hub](/guide/compute/hugging-face/#add-your-ssh-key-to-the-hub).

## Forward a port

`tunnel` makes a port on the machine reachable on your laptop, on
`127.0.0.1` only:

```sh
hi compute tunnel box 8000          # the machine's 8000 on 127.0.0.1:8000
hi compute tunnel box 8888:18888    # the machine's 8888 on 127.0.0.1:18888
```

```text
Forwarding http://127.0.0.1:8000 to box port 8000. Press Ctrl+C to close.
```

- It runs in the foreground until you press Ctrl+C. In a terminal, `hi` then
  asks whether to stop the machine too.
- If the machine stops while the tunnel is open, the tunnel closes itself
  within about 30 seconds.
- If a local port is taken, the tunnel fails with `Address already in use`;
  pick another local port with `remote:local`.
- Colab uses port 8080 on its machines for itself, so `hi` refuses to tunnel
  it. Serve on another port, such as 8000.

[JupyterLab on a rented GPU](/guide/examples/jupyter/) is a complete example.

## Connect VS Code, rsync, or plain ssh

`hi compute proxy <name>` is the connection `hi compute ssh` uses. Put it in
`~/.ssh/config` and any SSH tool can reach a Colab machine by name, including
VS Code Remote-SSH, `scp`, and `rsync`:

```text
Host hi-box
  User root
  ProxyCommand hi compute proxy box
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
```

```sh
ssh hi-box
rsync -av ./data/ hi-box:/content/data/
```

`hi compute proxy` is for Colab. Hugging Face machines have a direct SSH
address, `<job-id>@ssh.hf.jobs`; use `hi compute ssh <name>`, which finds it
for you.

## See what is running

```sh
hi compute ls
```

```text
NAME  PROVIDER  HARDWARE    UP   STOPS IN
box   colab     T4          12m  1h48m
job   hf        a10g-small  3m   57m

Colab balance: 1795.89 compute units, currently using 1.07 units/h
```

`ls` also shows machines that `hi` did not start, marked `(not started by hi)`,
and stops any machine of yours past its limit. `hi compute status <name>`
shows one machine in detail.

## Stop

```sh
hi compute stop              # pick from what is running, then confirm
hi compute stop box
hi compute stop --all        # asks first; add --yes in scripts
```

Without a name, `stop` lists everything running with its hardware and time
left, plus an option to stop all of them, and asks before stopping. In
scripts, name the instance or use `--all`.

Stopping releases the machine and everything on its disk.

## Setup logs

Commands such as `serve` run setup on the machine and keep logs there.
`hi compute logs <name>` shows them, and `--follow` keeps printing new lines:

```sh
hi compute logs box --follow
```

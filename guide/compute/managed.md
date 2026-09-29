---
title: Managed compute for a team
description: Run hi server so employees, students, and their agents use GPUs through one set of provider keys, with every start approved and every machine stopped at its limit.
---

A team can run `hi server` on a small always-on machine inside its NetBird
network. The server holds the provider keys, makes every provider call itself,
and starts nothing until someone approves it. People join with `hi connect`
and then use the same `hi compute` commands as before.

- Nobody but the admins holds a provider key, so nobody can spend outside
  `hi`, and taking someone's access away is one command.
- Every paid start is approved by a person who is not the requester.
- The server stops every machine at its `--max`, even when the laptop that
  asked for it is off.

> Management is opt-in. If you never run `hi connect`, `hi` works exactly as
> before, with your own keys. Colab is never managed: it always runs under
> your own Google sign-in.
{: .note}

This release manages RunPod, and admins approve from the server box. Slack
approvals, budgets, and a live dashboard come in later releases.

## For users

### Join the server

Your admin tells you the server's name, such as `compute.internal`. On a
machine that is already in the NetBird network:

```sh
hi connect compute.internal
```

```text
Asked http://compute.internal:7373 to let laptop-alice join as alice (request e-3f09a1).
An admin approves it with: hi server approve e-3f09a1 --group <group>
Waiting for approval… (Ctrl-C stops waiting; the request stays open)
Connected to http://compute.internal:7373 as alice (students).
Managed by the server: runpod. Starting there needs an approval.
Colab and any other provider still use your own sign-in.
```

`hi connect` creates a key for this device in `~/.config/hi/device_key`,
readable only by you, and signs every request to the server with it. Your
user name defaults to your login name; choose another with `--user`.

### Start a machine

```sh
hi compute up --on runpod --gpu rtx-4090 --max 2h --reason "LoRA sweep for my thesis"
```

```text
Start runpod/rtx-4090-1c2d on rtx-4090 ($0.74/h), stopping after 2h at 16:05.
Start it? [y/N] y
Sent request r-9b41e0 to http://compute.internal:7373.
Waiting for approval… (Ctrl-C stops waiting; the request stays open)
Approved by bob; starting: Created pod 7xk2q9; waiting for it to start (this can take a few minutes)...
Approved by bob; starting: running
rtx-4090-1c2d is running.

rtx-4090-1c2d is up. Next:
  hi compute ssh rtx-4090-1c2d
  hi compute tunnel rtx-4090-1c2d 8000
  hi compute stop rtx-4090-1c2d
```

Two things differ from using your own key:

- **A reason and a time limit are required.** Approvers see the reason, and
  `--max none` is refused. Without `--reason`, `hi` asks for one.
- **You wait for a person.** If you stop waiting, the request stays open.
  Check it later with `hi compute requests`. A request with no decision
  expires after 30 minutes.

After that, `ssh`, `tunnel`, `logs`, `ls`, and `stop` work as usual. The
machine accepts the SSH key from your `~/.ssh`, and connections go straight
to it, not through the server.

### Stop it

```sh
hi compute stop rtx-4090-1c2d
```

Stopping never needs approval. You can stop your own machines only;
`hi compute stop --all` stops all of yours and nobody else's. If you forget,
the server stops the machine at its `--max`.

### Check your requests

```sh
hi compute requests                    # your recent requests
hi compute requests r-9b41e0           # one request
hi compute requests r-9b41e0 --wait    # wait for the decision and the start
```

| Exit status | Meaning                                  |
|-------------|------------------------------------------|
| 0           | Running, or already stopped              |
| 3           | Still waiting for approval               |
| 4           | Denied; the message has the reason       |
| 1           | Failed to start, or expired              |

### Agents

Coding agents use the same commands and the same approval. An agent working
on your laptop acts as you. Agents can't be prompted and have time limits on
their commands, so they use:

```sh
hi compute up --on runpod --gpu rtx-4090 --max 1h --reason "eval run" --yes --no-wait
hi compute requests r-9b41e0 --wait --timeout 10m
```

`--no-wait` returns at once with exit status 3 and the request ID. Nobody can
approve their own request, so an agent can't approve its own either. The
[agent skill](/guide/reference/skill/) teaches agents this flow.

### Leave the server

```sh
hi disconnect
```

This deletes the device key and the server address. `hi compute` then uses
your own keys again. Machines you started through the server keep its limits
until they stop.

## For admins

### Set up the server

On a small always-on Linux machine, enrolled in NetBird with `hi net`:

```sh
hi server init                    # listens on this machine's NetBird address, port 7373
hi server provider add runpod     # asks for the key without echoing it
hi server run
```

`hi server init` prints a systemd unit to run the server as a service. The
server keeps its state, audit log, and provider keys in
`~/.local/state/hi/server`, readable only by the user it runs as. Change that
with `--dir` or `HI_SERVER_DIR`.

It listens only on its NetBird address. Clients sign every request, so a
forged, altered, or replayed request is refused, even from inside the VPN.

### Approve and deny

Admin commands talk to the running server through a socket that only the
server's user can open, so they work on the server box:

```sh
hi server requests                            # what is waiting
hi server approve e-3f09a1 --group students   # a new device; new users need a group
hi server approve r-9b41e0                    # a compute request
hi server deny r-9b41e0 --reason "use a 4090, not an H100"
```

Decisions are recorded under your login name; change it with `--as`. Nobody
can decide their own request. To add your own device, pre-approve its key:
run `hi connect key` on the device, then:

```sh
hi server user add alice --group staff --key <key>
```

### Watch and stop

```sh
hi server ls                      # everything running, for every user
hi server stop rtx-4090-1c2d
hi server stop --user alice       # everything alice runs
hi server stop --all
hi server audit --since 24h       # requests, approvals, starts, and stops
```

Every 30 seconds the server checks each provider account. It stops machines
past their `--max`, and it records a machine it didn't start, such as one
created on the provider's website, as `unleased instance` in the audit log.

### Remove a user

```sh
hi server user remove alice
hi server stop --user alice
```

Removing a user refuses their devices at once. Their running machines keep
running until stopped or until their limit.

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

This release manages RunPod. Admins approve from Slack or the server box.
Budgets and a live dashboard come in later releases.

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

If the approved hardware is sold out by the time it starts, the approval
still counts. The server starts the cheapest free hardware with at least as
much memory, costing at most twice the approved price, and says so:

```text
rtx-4090-1c2d is running on rtx-3090 ($0.50/h); the approved l4 ($0.49/h) was sold out.
```

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

### Approve from Slack

The server can post every request to a private Slack channel, with buttons
to approve, deny, and stop. It connects to Slack over Socket Mode, which is
an outbound connection, so the server needs no public address.

1. At [api.slack.com/apps](https://api.slack.com/apps), choose **Create New
   App → From a manifest** and paste the output of
   `hi server slack manifest`. Install it to your workspace.
2. Create a private channel, such as `#compute-approvals`, and invite the
   app with `/invite @hi compute`.
3. On the server box, run `hi server slack setup`. It asks for the **Bot User
   OAuth Token** (`xoxb-…`), an **App-Level Token** with `connections:write`
   (`xapp-…`, under Basic Information), and the channel's ID (at the bottom
   of the channel's details). It posts a test message, then saves them.
4. Add each approver by Slack member ID (profile → ⋮ → Copy member ID) and
   the `hi` user name they request compute as:

   ```sh
   hi server approvers add U0123456789 --name bob
   ```

5. Restart the server: `sudo systemctl restart hi-server`.

In the channel:

- **Join requests** have *Approve as staff*, *Approve as student*, and
  *Deny…* buttons.
- **Compute requests** show the user, hardware, price, maximum cost, and
  reason, with *Approve* and *Deny…*. *Deny…* asks for a reason, which the
  requester sees.
- The message changes as the request moves on: 🔵 starting, 🟢 running with a
  **Stop** button, ⚪ stopped with its run time and cost, 🔴 denied, ❌ failed.
- Its thread records the start, a replacement for sold-out hardware, a
  warning at 80% of the time limit, and the stop.
- A machine on the provider account that the server didn't start gets its
  own alert.

Only approvers can click or use `/hi`; anyone else is told so, and the
attempt is logged. Nobody can approve their own request, so an approver's
own requests need another approver.

| Command                  | Does                                                  |
|--------------------------|-------------------------------------------------------|
| `/hi status`             | What is running, the rate right now, with Stop buttons |
| `/hi stop <name>`        | A Stop button for one machine                         |
| `/hi stop user <user>`   | A button to stop everything that user runs            |
| `/hi stop all`           | A button to stop everything                           |

Answers are visible only to you, and every stop needs a click to confirm.

### Watch and stop

```sh
hi server ls                      # everything running, for every user
hi server stop rtx-4090-1c2d
hi server stop --user alice       # everything alice runs
hi server stop --all
hi server audit --since 24h       # requests, approvals, starts, and stops
```

When approved hardware is sold out, the server starts a replacement with
at least as much memory at up to twice the approved price, and records it in
the audit log. Change the bound with `"fallback_price_factor"` in the
server's `config.json` (`1` turns replacements off), then restart the server.

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

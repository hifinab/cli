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
Approved by bob.
Starting: Created pod 7xk2q9; waiting for it to start (this can take a few minutes)...
Starting: running
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
much memory, costing at most twice the approved price or the approved price
plus $1.00/h, whichever is higher, and says so:

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

### Ask for more time

```sh
hi compute extend rtx-4090-1c2d 2h --reason "needs two more epochs"
```

An extension goes through the same approval as a start. Once approved, the
machine's limit moves, and `hi compute ls` shows the new time.

### Budgets

If your group has a monthly budget, `hi connect status`, `hi compute ls`, and
every request show what you have spent this month:

```text
This month: $42.10 of iman's $100.00 · staff $120.00 of $300.00
```

Going over a budget never blocks you. The request shows a warning, the
approvers see it too, and it waits for a person even if the policy would
otherwise approve it by itself.

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

An agent that runs on its own, such as a scheduled job on a build server,
joins as itself with a person responsible for it:

```sh
hi connect compute.internal --agent build-bot --owner iman
```

Approvers see it as 🤖 **build-bot** (owner iman), with an *Approve as
agent* button that puts it in the `agents` group. Requests from an agent on
someone's laptop show as `iman via Claude Code`; set `HI_AGENT` to name any
other agent.

### Link your Slack account

Link your Slack account to get a message when your request is approved,
denied (with the reason), started, near its time limit, or stopped, and to
see and stop your machines from the app's Home tab in Slack:

1. In Slack, type `/hi link <your hi user name>`, such as `/hi link iman`.
2. Run the command it replies with on your own device, within 10 minutes:

   ```sh
   hi connect slack 4F7K2Q
   ```

The code only works from a device that is connected as that user, so nobody
can link themselves to someone else. `/hi unlink` undoes it.

### Watch your machines live

```sh
hi compute live
```

A full-screen view of your managed machines: running time, cost so far, a
bar towards each time limit, your requests, and your budget. Select a
machine with the arrow keys and press `s` to stop it, or `q` to quit.

### Use the team's model with hi q

When the server serves a model, [`hi q`](/guide/q/) uses it without a key or
any setup, and `hi q --setup` lists it first. Your choice of model is
yours; the server passes requests to the upstream with the team's key.
`hi q --status` shows "… via <server>".

If the server can't be reached, `hi q` falls back to your own key or Claude
Code, if you have one, and says so in a dim line. Without one, it stops
until the server is back.

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

### Update the server

As the account the server runs as:

```sh
hi update
```

After installing the new binary, `hi update` finds the running server, which
still runs the old one, and asks to restart its systemd service with
`sudo systemctl restart hi-server.service`. It is down for about a second.
It also asks when the binary is already current but the server was never
restarted. `--restart` restarts without asking, for scripts, and
`--no-restart` only prints the command. A server started by hand, outside
systemd, is reported but not restarted.

State, keys, and devices carry over; connected devices need no change.
Devices on an older `hi` keep working, and get new features when they run
`hi update` themselves.

If systemd warns that `hi-server.service` changed on disk, compare
`systemctl cat hi-server` with what you meant to change, then run
`sudo systemctl daemon-reload` and restart. The reload alone doesn't restart
the server.

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

### Policy, budgets, and reports

With no policy, every start needs a person. A policy lets groups run cheap,
short jobs without waiting, caps what each group may use, and sets monthly
budgets:

```sh
hi server policy example > /tmp/policy.json   # a starting point
hi server policy edit                         # opens $EDITOR, checks, saves
hi server policy show
```

| Setting                     | Meaning                                                   |
|-----------------------------|-----------------------------------------------------------|
| `max_hours`                 | Refuse starts and extensions that would run longer        |
| `hardware`                  | Refuse other hardware                                     |
| `auto_approve`              | Approve without a person up to this price and total time  |
| `user_monthly_budget_usd`   | Warn when a user in the group goes over                   |
| `group_monthly_budget_usd`  | Warn when the whole group goes over                       |

Budgets warn and never block. An over-budget request shows ⚠️ and the spend
in Slack, and always waits for a person. The channel gets one alert a month
when a user or group crosses its budget. Changes apply to the next request;
no restart needed.

At 09:00 server time the channel gets a report: yesterday's spend every
day, the week by user and group with the most-used hardware and silent
devices on Mondays, and the month on the 1st. Turn any off with
`"reports": {"daily": false}`.

```sh
hi server spend                 # this month, per user and group
hi server spend --since 7d
```

In Slack:

| Command                         | Does                                           |
|---------------------------------|------------------------------------------------|
| `/hi spend`                     | This month's spend per user and group          |
| `/hi users`                     | Users, groups, devices, and spend              |
| `/hi audit [user]`              | The last 15 audit entries                      |
| `/hi budget <group> <usd>`      | Set each user's monthly budget in a group      |
| `/hi budget <group> total <usd>` | Set the whole group's monthly budget          |

### Slack Home tab

Open the hi compute app in Slack for a dashboard that is rebuilt each time
you open it:

- **Now:** each running machine with its user, hardware, cost so far, and a
  **Stop** button, plus **Stop all…**.
- **Waiting:** requests with **Approve** and **Deny…** buttons.
- **This month:** spend per group against its budget.
- **Devices:** each device, when it was last seen, and its `hi` version,
  flagged when it is older than the server's or silent for a week.

A linked user who isn't an approver sees only their own machines, requests,
and budget there, and can stop their own machines. Anyone else is told how
to link.

The Home tab needs two settings in the Slack app that older setups lack. At
[api.slack.com/apps](https://api.slack.com/apps), open the app, choose **App
Manifest**, replace it with the output of `hi server slack manifest`, save,
and reinstall if Slack asks.

### Live dashboard

On the server box:

```sh
hi server live
```

```text
hi compute · live                                        Tue 29 Sep 14:32:05
3 running   $2.07/h now   $4.10 today   $41.20 this month of $2500.00
────────────────────────────────────────────────────────────────────────────
Running
▸ train     iman via Claude Code staff  runpod l4    48m  $0.39  ████████░░ stops 14:44
    fine-tune pricing model   ssh · tunnel
  sweep     sam        students  runpod rtx-4090@community  12m  $0.07 ██░░░░░░░░ stops 15:20
    thesis LoRA sweep   community cloud
Waiting
  dana      students  a100 2h                waiting 3m
Budgets this month
  staff      ███░░░░░░░░░░░░░  $38.10 of $2000.00
Activity
  14:31  sam opened ssh to sweep
  14:29  dana requested runpod/big
↑↓ select · a approve · d deny · s stop · q quit
```

It updates every second. `a` approves the selected request, `d` asks for a
reason and denies it, and `s` stops the selected machine after a yes; each
is logged and posted in Slack like a button click. The second line of each
machine shows the reason it was requested and what its user is doing:
connected clients report when they open `ssh`, a tunnel, `logs`, or
`serve`, with the command and instance names only.

### A wall screen

`hi server live --wall` is the same dashboard, read-only, for a shared
screen. It shows initials instead of names and hides reasons (`--names full`
and `--reasons` turn them on), fills whatever screen it gets, and takes
turns showing what doesn't fit. If the server stops answering, it dims and
says `Reconnecting…` instead of going blank.

To show it on screens without installing anything on them, run it once in a
tmux session on the server box that screens attach to over SSH:

```sh
sudo ~/.local/bin/hi server wall setup
sudo ~/.local/bin/hi server wall add office-tv ~/office-tv.pub
```

Then on the screen: `ssh -t hi-wall@<server box>`.

- The session runs as a separate `hi-wall` account with a viewer key: it can
  watch the dashboard and nothing else, and cannot read the server's state
  or provider keys.
- A screen's key can only attach read-only (`tmux attach -r`): asking for a
  shell or another command still gives only the dashboard, and port
  forwarding is refused.
- A systemd service (`hi-wall-live`) keeps the session running across
  crashes and reboots. Rerun `setup` after updating `hi`.
- All screens on one session share its size. For a screen of another size,
  add a session: `sudo hi server wall setup --session tv --size 240x67`, and
  `wall add … --session tv`.

A device elsewhere can also run `hi server live --wall` itself, if an admin
makes it a viewer with `hi server viewer add <name> --key <key from hi connect key>`.

### Serve private project templates

The server can also hand connected devices private project templates and agent skills, which `hi init` then offers next to the built-in
ones. Users never need access to the repository on GitHub.

```sh
hi server templates add private https://github.com/<org>/<private-repo>
                                  # asks for a read-only token, shown as *
hi server templates list          # each source, its commit, layers, and skills
hi server templates sync          # fetch now instead of within 15 minutes
hi server templates rename private team   # keeps the token and history
hi server templates remove private
```

- Use a fine-grained GitHub token limited to that one repository with
  read access to contents: on GitHub, Settings → Developer settings →
  Personal access tokens → Fine-grained tokens, with the organization as
  resource owner. It stays in the server's `keys.json`. An `ssh://` or
  `git@` URL uses the server account's SSH key instead, such as a deploy key,
  where the organization allows them.
- When the token expires, fetching fails and the channel gets an alert, but
  devices keep the last good commit. Run `remove` and `add` again with a new
  token; the commits stay the same, so nothing devices cached changes.
- Every git command gives up after 2 minutes, so a slow GitHub shows up as a
  problem in `hi server templates list` instead of blocking the commands.
- Every new commit is checked before devices get it: valid `layer.json`
  files, no built-in template names, and skills with a `SKILL.md`. A commit
  that fails keeps the previous one in service and posts an alert.
- Devices check that each bundle is signed with the server's key, which they
  stored the first time they connected.
- To keep a group from seeing a source, list the ones it may use in
  `policy.json`: `"template_sources": ["private"]`, or `[]` for none.

### Serve a model to hi q

The server can pass `hi q`'s requests to OpenRouter, or any
OpenAI-compatible endpoint, with one team key, so nobody needs a key of
their own:

```sh
hi server ai set                       # asks for the OpenRouter key, shown as *
hi server ai set --model google/gemini-2.5-flash   # change the default
hi server ai set --url http://aiw11.hi.fin:8731/v1 --model <name> --no-key
                                       # a model on your own machine, no key
hi server ai                           # the setup and this month's use per user
hi server ai off                       # stop serving; keeps the key
hi server ai remove                    # stop and delete the key
```

- It is a plain pass-through: users and agents may ask for any model the
  upstream has. The default, `anthropic/claude-haiku-4.5` unless you set
  another, is what `hi q` uses when the user hasn't chosen one.
- `set` makes one small request with the key and model before saving them.
  Run it again to change them; Enter at the key prompt keeps the stored key.
- Each request is recorded in `ai_usage.jsonl` with the user, device, model,
  tokens, and cost, never the messages or the answer. `hi server spend`
  shows it under "Models through hi q".
- Nothing in hi limits spending on models. Set a credit limit on the key at
  OpenRouter as the backstop.
- `hi server live` and the Slack Home tab show this month's model spend next
  to compute.
- Prompts carry the user's shell history and folder names. The server
  passes them on and stores none of them, but whoever runs it could read
  them in transit; tell your users.

#### A model on your own machine

Any OpenAI-compatible server on the network works as the upstream, such as
llama.cpp's `llama-server`, vLLM, or Ollama (`http://<host>:11434/v1`), and
prompts then never leave your network. `--no-key` is for servers that take
no key; `--model` must be a name from the server's `/v1/models`.

It needs a model that calls tools well, since `hi q` lists and reads files
through tool calls. Tested on 2026-10-02 with `halogen-qwen3.8-flash-next`
(Qwen 3.8 Flash Next) served on the Strix Halo workstation aiw11: correct
commands and tool calls, 4–14 seconds an answer, against about 2 seconds for
Claude Haiku 4.5 through OpenRouter. Large models need most of the machine's
memory, so share it knowingly: a 4-bit Qwen 3.8 Flash Next is about 110 GB.

Switch back to OpenRouter with `hi server ai set` without `--url`; the
stored key is kept.

### Watch and stop

```sh
hi server ls                      # everything running, for every user
hi server stop rtx-4090-1c2d
hi server stop --user alice       # everything alice runs
hi server stop --all
hi server audit --since 24h       # requests, approvals, starts, and stops
```

When approved hardware is sold out, the server starts a replacement with
at least as much memory, and records it in the audit log. A replacement may
cost up to twice the approved price or the approved price plus $1.00/h,
whichever is higher, so cheap hardware gets a lot of room and expensive
hardware stays near 2×:

| Approved | Replacement up to |
|----------|-------------------|
| $0.24/h  | $1.24/h           |
| $0.49/h  | $1.49/h           |
| $1.00/h  | $2.00/h           |
| $3.49/h  | $6.98/h           |

Change it with `"fallback_price_factor"` (default `2`) and
`"fallback_price_extra"` (default `1.0`) in the server's `config.json`, then
restart the server. A factor of `1` and an extra of `0` turn replacements
off.

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

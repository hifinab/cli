# `hi server` and `hi connect` specification

Status: Approved

Dependencies: `hi compute` provider drivers (v0.6.0–v0.8.0), `hi net`
(NetBird), a Slack workspace with a private channel for compute managers.

## Goal

Let employees and students use paid GPU compute through `hi compute` without
holding any provider key, and let a few admins approve runs, follow spending,
and stop anything that runs amok, all from Slack.

- Managed users never hold an organization RunPod, Hugging Face, or other
  provider key. Admins manage one key per provider, in one place.
- Every paid start is approved by a person or by a rule the admins wrote.
- Admins see everything that is running, what it costs, and who started it,
  and can stop any of it in one action.
- Slack is the whole admin interface: approvals, reports, and management. There
  is no web page to host, secure, or sign in to.
- A live terminal dashboard, `hi server live`, shows what is running and what
  users are doing as it happens. It can run full screen on a wall display.
- For users, `hi compute` stays the same commands, with one extra wait for
  approval.
- Management is opt-in. A `hi` that has never run `hi connect` works exactly
  as it does today, with the user's own keys on their own machines. Colab is
  never managed.
- The server is also where connected devices get private project templates
  and agent skills, so the team's own knowledge never has to be public or
  handed out as repository access (see [Template sources](#template-sources)).

## Design choice: broker, don't distribute

The server keeps the provider keys and makes every provider call itself. A
connected client asks the server to start, list, and stop instances; it never
receives a provider key.

Distributing keys, even encrypted or through the server, was considered and
rejected:

- A key on a laptop can be copied and used outside `hi`, which skips approval
  entirely.
- Most providers have no per-user or scoped keys (RunPod has none), so taking
  access away from one person means rotating the key for everyone.
- Stopping runaway instances needs a list of everything running. A broker has
  that list by construction.

Provider-native team features (RunPod teams, Hugging Face organization roles)
were also considered. They need a provider account per user, differ per
provider, and have no approval step.

The existing `computeProvider` interface makes brokering cheap. The server runs
the real drivers. The client gets one new driver, `managed`, that forwards each
interface call to the server.

## Managed and unmanaged

Management applies only to a device that has joined a server with
`hi connect`, and only to the providers that server offers.

- **Never connected:** nothing changes. `hi login <provider>` and the
  provider keys in their standard locations work as today, `hi compute` talks
  to providers directly, and nothing is reported anywhere. Someone can use
  `hi` on their own machines with their own keys and never know that
  `hi server` exists.
- **Connected, provider offered by the server:** that provider goes through
  the server, with approval, leases, and limits. The user's own key for that
  provider, if they have one, is not used.
- **Connected, provider not offered by the server:** the provider works as
  today with the user's own key. Nothing about those instances is sent to the
  server.
- **Colab is never managed.** A Colab runtime runs under the user's own
  Google account or Google Workspace sign-in, with that account's compute
  units, and there is no organization key a server could hold. Colab always
  works as today, connected or not. It is left out of approvals, the
  reconciler, Slack, and the live dashboard.
- **`hi disconnect`** deletes the device key and the server address, and
  returns the device to the never-connected behaviour. Instances already
  started through the server stay under the server's limits until they stop.

`hi compute ls` and `hi compute providers` label each provider and instance
as `managed` or `own`, so it is always clear who pays and who can stop it.

## Roles

| Role | Who | Can |
|---|---|---|
| User | Employees and students with an enrolled device | Request compute; list, reach (`ssh`, `tunnel`, `logs`) and stop their own instances |
| Approver | Slack users on the server's approvers list, normally the members of `#compute-approvals` | Approve or deny requests and enrollments; stop any instance; see all reports |
| Admin | One to three people, also approvers | Everything above, plus users, groups, and policy. Provider keys change only on the server box. |
| Viewer | A device that drives a wall display, not a person | Read the live dashboard stream; nothing else |

Users belong to groups (for example `staff` and `students`) and policies attach
to groups.

### AI agents

An AI agent can do everything a user can, through the same `hi` commands, and
nothing more. There are two cases:

- **An agent working for a person on their device**, such as Claude Code or
  Codex on a student's laptop, acts as that person. It uses the device key,
  gets the same group limits and budget, and its requests go through the
  same approval. Slack and the dashboard show it as `alice via Claude Code`
  when `hi` can tell. `hi` recognises the agents' own environment variables,
  and `HI_AGENT=<name>` covers any others.
- **An agent running on its own**, such as a scheduled job on a build server,
  enrolls its own device with `hi connect --agent <name> --owner alice`. It
  becomes a user of kind `agent`, with a human owner who is shown on every
  request, and normally sits in its own group (for example `agents`) with
  its own limits and budget.

The rules are the same for both:

- **Agents never approve.** A person in Slack or a policy rule does. No one,
  person or agent, can approve a request made under their own name, so an
  agent on an approver's laptop can't approve its own requests.
- **Agents can't be prompted.** Without a terminal, `hi` never prompts:
  `--reason` is required, and `--yes` confirms the price as it does today.
  The `hi skill` rule still applies: an agent passes `--yes` only after its
  person has agreed.
- **Waiting must fit an agent's command time limit.** `hi compute up
  --no-wait` returns at once with the request ID and exit status 3 (pending).
  `hi compute requests <id> --wait [--timeout 10m]` waits for the decision.
  A denial exits with status 4 and prints the approver's reason. `--json`
  gives the same information in a form agents can read.
- **Agents are logged like everyone else.** Every request, start, and stop
  records the user, the device, and the agent name.

`hi skill` teaches agents this flow when the device is connected: send the
request, tell the person it is waiting in Slack, and never retry a denied
request by changing the hardware to get around it. Users do not need to be in Slack; the CLI tells them everything
they need. Linking a user to their Slack account is optional and only adds
direct-message notifications.

## Architecture

```text
 laptop (in VPN)                 hi server (dedicated box, in VPN)            outside
┌──────────────┐  HTTPS over   ┌─────────────────────────────────────┐
│ hi compute   │  NetBird,     │ API        ── requests, leases       │── REST ──▶ RunPod, HF,
│  managed     │──signed by ──▶│ policy     ── auto-approve, budgets  │            Vast.ai, ...
│  driver      │  device key   │ reconciler ── every 30 s: list, stop │
└──────┬───────┘               │ reporter   ── alerts, daily/weekly   │
       │ ssh / tunnel          │ state      ── JSON file, audit log   │── Socket ─▶ Slack
       ▼ (direct, user's key)  │ provider keys (never leave the box)  │   Mode      #compute-approvals,
  GPU instance                 └─────────────────────────────────────┘   (outbound)  App Home, /hi
```

- **Server:** one `hi` binary on a small always-on machine enrolled in NetBird
  (`hi server run`, as a systemd service). It listens only on its NetBird
  address, where it serves the client API and nothing else. The one
  exception is a second listener with only the `hi data` proxy, which it
  publishes with `netbird expose` while cloud runs need it
  ([hi_server_expose.md](../ideas/hi_server_expose.md)). It keeps its state
  in a JSON file with an append-only JSON Lines audit log, which is enough for
  one organization and adds no database to the binary. Its provider keys live
  in a `0600` file on an encrypted disk.
- **Slack:** the server connects to Slack through Socket Mode, which is an
  outbound WebSocket. Button clicks, slash commands, forms, and App Home all
  arrive over that connection. Slack never needs to reach the server, so the
  server stays private inside the VPN with no public address and no
  certificate.
- **Live events:** the server keeps an event stream (requests, approvals,
  starts, activity, cost ticks, stops) that the live dashboard subscribes to
  over the same signed client API. Connected devices also report each
  `hi net expose` (address, port, lock, end time), which goes to the audit
  log and the feed ([hi_net_expose.md](../ideas/hi_net_expose.md)).
- **SSH and tunnels:** the SSH connection goes straight from the laptop to the
  instance, not through the server. The server puts the user's SSH public key
  on the instance when it creates it, so the server never handles a user's
  session.

## Slack interface

The server is one Slack app with four parts. Everything an approver can do is
checked against the approvers list when they click or type, not when the
message was posted.

### Approvals channel (`#compute-approvals`)

A private channel where the server posts everything that needs a decision or
attention. Every request gets one message, and everything that follows goes
in that message's thread, so each run's whole history sits in one place.

- **Enrollment requests:** buttons **Approve as staff**, **Approve as
  student**, and **Deny**.
- **Compute requests:** buttons **Approve**, **Approve with changes…**, and
  **Deny…**. **Approve with changes…** opens a form to shorten the max or
  switch to cheaper hardware. **Deny…** asks for a reason, which the user sees
  in the CLI.
- **Running instances:** the original message is edited in place to show the
  current state (`🟡 waiting`, `🟢 running 1h12m · $4.19`, `⚪ stopped · $10.47`)
  and always has a **Stop** button while the instance runs.
- **Alerts:** 80% of the max, cost cap reached, an instance stopped by the
  reconciler, an instance on an organization account that has no lease, a
  provider refusing requests, or a failing provider key.

### App Home (the dashboard)

Opening the app in Slack shows a live page, rebuilt each time it is opened:

- **Now:** each running instance with user, provider, hardware, running time,
  cost so far, and a **Stop** button. A **Stop all…** button asks for
  confirmation first.
- **Waiting:** pending requests with the same buttons as in the channel.
- **This month:** spend per group and the top users against their budgets.
- **Devices:** enrolled devices, their `hi` version, and last contact, with
  outdated or silent ones flagged.

Approvers see all of this. A linked user who opens App Home sees only their
own instances, requests, and budget.

### Slash command `/hi`

For anything that is not a button:

```text
/hi status                        what is running now, with stop buttons
/hi spend [7d|30d|month] [user]   spend per user and group
/hi stop <name> | user <u> | all  stop, with a confirmation for user and all
/hi users [group]                 users, groups, devices, budget left
/hi user move <u> <group>         change a user's group
/hi user remove <u>               revoke all their devices; offers to stop their instances
/hi budget <group> <usd>          change a group's monthly budget
/hi policy                        show the current policy
/hi audit [user] [7d]             recent requests, approvals, starts, and stops
```

The command replies privately unless it changes something. Changes also post
a line in `#compute-approvals` so the other approvers see them.

### Scheduled reports

- **Daily, in the channel:** yesterday's spend, runs started, anything stopped
  by the reconciler, and requests still waiting.
- **Weekly, in the channel:** spend per group and user against budget, the most
  used hardware, and devices that have not reported in over a week.
- **Monthly, in the channel:** a summary for invoicing or cost allocation,
  with a CSV attached.

### What Slack never shows or accepts

Slack never shows or accepts provider keys or device keys. Provider keys
change only through `hi server provider add` on the server box, so a
compromised Slack account can spend money within policy but cannot take the
keys.

## Live dashboard (`hi server live`)

A full-screen terminal dashboard built with Bubble Tea and Lip Gloss, which
`hi` already uses for the compute menu. It connects to the server's event
stream and redraws as things happen, so the screen is never more than a
second or two behind.

```text
╭─ hi compute · live ───────────────────────────────── Tue 29 Sep 14:32 ─╮
│  5 running    $18.40/h now    $126 today    $2,310 of $4,000 in Sep   │
├─ Running ─────────────────────────────────────────────────────────────┤
│  alice     staff     RunPod  H100 SXM   1h12m  $4.19   ███████░░░ 3h  │
│            fine-tune pricing model        ssh · tunnel :8080          │
│  bob       staff     HF      A100 80G     22m  $0.92   █░░░░░░░░░ 4h  │
│            eval harness, run                                          │
│  chen      students  RunPod  RTX 4090     48m  $0.59   ████░░░░░░ 2h  │
│            thesis: LoRA sweep 3/8         ssh                         │
├─ Waiting ─────────────────────┬─ Budgets this month ──────────────────┤
│  dana  students  A100  2h     │  staff     ████████████░░░░  $1,840   │
│   → approve in Slack   3m ago │  students  ██████░░░░░░░░░░    $470   │
├─ Activity ────────────────────┴───────────────────────────────────────┤
│  14:31  chen opened ssh to lora-sweep                                 │
│  14:29  dana requested A100 80G for 2h                                │
│  14:20  bob started eval-harness (auto-approved)                      │
│  14:02  erik's train-run stopped at its 2h max · $6.98                │
╰───────────────────────────────────────────────────────────────────────╯
```

The panels:

- **Header:** instances running, spend rate right now, spend today, and the
  month against the organization's total budget.
- **Running:** every instance with its user, group, provider, hardware,
  running time, cost so far, and a bar towards its max. The bar turns yellow
  at 80% and red at the cost cap. The second line shows the reason given at
  request time and what the user is doing now, such as an open `ssh`
  session, a tunnel, or a run's progress when it reports one.
- **Waiting:** pending requests and how long they have waited.
- **Budgets:** spend per group against its monthly budget.
- **Activity:** a scrolling feed of requests, approvals, starts, SSH sessions
  and tunnels opened, alerts, and stops.

### Two modes

**Interactive (default)** is for approvers at their own terminal. Arrow keys
select an instance or request. `a` approves, `d` denies with a reason, `s`
stops with a confirmation, `enter` opens a detail view with the instance's
full thread history, and `/` filters by user, group, or provider. Every action
goes through the same checks and audit log as the Slack buttons, and posts in
the Slack thread.

**Wall (`--wall`)** is for a shared screen:

- It is read-only, with no selection, keys, or buttons except `q` to quit.
- It fills whatever terminal size it gets and adds panels as the screen
  widens: a 4K screen at a large font shows everything, and a small one shows
  Header and Running only.
- The header and the Running panel always stay on screen. When the other
  panels don't fit, they rotate every 20 seconds.
- For privacy it hides reasons and shows user names as initials by default.
  `--names full` and `--reasons` turn them on for rooms where that is fine.
- If the server connection drops, it keeps the last picture, dims it, and
  shows `Reconnecting… last update 14:32` instead of going blank.

### Showing it on a screen: tmux over SSH

The wall dashboard runs once, on the server box, inside a tmux session.
Every screen just attaches to that session over SSH, so a display needs
nothing but an SSH client and a terminal: no `hi`, no enrollment, no keys
beyond its SSH key.

```text
server box                                       any screen in the VPN
┌──────────────────────────────────────────┐
│ user hi-wall (no access to provider keys)│      ssh -t hi-wall@compute.internal
│  tmux session "live"                     │◀──── (forced read-only attach)
│   └─ hi server live --wall               │◀──── TV, laptop, Raspberry Pi
│       └─ viewer key → server event stream│
└──────────────────────────────────────────┘
```

`hi server wall setup` configures it:

1. It creates a separate `hi-wall` Unix account with no sudo and no read
   access to the server's state or provider keys. The dashboard in the tmux
   session runs as that account, using a viewer key, just like any other
   viewer device. Even a screen that somehow broke out of tmux would find
   nothing to steal.
2. It adds a systemd unit that keeps the tmux session `live` running
   `hi server live --wall`. It restarts the session after a crash or a
   reboot.
3. It lets screens in through `hi server wall add <name> <ssh-pubkey>`. Each
   key goes into `hi-wall`'s `authorized_keys` with
   `restrict,pty,command="tmux attach -r -t live"`. The forced command gives a
   read-only attach with no shell and no port forwarding, whatever command
   the screen asks for.

Any approver can also glance at it from a laptop with the same
`ssh -t hi-wall@compute.internal`. `ctrl-b d` or closing the window
detaches without affecting the other screens.

The catch is size: every screen attached to one tmux session shows the same
window size. Screens of the same size work best. For a mix, `hi server wall
setup --session tv --size 240x67` adds a second session with its own size,
and each screen's key is tied to the session it should show.

A device that runs `hi server live --wall` itself, as an enrolled viewer,
still works when a screen needs its own size or its own `--names` setting.

### What users are doing

"Doing" comes from three sources, so it never requires reading a user's
session:

1. **Lifecycle:** requested, approved, started, stopped, from the server
   itself.
2. **Client activity:** the managed client reports when it opens or closes an
   `ssh` session, a `tunnel`, `logs --follow`, or a `serve` endpoint. It
   reports the command name and instance only, never arguments, commands typed
   in the session, or file names.
3. **Provider metrics, where the provider offers them:** GPU utilization and
   memory, which let the dashboard flag an instance that has sat at 0% GPU
   for a long time as probably idle.

Users can also see their own part with `hi compute live`, which is the same
screen limited to their own instances, requests, and budget.

## Template sources

`hi` ships generic project templates (`python`, `web`) built in
([hi_init.md](hi_init.md)). Private templates and skills, such as
quant research layouts, backtest guards, and the `data-access` skill, must not
be public. The server distributes them, in the same way it brokers provider
keys: it holds the repository access, and enrolled devices ask it for what
they need.

### Adding a source

```text
hi server templates add private https://github.com/hifinab/templates [--ref main]
```

- `<name>` is how devices and `.hifin/template.json` refer to the source. It
  must not be `builtin` or `local`.
- For an `https://` URL the server asks for a read-only token (a GitHub
  fine-grained token limited to that one repository, with contents read
  access), showing `*` per character like `hi server provider add`. The token
  is stored in `keys.json` next to the provider keys and never leaves the
  server box. An `ssh://` or `git@` URL uses the server account's own SSH key
  instead, for example a GitHub deploy key.
- `--ref` is a branch or tag; the default is the repository's default branch.
- Before saving, the server clones the repository into
  `templates/<name>.git` in its state folder and checks it: each
  `<layer>/layer.json` must be valid, layer names must not reuse built-in
  names, and every skill must have a `SKILL.md`. A source that fails is not
  added, and the errors are printed.

### Keeping it current

- The server fetches each source every 15 minutes, and at once with
  `hi server templates sync [<name>]`. A new commit on the ref becomes the
  source's current commit only if it passes the same checks; otherwise the
  previous commit stays current and the channel gets an alert.
- Every change of current commit is written to the audit log with the old
  and new commit and the layers and skills it contains.
- The mirror keeps history, so the server can serve any commit it has served
  before. That lets `hi init --update` compare a repository's recorded commit
  with the current one.
- `hi server templates rename <name> <new-name>` keeps the mirror, token,
  and commits; devices fetch the source again under its new name.
- `hi server templates list` shows each source, its URL, ref, current commit,
  last fetch, and layers and skills. `hi server templates remove <name>`
  stops serving it and deletes the mirror and its token.

### Serving devices

Two calls on the signed client API, for enrolled user devices only (viewer
devices get nothing):

- `GET /v1/templates`: each source the user's group may see, with its current
  commit, layers (name, summary, extends, required `hi`), and skills.
- `GET /v1/templates/<source>/<commit>`: the source at that commit as a
  `tar.gz` archive (`git archive`), with the server's signature over the
  source name, commit, and archive digest.

`hi init` on the device uses these as described in
[hi_init.md](hi_init.md#server-templates-on-the-device). A group's access is
set in policy with `template_sources`; with no such field the group sees
every source.

### Why the server signs

Templates run commands on the device (`uv sync`, `npm ci`) and put code and
agent instructions into new repositories, so a device must know they came
from its own server. The client API is plain HTTP inside NetBird and so far
only the device signs. For templates the server signs too:

- `hi server init` creates a server ed25519 key in the state folder. An
  existing server creates it on first start after the upgrade.
- `hi connect` receives the server's public key in the enrollment answer and
  stores it in `server.json`. A device enrolled before this release receives
  it on its next call and prints the fingerprint once.
- The device refuses a bundle whose signature does not match the stored key.

Whoever can change the source repository can change what every new
repository gets. That is the same trust as any shared code; the audit log
and the commit shown in every `hi init` plan make it visible.

## Workflows

### 1. Admin sets up the server (once)

```text
hi net <setup-key>                  # enroll the box in NetBird
hi server init                      # create state, print the server's address
hi server provider add runpod       # prompt for the key, no echo, stored 0600
hi server slack setup               # prints the app manifest to install,
                                    # asks for the bot and app tokens,
                                    # picks #compute-approvals and approvers
hi server policy edit               # groups, limits, auto-approve rules
hi server templates add private https://github.com/hifinab/templates
                                    # optional: private templates
sudo systemctl enable --now hi-server
```

`hi server slack setup` prints a ready-made Slack app manifest with Socket
Mode, the `/hi` command, App Home, and the smallest set of scopes enabled, so
the admin pastes it into Slack instead of configuring the app by hand.

### 2. User enrolls a device

Keys are pre-approved: nothing connects unless an approver has approved its
public key.

1. The user runs `hi connect compute.internal` (the server's NetBird name).
2. `hi` creates an ed25519 device key in `~/.config/hi/device_key` (`0600`).
   The private key never leaves the device.
3. The server posts the enrollment request in `#compute-approvals` with user
   name, hostname, public key fingerprint, and NetBird peer name. The user can
   add their Slack handle to receive direct messages.
4. An approver clicks **Approve as staff**, **Approve as student**, or
   **Deny**. An admin can also pre-register a key with
   `hi server user add alice --key <pubkey>`, and then no request is needed.
5. `hi connect` waits and reports the result. From then on every request to
   the server is signed with the device key.

`/hi user remove alice` refuses her devices' next requests at once and offers
to stop her instances.

### 3. User requests compute

`hi compute up --gpu H100 --max 3h` works as before. The difference is where
the request goes:

1. The client sends the request to the server. It includes provider,
   hardware, `--max`, the user's SSH public key, and a short reason. `hi` asks
   for the reason, or it can be given with `--reason`.
2. The server checks it against policy. The request can be refused outright
   (hardware not allowed for the group, or longer than it may run), approved
   automatically by a rule, or sent to Slack.
3. Slack shows:

   ```text
   🟡 alice (staff, laptop-alice) requests compute
      RunPod · H100 SXM · $3.49/h · max 3h → at most $10.47
      Reason: fine-tune run for the pricing model
      This month: $42 of $200
      [Approve]  [Approve with changes…]  [Deny…]
   ```

4. The client waits and shows `Waiting for approval in #compute-approvals…`.
   Ctrl-C leaves the request pending, and `hi compute requests` shows it later.
   An unanswered request expires after 30 minutes.
5. On approval the server creates the instance and records a lease: user,
   instance, hardware, price, max lifetime, and cost cap. It then edits the
   Slack message to `🟢 approved by bob · running`. The client prints the name
   and SSH details as `hi compute up` does today. A linked user also gets a
   direct message.
6. If the approved hardware is sold out when the server starts it, the
   approval still counts: the server starts the cheapest free hardware with
   at least as much memory, costing at most `fallback_price_factor` (default
   2) times the approved price or the approved price plus
   `fallback_price_extra` (default $1.00/h), whichever is higher, and tells
   the user and the audit log.

`hi compute run` follows the same flow.

### 4. While it runs

- The reconciler lists every provider account every 30 seconds and compares
  what is running against the leases.
  - It stops any instance past its max or its cost cap. The thread gets a
    warning at 80% of the max, and a final post with the total cost when it
    stops.
  - It posts an alert for any instance with no lease, such as one started by
    hand on the provider's website. Stopping it automatically is a policy
    setting.
- The user can see and stop only their own instances. `hi compute ls`, `ssh`,
  `tunnel`, `logs`, and `stop` work unchanged through the managed driver.
  Stopping never needs approval: `hi compute stop <name>` asks the server,
  which terminates the instance at once, ends the lease, and edits the Slack
  message to `⚪ stopped by alice · $6.98`. `hi compute stop --all` stops all
  of the user's own managed instances and nobody else's.
- A user can ask for more time with `hi compute extend <name> 2h`. This posts
  a new approval request in the same thread.

### 5. Stopping things that run amok

Approvers can stop from any of these:

- the **Stop** button on the instance's message in `#compute-approvals`,
- the **Stop** and **Stop all…** buttons in App Home,
- `/hi stop <name>`, `/hi stop user alice`, or `/hi stop all`.

Stops by user and stop all ask for confirmation in a Slack form first. Every
stop is written to the audit log and posted in the thread.

If Slack is down, an admin on the server box can run
`hi server stop <name> | --user alice | --all`.

### 6. Client reporting

A connected client reports to the server on every `hi` command. An optional
user systemd timer also sends a heartbeat every 5 minutes. A report contains
only the user, hostname, `hi` version, OS, and whether the device is online.
The report feeds the Devices section in App Home and the weekly report.
Clients also send the activity events listed under
[What users are doing](#what-users-are-doing), for managed instances only.
Reports never include environment variables, files, command arguments,
Colab or own-key instances, or anything from other tools.

## Policy

Policy is one JSON file on the server, `policy.json`, that admins edit with
`hi server policy edit`. `hi` checks it before saving and rejects unknown
fields, so a typo can't silently turn a limit off. The server reads it on
every request, so changes apply without a restart. `/hi budget` covers the
change made most often, so admins rarely need the server box for day-to-day
changes. With no policy file, every start needs a person and nothing else is
limited.

```json
{
  "groups": {
    "staff": {
      "max_hours": 8,
      "auto_approve": { "max_price_per_hour": 1.00, "max_hours": 2 },
      "user_monthly_budget_usd": 100,
      "group_monthly_budget_usd": 300
    },
    "students": {
      "max_hours": 4,
      "hardware": ["l4", "rtx-4090", "rtx-a5000", "a40"],
      "user_monthly_budget_usd": 25,
      "template_sources": []
    }
  },
  "reports": { "daily": true, "weekly": true, "monthly": true }
}
```

- `max_hours` and `hardware` are hard limits: a request outside them is
  refused with the reason.
- **Budgets warn and never block.** A request from a user or group that is
  over budget shows ⚠️ with the spend in Slack and warns the user in the
  terminal, and it always goes to a person, even within auto-approve limits.
  The channel gets one alert per user or group per month when a budget is
  crossed. The user also sees the spend in `hi connect status` and
  `hi compute ls`.
- `template_sources` lists the template sources a group's devices may see
  and use. An empty list hides them all; a missing field allows every source.
- `auto_approve` approves a start or extension within its price and total
  hours, when the user and group are within budget. The message says
  `approved by policy (staff: up to $1.00/h and 2h)` and keeps its Stop
  button.

## Security

- Provider keys stay on the server and are never put on an instance, where
  users have shell access. The server also skips today's RunPod on-pod
  watchdog, which uses a key RunPod scopes to that one pod, because installing
  it needs SSH access to the pod and the server has none. The reconciler stops
  instances instead. Where the provider has a native lifetime limit (Hugging
  Face job timeouts), the server sets it too.
- Each device signs every request with its ed25519 key, including a timestamp
  and a nonce, so a request can't be replayed. The VPN is a second layer, not
  the only one.
- Slack is the control surface, so Slack account security matters. The
  workspace should require two-factor sign-in or SSO for approvers. Every
  Slack action is checked against the approvers list on the server. Slack
  cannot change provider keys, the approvers list, or policy beyond budgets
  and group membership.
- If the server is down, managed providers cannot start compute: the managed
  driver refuses to start rather than falling back to the user's own key.
  Colab and own-key providers are unaffected.
  Instances that are already running keep their provider-side limits and are
  reconciled when the server returns.
- If Slack is down, requests wait. Anything auto-approved still starts, and
  the reconciler keeps enforcing limits. Admins can approve and stop from the
  server box with `hi server approve` and `hi server stop`.
- Template source tokens stay in `keys.json` on the server, like provider
  keys. Devices receive templates, never repository access, and every
  template bundle is signed with the server key that the device stored at
  `hi connect`.
- The server does not control personal provider accounts, and doesn't try
  to. Someone with their own RunPod account can still use it on a device that
  has not joined the server. The server governs the organization's accounts
  and money. See [Managed and unmanaged](#managed-and-unmanaged).

## Commands

Client:

```text
hi connect <server>          enroll this device and wait for approval
hi connect status            server, user, group, budget left
hi disconnect                forget the server and delete the device key
hi compute requests          pending and recent requests
hi compute extend <name> <d> ask for more time
hi compute live              live view of your own instances and budget
```

Server box (setup, and a fallback for when Slack is unavailable):

```text
hi server init | run
hi server provider add|remove|list <provider>
hi server slack setup
hi server approvers add|remove|list <slack-user>
hi server policy edit|show
hi server user add|remove|list|move <user> [--group g] [--key k]
hi server approve|deny <request>
hi server stop <name> | --user <u> | --all
hi server audit [--since 7d]
hi server viewer add|remove|list <name> [--key k]
hi server wall setup [--session s] [--size WxH]
hi server wall add|remove|list <screen> [<ssh-pubkey>] [--session s]
hi server templates add <name> <git-url> [--ref <ref>]
hi server templates remove|list|sync [<name>]
hi server templates rename <name> <new-name>
hi server data [add <org>... | list | test | remove <org>]
hi server expose [stop]
```

The team's Hugging Face data (`hi server data`, `hi data`) is specified in
[hi_data.md](hi_data.md).

Approver devices and wall displays:

```text
hi server live               interactive dashboard for approvers
hi server live --wall        read-only full-screen dashboard
    [--names initials|full] [--reasons]
```

Slack: the `/hi` commands, App Home, and channel buttons above.

## Phasing

1. **Brokered RunPod with CLI approval.** Server, device enrollment, the
   managed driver, leases, the reconciler, and `hi server approve` and `stop`.
   It also covers the agent-ready request flow: `--reason`, `--no-wait`,
   `hi compute requests --wait`, the pending and denied exit statuses, and
   the rule that nobody approves their own request. This alone takes keys off
   laptops.
2. **Slack approvals and control.** Socket Mode app and manifest, enrollment
   and compute approval buttons, stop buttons, message states, threaded
   alerts, and `/hi status`, `/hi stop`.
3. **Policy, budgets, and reports.** Groups, auto-approve rules, monthly
   budgets, `extend`, `/hi spend`, `/hi users`, `/hi budget`, `/hi audit`,
   and the daily, weekly, and monthly reports.
4. **Live dashboard.** The event stream, client activity events, `hi server
   live` in both modes, viewer devices, the tmux wall session with
   `hi server wall`, and `hi compute live`. Provider GPU
   metrics come last, provider by provider.
5. **App Home dashboard.** Now, Waiting, This month, and Devices, plus the
   per-user view and direct messages for linked users.
6. **Template sources.** The server key and signed answers, `hi server
   templates`, the two template calls, `template_sources` in policy, and
   server templates in `hi init`.
7. **More providers.** Hugging Face Jobs, then the providers on the roadmap.
   Colab is never managed (see
   [Managed and unmanaged](#managed-and-unmanaged)).

Every phase keeps the never-connected behaviour unchanged, and the existing
`hi compute` tests keep passing with no server.

## Open questions

1. Hugging Face SSH (`ssh <job>@ssh.hf.jobs`) checks keys registered on the
   Hub account that owns the job. Can the server register a user's key on the
   organization's account for the job's lifetime, or does brokered HF need to
   be run-only?
2. Whether any instance can end itself safely without an organization key,
   for example with an on-box `shutdown` timer plus provider auto-terminate on
   power-off, as a backstop when the server is down.
3. HTTPS for the client API inside the VPN: use an internal certificate
   authority, or rely on WireGuard encryption and signed requests. With no web
   page, only `hi` clients connect, so signed requests over WireGuard may be
   enough. Template bundles are signed by the server either way (see
   [Why the server signs](#why-the-server-signs)).
4. Whether students' reasons and usage should be visible to approvers only, or
   to the student's supervisor as well, for example by a direct message to the
   supervisor.
5. Which providers expose GPU utilization through their API, and how often.
   RunPod's GraphQL API appears to report per-pod GPU utilization; Hugging
   Face Jobs and the roadmap providers are unchecked. Where no API has it,
   the choice is between leaving the idle flag out and having the managed
   client report it from `nvidia-smi` over the user's own SSH session.
6. Whether the wall display should show spend at all, or only what is
   running, in rooms that students or visitors pass through.
7. Whether a connected user may still use their own key for a provider the
   server offers, for example with `--own`. The draft says no, so everything
   on the organization's providers is approved and counted. The user can
   always `hi disconnect`.
8. Is one server per organization enough, or do departments need separate
   channels and budgets on the same server? The draft assumes groups are
   enough.

## Acceptance criteria

1. No provider key is ever written to a client device, an instance, or Slack.
2. An unenrolled or removed device gets a refusal on every server call.
3. A paid start that no policy rule auto-approves starts only after a person
   approves it, and the request records who approved it.
4. Every instance started through the server stops within 60 seconds of its
   max lifetime or cost cap, even when the user's laptop is off.
5. Stop by instance, by user, and for everything each work from Slack and from
   the server box CLI, and each is logged and posted in the thread.
6. A Slack action by someone not on the approvers list is refused and logged,
   even if they are in the channel.
7. An instance with no lease on an organization account is reported in Slack
   within 60 seconds.
8. The daily report arrives every day and matches the audit log's spend.
9. With the server unreachable, a managed `hi compute up` fails with a clear
   message and starts nothing.
10. `hi compute ls`, `ssh`, `tunnel`, `logs`, and `stop` behave the same for a
    managed user as for a user with their own key.
11. `hi server live` shows a start, stop, or approval within 2 seconds of it
    happening.
12. `hi server live --wall` accepts no action but `q`, hides reasons and full
    names by default, and runs on a viewer device that can do nothing else.
13. Approving, denying, or stopping from the interactive dashboard is checked,
    logged, and posted in Slack exactly like the Slack buttons.
14. Client activity reports contain command names and instance names only,
    never arguments or anything typed in a session.
15. A screen key added with `hi server wall add` gets only the read-only tmux
    attach. Asking for a shell or another command still gives only that
    attach, and port forwards are refused. The `hi-wall` account cannot read the server's state or provider
    keys.
16. The wall tmux session comes back on its own after a crash or a reboot of
    the server box.
17. A `hi` that has never run `hi connect` behaves exactly like the current
    release: own keys, direct provider calls, and no network traffic to any
    management server.
18. Colab never goes through the server, and nothing about Colab runtimes or
    own-key instances is sent to it, whether or not the device is connected.
19. After `hi disconnect`, the device behaves as if it had never connected.
20. A source's token is never sent to a device, Slack, or the audit log.
21. A new commit that fails the source checks does not replace the current
    commit, and the channel is alerted.
22. A device whose group's `template_sources` excludes a source neither sees
    it in the catalog nor can download it.
23. A device refuses a template bundle not signed by the server key it stored
    at `hi connect`.

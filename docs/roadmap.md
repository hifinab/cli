# Roadmap

Implementation order for `hi`. Completed releases use `[x]`; planned work uses
`[ ]`. Detailed behavior and acceptance criteria live in `docs/specs/`.

Only work with an accepted specification in `specs/approved/` is listed below.
Drafts in `specs/ideas/` remain outside the roadmap until approved.

## Released

### v0.1.0 — Initial workstation support

- [x] Install the CLI from checksummed amd64 and arm64 release assets.
- [x] Configure and verify an Ubuntu Strix Halo workstation.
- [x] Create users with render and video access.

### v0.2.0 — General workstation installation

- [x] Install the non-hardware workstation software through `hi install`.

### v0.3.0 — NetBird enrollment

- [x] Connect a machine with `hi net <setup-key>`.

### v0.4.0 — NetBird device naming

- [x] Prompt for the NetBird device hostname and show the machine hostname as
  the default.

### v0.5.0 — NetBird security and lifecycle

- [x] Secure NetBird enrollment and add lifecycle commands.
  Approved spec: [hi_net.md](specs/approved/hi_net.md).

### v0.5.1 — AI coding tools

- [x] Install Claude Code, Codex CLI, and herdr, and verify their commands.

### v0.5.2 — Unattended installation

- [x] Install Codex without its interactive launch prompt.
- [x] Run `hi` straight from the installer with `sh -s -- <command>`.

### v0.6.0 — Colab compute

- [x] Start, list, inspect, reach, and stop Colab instances with `hi compute`:
  `up`, `ls`, `status`, `ssh`, `tunnel`, `logs`, `stop`, `hardware`, and
  `providers`, plus a guided menu for bare `hi compute`.
- [x] Enforce a maximum lifetime with a detached watcher and at every
  `hi compute ls`; confirm paid hardware; validate hardware names before
  calling Colab.
- [x] Run Python scripts to completion with `hi compute run`.
- [x] Serve a GGUF model with llama.cpp and tunnel its OpenAI-compatible API
  with `hi compute serve`, including the tested Qwen3.8-Flash-Next recipe.
- [x] Install the Colab CLI with `hi install`.
- [x] Verify against a live Colab account: CPU `up`, `ssh`, `tunnel`, `run`,
  and `stop`, and the Qwen3.8-Flash-Next recipe on a G4 (about 7 minutes to
  ready, ~84 tokens/s, 0.93 units for the whole test).
  Approved spec: [hi_compute.md](specs/approved/hi_compute.md).

Dependency: the Colab CLI (`google-colab-cli`) and a Colab Pro or Pro+ plan,
which SSH requires.

### v0.7.0 — Hugging Face compute

- [x] Install the `hf` CLI and add `hi login hf` (and `hi login colab`).
- [x] Run, list, follow, wait for, and stop Hugging Face Jobs with
  `hi compute run`, calling the Jobs REST API directly: Python scripts or
  container images, secrets, `--detach`, and `--namespace`.
- [x] Start and reach Hugging Face instances with `hi compute up`, `ssh`,
  `tunnel`, and `logs`; serve models with `hi compute serve`.
- [x] Infer the provider from the hardware name when both are signed in.
- [x] Verify against a live account (the `hifinab` organization): runs,
  detached runs, `up`/`stop`, and `serve` on a `t4-small` (ready in about a
  minute, ~220 tokens/s for Qwen3-0.6B), for under $0.05. SSH through the Jobs
  gateway still needs a registered key to test.
  Approved spec: [hi_compute.md](specs/approved/hi_compute.md).

Dependencies: the v0.6.0 driver interface, state, and SSH layer; the
token-read exception in [hi_login.md](specs/approved/hi_login.md).

### v0.7.1 — Agent skill, billing, guide, and self-update

- [x] Write a `hi` agent skill for Claude Code and Codex with `hi skill`,
  into the current folder or, with `--global`, the home folder; one copy in
  `.agents/skills`, linked from `.claude/skills`.
  Approved spec: [hi_skill.md](specs/approved/hi_skill.md).
- [x] Choose who pays for Hugging Face Jobs automatically, and show or save
  it with `hi compute billing`.
- [x] Close tunnels whose instance was stopped even when the SSH gateway
  stays up; verify Hugging Face SSH and tunnels live.
- [x] Publish a searchable, multi-page guide at https://hifin.sh/guide/ with
  step-by-step setup, examples, and a command reference.
- [x] Add atomic, checksummed self-updates with `hi update`.
  Approved spec: [hi_update.md](specs/approved/hi_update.md).
- [x] Accept bare hours for `--max`, and `--max none` for no limit after a
  confirmed warning.

### v0.7.2 — Styled compute menu

- [x] Give `hi compute`'s guided menu a styled terminal UI: arrow-key menus,
  filtering, validated inputs, a record of each answer, and a cost summary
  that defaults to No before anything starts.

### v0.7.3 — Update without the GitHub API

- [x] Find the latest release from GitHub's `/releases/latest` redirect, so
  `hi update` works when the API's hourly limit for the network is used up.

### v0.7.4 — Never downgrade

- [x] A bare `hi update` only installs a newer release, even while GitHub's
  latest-release redirect still points at the previous one.

### v0.8.0 — RunPod compute

- [x] Add a RunPod driver through its REST API v2 (v1 retires on
  2026-11-15): hardware with prices, `up`, `ls`, `status`, `ssh`, `tunnel`,
  `logs`, and `stop` (which terminates).
- [x] Enforce `--max` with a watchdog on the pod as well as the local watcher.
- [x] Add `hi login runpod`.
- [x] Ask for the API key in `hi login runpod` and in the menu, and save it
  in runpodctl's `~/.runpod/config.toml`.
- [x] Show which GPUs are free and suggest free alternatives when a start
  fails.
- [x] Verify against a live RunPod account: SSH, tunnel, and a pod that
  terminated itself at `--max` with no local watcher.
- [ ] Runs to completion on RunPod.
  Approved spec: [hi_compute.md](specs/approved/hi_compute.md).

Dependency: extends the v0.6.0 and v0.7.0 driver interface.

### v0.8.1 — Pick what to stop

- [x] A bare `hi compute stop` lists what is running and stops the chosen
  instance, or all of them, after confirming.

### v0.8.2 — Steady menus

- [x] Menu lists stay still while the cursor moves; long lists scroll only
  at the edge.

### v0.9.0 — Managed compute: server and brokered RunPod

- [x] Add `hi server` on a dedicated box in the VPN. It holds the provider
  keys, makes every provider call for connected devices, and keeps state in
  a JSON file with an audit log.
- [x] Add `hi connect` and `hi disconnect`, with an ed25519 device key that
  signs every request and is pre-approved by an admin.
- [x] Add a managed driver, so that connected devices start, list, reach,
  and stop RunPod through the server with the same `hi compute` commands.
- [x] Record a lease for every start. A reconciler stops instances at their
  `--max` and reports instances that have no lease.
- [x] Approve, deny, and stop from the server box with `hi server approve`,
  `deny`, and `stop`.
- [x] Make the request flow work for agents: `--reason`, `--no-wait`,
  `hi compute requests --wait`, exit statuses for pending and denied, and
  nobody approves their own request.
- [x] Keep a never-connected `hi` exactly as it is today. Colab is never
  managed.
- [x] Document it in the guide and teach it to agents in `hi skill`.
- [x] Verify with a live RunPod account over NetBird: enroll, approve, SSH,
  tunnel, stop, and a pod stopped by the server at `--max` (2026-09-29,
  aiw12 serving aiw9).
  Approved spec: [hi_server.md](specs/approved/hi_server.md).

### v0.9.1 — Visible key entry

- [x] Key and token prompts (`hi server provider add`, `hi login runpod`,
  `hi net`) show a `*` per character, so a paste is visibly received.
- [x] `hi server provider add` works before the server is started, and checks
  the provider name before asking for the key.

### v0.9.2 — Approvals survive sold-out hardware

- [x] When approved hardware is sold out, the server starts the cheapest free
  hardware with at least as much memory at up to twice the approved price
  (`fallback_price_factor` in the server's `config.json`), instead of failing
  and needing a second approval.
- [x] Audit entries stay on one line, and stops read `ran 2m, started by
  iman`.

### v0.10.0 — Managed compute: Slack

- [x] Add a Slack app over Socket Mode, with a manifest printed by
  `hi server slack manifest` and tokens stored by `hi server slack setup`.
  It posts enrollment and compute approval buttons, stop buttons, message
  states, and threaded alerts, and answers `/hi status` and `/hi stop`.
- [x] Add `hi server approvers`, which maps Slack members to `hi` users, so
  nobody approves their own request from Slack either.
- [x] Verify with a live Slack workspace (2026-09-29): approve from a
  button, a sold-out L4 replaced by an RTX A5000, and a Stop click.
  Approved spec: [hi_server.md](specs/approved/hi_server.md).

### v0.10.1 — Clearer Slack messages

- [x] A request that fails because hardware is sold out lists what is free
  now with as much memory, and its price.
- [x] Final Slack messages keep who approved them, and costs under a cent
  read "under $0.01".

### v0.10.2 — Friendlier /hi

- [x] `/hi stop @alice` and `/hi stop user @alice` accept Slack mentions, and
  `/hi stop <user>` stops that user's instances when no instance has the name.
- [x] After a stop from a private `/hi` answer, the answer says what was
  stopped instead of keeping a live button.
- [x] `/hi status` says "Nothing is waiting for a decision" instead of "0
  waiting".

## Planned

### v0.11.0 — Managed compute: policy, budgets, and reports

- [ ] Add groups, auto-approve rules, monthly budgets, and
  `hi compute extend`.
- [ ] Answer `/hi spend`, `/hi users`, `/hi budget`, and `/hi audit`, and
  post daily, weekly, and monthly reports.
- [ ] Label agents in requests, and add `hi connect --agent` for agents that
  run on their own.
  Approved spec: [hi_server.md](specs/approved/hi_server.md).

### v0.12.0 — Managed compute: live dashboard

- [ ] Add `hi server live` for approvers and `--wall` for shared screens.
- [ ] Run the wall in a tmux session that screens attach to read-only over
  SSH, set up by `hi server wall`.
- [ ] Add `hi compute live` for users, client activity events, and provider
  GPU metrics where the provider has them.
  Approved spec: [hi_server.md](specs/approved/hi_server.md).

### v0.13.0 — Managed compute: Slack App Home

- [ ] Add an App Home dashboard with Now, Waiting, This month, and Devices,
  plus a per-user view and direct messages for linked users.
  Approved spec: [hi_server.md](specs/approved/hi_server.md).

Managed Hugging Face Jobs and the providers under "More compute providers"
follow provider by provider.

### v0.14.0 — Authentication

- [ ] Delegate GitHub and OMP authentication to their native tools.
  Approved spec: [hi_login.md](specs/approved/hi_login.md).

Dependency: login requires the corresponding installed CLI.

### v0.15.0 — Project bootstrap

- [ ] Add the interactive `hi init` helper and deterministic Python template.
  Approved spec: [hi_init.md](specs/approved/hi_init.md).

Dependency: the Python template requires `uv` and establishes the metadata and
safe-generation contract used by later templates.

### v0.16.0 — Specialized project templates

- [ ] Add the quantitative-research template.
  Approved spec: [hi_init.md](specs/approved/hi_init.md).
- [ ] Add the deterministic web-application template.
  Approved spec: [hi_init.md](specs/approved/hi_init.md).

Dependency: both templates extend the v0.15.0 planner, conflict detection,
metadata, agent instructions, and documentation structure.

### v0.17.0 — Focused machine operations

- [ ] Read and change the machine hostname independently of installation.
  Approved spec: [hi_hostname.md](specs/approved/hi_hostname.md).
- [ ] Inspect users and login history; manage accounts, groups, and sudo access.
  Approved spec: [hi_user.md](specs/approved/hi_user.md).
- [ ] Install `hi` and the AI and developer tools for all users, including
  accounts created later. Review the caveats before starting.
  Approved spec: [hi_install_shared.md](specs/approved/hi_install_shared.md).

Dependency: shared tools no longer update themselves, so they rely on
rerunning `hi install` or on `hi update` (v0.7.1) to stay current. A shared
`hi` also changes how `hi update` replaces the binary.

### v0.18.0 — Guided installer

- [ ] Replace the fixed `hi install` run with a guided menu of checkboxes
  that selects which tools to install. Unchecking an installed tool
  uninstalls it, so the same menu serves as both installer and uninstaller.
  Spec not yet written.

Dependency: needs uninstall steps for every tool `hi install` manages, and
must respect the per-user or all-users choice from v0.17.0.

### Deferred — More compute providers

On-demand providers to add to `hi compute`, in priority order. Ranked
2026-09-29 on three criteria in order: an easy API, a low on-demand price,
and coverage across many countries. Prices are USD per GPU-hour for a single
GPU, checked 2026-09-28. Spot pricing played no part in the ranking. Specs
not yet written.

- [ ] RunPod Community Cloud as an opt-in on the existing driver: H100 SXM
  $2.69 against Secure's $3.49, and RTX 4090 $0.34 against $0.74. It is still
  on-demand, but it runs on third-party hosts, so Secure stays the default.
- [ ] Shadeform. One REST driver reaches 19 datacenter clouds (Lambda, Verda,
  Hyperstack, Scaleway, Latitude, Massed Compute, Crusoe, Nebius, and more)
  in about 16 countries, mostly in the US. Its price and stock catalog needs
  no key. Cheapest today: H100 $2.73, A100 80GB $1.35, L40S $0.88. Compare
  its prices with going direct before relying on it, because one third-party
  source claims a 7–12% markup.
- [ ] Vast.ai. The cheapest option (H100 $1.87–2.66, A100 $0.74, RTX 4090
  $0.48) and the widest coverage, with hosts in 64 countries on six
  continents. REST with an API key, and search needs no account. Restrict it
  to verified datacenter hosts with high reliability scores, because a host
  going offline interrupts a job as surely as an eviction does. SSH goes
  through a mapped port, not port 22.
- [ ] Latitude.sh. REST with a Bearer key. Its plans endpoint returns price
  and stock per location. It has 25 locations across North America, Latin
  America, Europe, and Asia-Pacific. H100 is about $1.66 and L40S about $0.74
  (both unverified). First confirm which locations actually have GPUs.
- [ ] Prime Intellect. An aggregator with a REST API and a stock endpoint
  that includes country. H100 costs $2.43.
- [ ] TensorDock. REST v2, H100 from $2.25, and a claimed 100+ locations in
  20+ countries. Its API docs were partly unreachable.
- [ ] Novita AI. REST, with container pods reached over SSH like RunPod's.
  About 12 countries on five continents, and its on-demand prices are
  unverified.
- [ ] Verda. Finland only, but the only self-serve single B200 in the EU
  ($6.85). H100 $3.56. It has well-documented price and stock endpoints and
  is also reachable through Shadeform.
- [ ] Lambda. The simplest REST API, with 12 regions in the US, Japan, India,
  Germany, and Israel. H100 costs $3.29 PCIe or $4.29 SXM, and single H100s
  often sell out. It is also reachable through Shadeform.
- [ ] Hyperstack. REST with an API key and a Go SDK, in Canada, Norway, and
  the US. H100 PCIe is about $2.50, worked back from its spot price. It is
  also reachable through Shadeform.
- [ ] Scaleway. REST with a Go SDK and a catalog that needs no login, but EU
  only. H100 costs €2.87 and L4 €0.79. It is also reachable through
  Shadeform.

Build a direct driver for any provider reachable through Shadeform only if
going direct is clearly cheaper or more reliable. Left out:
- Nebius. H100 rises to $4.50 on 2026-10-01, and its API is gRPC.
- Spheron. Its on-demand price is high: H100 SXM costs $4.43.
- AWS, Azure, and Google Cloud. They cost 2–5 times as much on demand, and
  new accounts start with zero GPU quota.
- DigitalOcean. H100 costs $4.41, and new accounts start with zero GPU quota.
- Sales-led clouds: CoreWeave, Fluidstack, Nscale, TensorWave, and Cirrascale.
- CUDO Compute, which closed its self-serve platform on 2026-03-31.
- Container platforms without SSH: Salad, Koyeb, and Northflank.
- Model-inference APIs, which don't rent general compute: Together,
  Fireworks, Groq, and Replicate.

Dependency: each driver implements the existing `computeProvider` interface.
Supporting a host and port pair for SSH (Vast.ai, Novita) generalizes the
RunPod code.

### Deferred — Spot instances

- [ ] Add `--spot` to `hi compute up` and `hi compute run` to rent
  interruptible machines at a discount, keeping on-demand as the default.
  Show how much notice the provider gives before eviction when confirming,
  and show evicted instances as evicted, not stopped, in `hi compute ls` and
  `status`. Spec not yet written.

Dependency: needs a provider that sells spot. Colab, Hugging Face Jobs, and
RunPod do not (RunPod stopped selling spot pods in September 2026), so this
waits for a spot-capable driver such as Vast.ai, Verda, or Novita AI. Not
scheduled to a version yet.

### Deferred — Modal compute

- [ ] Add a Modal driver through Modal's official Go SDK.
  Approved spec: [hi_compute.md](specs/approved/hi_compute.md).

Waiting until Modal's Go SDK leaves beta and its package path settles. Not
scheduled to a version yet.

The TUI, compute files, and templates in the same spec are still marked draft;
v0.6.0 ships a numbered menu instead of the full TUI.

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

### v0.10.3 — Slack shows user text as typed

- [x] Requesters' reasons, hostnames, and provider errors show in Slack
  exactly as typed: a reason can no longer mention people, ping `@channel`,
  or fake a link.
- [x] `/hi status` lists instances that are still starting, and `/hi stop`
  explains when the one you asked for hasn't finished starting.

### v0.10.4 — Say how long starting takes

- [x] After an approval, the message says starting takes 1–3 minutes and
  turns 🟢 when ready, shows the provider's progress, and the thread notes
  the approval at once.

### v0.10.5 — Running times that stay true

- [x] A running instance's Slack message shows "running since 12:56, stops
  at 13:06" instead of a running time that goes stale.

### v0.10.6 — More room to replace cheap hardware

- [x] A replacement for sold-out hardware may cost up to twice the approved
  price or the approved price plus $1.00/h, whichever is higher
  (`fallback_price_factor`, `fallback_price_extra`), so cheap GPUs find a
  replacement while expensive ones stay near 2×.

### v0.11.0 — Managed compute: policy, budgets, and reports

- [x] Add `policy.json` with `hi server policy show|edit|example|check`:
  per group `max_hours`, allowed `hardware`, `auto_approve`, and monthly
  budgets per user and per group. It applies on the next request.
- [x] Budgets warn and never block: over-budget requests show ⚠️ and the
  spend, always wait for a person, and the channel gets one alert a month;
  users see the spend in `hi connect status` and `hi compute ls`.
- [x] Add `hi compute extend`, approved like a start.
- [x] Answer `/hi spend`, `/hi users`, `/hi budget`, and `/hi audit`, add
  `hi server spend`, and post daily, weekly, and monthly reports.
- [x] Label requests from agents (`iman via Claude Code`), and add
  `hi connect --agent <name> --owner <user>` for agents that run on their
  own.
- [x] Verify live with a policy on aiw12 (2026-09-29): a staff start
  approved by policy, then stopped by the server at its limit.
  Approved spec: [hi_server.md](specs/approved/hi_server.md).

### v0.11.1 — Say who approved once

- [x] Waiting on a request prints "Approved by …" once, then the start's
  progress, instead of repeating the approver on every line.

### v0.12.0 — Shadeform

- [x] Add a Shadeform driver through its REST API (`X-API-KEY`): one
  integration reaches GPUs from many clouds (Hyperstack, Massed Compute,
  Lambda, Scaleway, Paperspace, Vultr, and more). Each GPU type is listed
  once at its cheapest free on-demand offer, with the cloud and region;
  `h100@lambdalabs` picks a cloud.
- [x] Register the user's SSH key with Shadeform once, set Shadeform's own
  `auto_delete` at `--max` so machines stop even with the laptop off, and
  delete on `stop`.
- [x] Add `hi login shadeform`, and let `hi server provider add shadeform`
  manage it, including replacements for sold-out offers from other clouds.
- [x] Record an instance's start when it is created, not when it is ready.
- [x] Verify live: an A4000 on Hyperstack in Oslo, booted in 3.5 minutes,
  SSH as `shadeform`.

### v0.12.1 — One row per managed instance

- [x] With two managed providers, `hi compute ls` listed each managed
  instance under both; each provider now lists only its own.
- [x] Verify managed Shadeform live: a staff start approved by policy on
  aiw12, SSH to the machine, and a stop.

### v0.12.2 — RunPod Community Cloud

- [x] Add RunPod Community Cloud as an opt-in: `--gpu rtx-4090@community`
  (25–54% cheaper than Secure Cloud, still on-demand), listed with
  `hi compute hardware --on runpod --community`.
- [x] Warn that no API tokens, passwords, SSH private keys, or sensitive data
  may ever be put on a Community machine: before every start (even with
  `--yes`), on every `ssh`, `tunnel`, `logs`, and `serve`, on the Slack
  request, and in the agent skill.
- [x] Never move an approved Secure Cloud start onto Community Cloud when
  hardware is sold out; Community starts are replaced only on Community.

### v0.13.0 — Managed compute: live dashboard

- [x] Add `hi server live` for admins on the server box: running machines
  with cost and a bar towards each limit, waiting requests, budgets, and
  activity, updated every second; `a`, `d`, and `s` approve, deny, and stop.
- [x] Add `--wall`: read-only, initials and no reasons by default, fills the
  screen and rotates what doesn't fit, dims while reconnecting.
- [x] Run the wall in a tmux session as a separate `hi-wall` account that
  screens attach to read-only over SSH, set up by `sudo hi server wall`.
- [x] Add viewer devices (`hi server viewer add`), which may only watch, and
  `hi compute live` for users' own machines.
- [x] Report client activity (`ssh`, `tunnel`, `logs`, `serve`) to the
  dashboard, with command and instance names only.
- [ ] Provider GPU metrics, such as an idle GPU flag. Deferred with the
  providers work.
- [x] Verify the wall live on aiw12 (2026-09-29): a screen attached over
  SSH saw the dashboard; a shell command and a port forward were refused.
  Approved spec: [hi_server.md](specs/approved/hi_server.md).

### v0.13.1 — A cleaner wall

- [x] On a wall, only people's names become initials: "stopped at its time
  limit" no longer reads "time L.".
- [x] The wall's tmux session hides the status bar.

### v0.14.0 — Managed compute: Slack App Home

- [x] Add an App Home dashboard: approvers see Now (with Stop and Stop
  all), Waiting (with Approve and Deny), This month, and Devices with their
  `hi` version, flagged when outdated or silent.
- [x] Let people link their Slack account with `/hi link <user>` and
  `hi connect slack <code>` from their own device; linked users see their
  own machines in App Home and can stop them.
- [x] Send linked users direct messages when their requests are approved,
  denied (with the reason), started, near their limit, or stopped.
- [x] Verify live after updating the Slack app's manifest (2026-09-29):
  Home tab, `/hi link`, and messages about a request.
  Approved spec: [hi_server.md](specs/approved/hi_server.md).

### v0.14.1 — Home says starting

- [x] The Home tab shows an approved request that is still starting as
  "approved, starting", not as waiting.

### v0.15.0 — Guided installer

- [x] Replace the fixed `hi install` run with a menu of checkboxes, each with
  a short description, that selects which tools to install. Unchecking an
  installed tool uninstalls it, so the same menu serves as both installer and
  uninstaller.
- [x] Install and remove named tools without a terminal with
  `hi install <tool>...`, `hi install --all`, and `hi uninstall <tool>...`.

### v0.16.0 — Project bootstrap

- [x] Add built-in templates under `templates/`, embedded in `hi`: a hidden
  `base` layer (agent instructions, `make check`, CI, secret scanning), and
  the well-known `python` (uv, ruff, pyrefly, pytest) and `web` (React and
  TypeScript on Vite, npm, Biome, Vitest) templates.
- [x] Add `hi init`, guided and direct, with `--list`, `--dry-run`, `--yes`,
  `--no-setup`, and `--github`, recording what it generated in
  `.hifin/template.json`.
- [x] Read a local source from `HI_TEMPLATES_DIR` for template authors.
- [x] CI generates every built-in template, runs its `make check`, and
  rejects internal names under `templates/`.
  Approved spec: [hi_init.md](specs/approved/hi_init.md).

Dependency: `uv` for `python`, Node.js with npm for `web`. Nothing
private or quant-related goes in the built-in templates.

### v0.17.0 — Private templates through `hi server`

- [x] Give the server an ed25519 key, store it on devices at `hi connect`,
  and sign template bundles with it.
- [x] Add `hi server templates add|remove|list|sync`: mirror a private
  repository with a read-only token, check every commit, fetch every 15
  minutes, and log and alert on changes.
- [x] Serve the catalog and signed bundles to enrolled devices, limited per
  group by `template_sources` in policy.
- [x] List and generate server templates in `hi init`, cached per commit,
  with server layers extending built-in ones.
- [x] Move the quant work into the private `hifinab/templates`: the
  `research` template with its backtest guards, and the private skills
  `time-series-validity` and `backtest-evaluation`. Layers can `remove`
  files they inherit.
- [ ] The `data-access` skill, once data access is decided.
- [x] Verify live on vmhiserver (2026-10-01): the server signed and served
  `hifinab/templates` at 7362a3e, a laptop stored its key, and
  `hi init research` passed `make check`. The server reads GitHub with a
  fine-grained, read-only token for that one repository (the `hifinab` org
  has deploy keys turned off), and a new commit reached the laptop after
  `hi server templates sync`.
  Approved spec: [hi_server.md](specs/approved/hi_server.md#template-sources);
  approved spec: [hi_init.md](specs/approved/hi_init.md).

Dependency: the v0.16.0 layers and metadata; the open decisions
(backtest engine, data access, scheduling) are tracked in
`hifinab/templates`.

### v0.17.1 — Template syncs that can't hang

- [x] Every git command for template sources gives up after 2 minutes, or
  after 30 seconds below 1 KB/s, so a stuck fetch no longer blocks
  `hi server templates` behind it.

### v0.17.2 — Call it the private repo

- [x] `hi init`'s menu marks server templates `(private repo)` instead of
  the source's name.
- [x] Add `hi server templates rename`, which keeps a source's token and
  history; the firm's source is now called `private`.
- [x] Docs speak of private templates and the team, and the planned
  `firm-data` skill is now `data-access`.
- [x] v0.17.3: `hi init` forgets cached sources the server no longer
  offers, so a renamed source isn't listed twice when the server is
  unreachable.

### v0.18.0 — Templates that keep up

- [x] Update a repository to the current templates with `hi init --update`:
  unedited owned files and managed blocks are replaced, hand edits are
  conflicts, and changes to the project's own files go to `docs/upgrades/`
  for an agent.
- [x] Check it in CI with `--check`, which skips private layers there unless
  `--strict`.
- [x] Bring existing repositories under a template with `hi init --adopt`.
- [x] Add the built-in `service`, `ml`, and `pipeline` templates. `ml`
  takes PyTorch from a `cpu`, `cuda`, or `rocm` dependency group; `rocm` is
  AMD's gfx1151 build for Strix Halo.
- [x] `hi init` says when a newer `hi` brings newer built-in templates.
- [x] Train with the `rocm` group on a Strix Halo machine (v0.18.2,
  aiw11, 2026-10-01): `hi init ml` installed AMD's PyTorch 2.11 for gfx1151
  through `make sync`, `make check` passed, and `make train` ran on the
  Radeon 8060S.
  Approved spec: [hi_init.md](specs/approved/hi_init.md).

The `mobile` and `cli` templates are built when the first real project of
each type starts.

### v0.18.1 — Follow renamed sources

- [x] `hi init --update` follows a private source renamed with
  `hi server templates rename`: when the recorded source is gone and one
  current source has the same layers, it uses that one and records the new
  name.

### v0.18.2 — ml picks the PyTorch build

- [x] The `ml` template's `Makefile` picks `rocm` on a Strix Halo, `cuda`
  with an NVIDIA GPU, and `cpu` otherwise, and runs everything with that
  build; before, any `uv run` swapped the ROCm build back to the CPU one.
  `hi init ml` sets it up with `make sync`.

### v0.19.0 — `hi q`: a command from plain words

- [x] `hi q <prompt>` sends the prompt with a small, redacted context (OS,
  shell, folder listing, git state, recent history, piped input) and shows
  one proposed command to run, copy, explain, or cancel.
- [x] hi classifies each command itself (read-only, changes files,
  dangerous) by parsing it; dangerous commands need `yes` typed, and globs
  in `mv`, `cp`, and `rm` show what they match first.
- [x] `--print` for scripts, `--explain '<command>'`, `--no-context`, and
  `hi q --context` to see exactly what is sent.
- [x] Providers: OpenAI-compatible endpoints, the Anthropic API, and a
  signed-in Claude Code, found from flags, saved config, or the
  environment; otherwise a first-run menu (`hi q --setup`). `hi q --status`
  names the one in use. Claude Code was tested live with Haiku (about 6
  seconds an answer); the OpenAI-compatible and Anthropic clients only
  against fake servers so far.
- [x] A local log of commands that ran.
  Approved spec: [hi_q.md](specs/approved/hi_q.md).

### v0.19.1 — Words are always the question

- [x] `hi q`'s own actions are options (`--setup`, `--status`,
  `--context`), so `hi q status of the log file` asks the model instead of
  showing the status. Everything from the first word that doesn't start with
  a dash is the question; `--` ends the options.

### v0.19.2 — OpenRouter for hi q

- [x] OpenRouter in `hi q --setup` and from `OPENROUTER_API_KEY`: only
  models that call tools are listed, all of them (the list was cut at 300),
  with fast, cheap ones first.
- [x] Models that reject tools are asked for JSON instead.

### v0.20.0 — `hi q`: shell integration, tools, and chat

- [x] `hi shell-init bash|zsh`, added to the rc file by `hi q --setup` after
  asking: the shell's current history and last exit status, `hi q …` and
  `q …` lines quoted on Enter so `( ) * ?` need no quotes, commands that ran
  in the shell's history, `cd` and `export` run in the shell itself, and in
  zsh `e` puts the command on the prompt. Tested in bash 5.3 and zsh 5.9.
- [x] The last 100 lines of the tmux pane as context.
- [x] Tools: list, read, help, which, and read-only runs, so the model looks
  before it proposes; a failed command goes back to the model for a fix,
  up to three times.
- [x] `hi q` with no question opens a chat; `hi q -c` continues the last
  conversation.
- [x] `e` edits a proposed command before it runs.

### v0.20.1 — Keep the saved key

- [x] `hi q --setup` for the same provider keeps the saved key when Enter
  is pressed at the key prompt, and lists the model in use first.

### v0.20.2 — Answers in terminal styles

- [x] `hi q` shows the Markdown in answers as terminal styles: bold,
  coloured inline code and code blocks without fences, headings, bullets,
  quotes, and links with their address dimmed. Piped output keeps the
  Markdown. No new dependency.

### v0.21.0 — A team model through `hi server`

- [x] `hi server ai set|off|remove` stores the team's OpenRouter key on the
  server, and connected devices and agents use it through a signed
  pass-through endpoint, with any model and the server's default first.
- [x] `hi q` uses the connected server without setup, lists it first in
  `hi q --setup`, and falls back to a personal provider when the server
  can't be used.
- [x] Usage per user, device, and model, without prompts, in `hi server ai`
  and `hi server spend`.
  Tested live against OpenRouter through a local server. The production
  server runs v0.21.0 and serves the team's OpenRouter key since 2026-10-02.
  Approved spec: [hi_server_ai.md](specs/approved/hi_server_ai.md).

### v0.22.0 — `hi q`: project notes, team spend, and a local model

- [x] AI spend from `hi server ai` in `hi server live` and the Slack App
  Home (release 2 of [hi_server_ai.md](specs/approved/hi_server_ai.md)).
- [x] Project notes in `.hifin/q.md`: how to test, where the logs are, what
  not to run. `hi q` uses them only after the user allows them once per
  file content. This repository has its own.
- [x] A local model as the team's default: `hi server ai set --url …
  --no-key` for upstreams without a key. Tested on 2026-10-02 with
  `halogen-qwen3.8-flash-next` already served on aiw11: correct tool calls
  and commands in 4–14 seconds, against about 2 seconds for Haiku through
  OpenRouter. The production server still uses OpenRouter.

### v0.22.1 — `hi update` restarts the server

- [x] `hi update` finds a `hi server run` still running the replaced binary,
  also when nothing new was downloaded, and asks to restart its systemd
  service (`sudo systemctl restart`, or `systemctl --user` for a user
  service). `--restart` and `--no-restart` for scripts.

### v0.22.2 — Podman in `hi install`

- [x] `hi install podman`: rootless Podman with `crun`, `uidmap`, and
  `passt`, and a subordinate ID range for users without one. Tested on aiw11
  on 2026-10-02: a rootless container with `--group-add keep-groups` sees
  the gfx1151 GPU through ROCm, which needs `crun` rather than Docker's
  `runc`. The first step towards `hi box`.

### v0.23.1 — Remove every box at once

- [x] `hi box rm --all` lists every box and asks once; boxes with
  uncommitted work are kept unless `--force`, branches with commits are
  kept, and without a terminal it needs `--yes`.

### v0.28.0 — `hi agent`: agents get their own command

- [x] `hi agent [claude|codex] "<task>"` runs the agent in a box on a new
  worktree and waits for one report, the same for every agent: status,
  exit status, final message, its own summary block, session ID, tokens
  where the agent gives them, and the changed files from git. Without a
  name, the first agent installed and signed in.
- [x] `--json`, `--detach`, and `hi agent wait <name>`; exit 1 when the
  agent failed. `hi agent claude|codex` without a task is an interactive
  session in a box.
- [x] `hi box` is now for any code: `hi box claude|codex` and `hi box token`
  moved to `hi agent`, and print a pointer. The `locked` preset allows
  nothing; an agent's own hosts come with the agent.
- [x] The guide's new page, the agent skill, the website, and `llms.txt`.
  Tested on 2026-10-07 on aiw9 with rootless Podman, Claude Code, and Codex.
  Spec: [hi_agent.md](specs/approved/hi_agent.md), release 1.

### v0.27.1 — The agent skill knows `hi data` and `hi net expose`

- [x] The skill's description, which decides when agents load it, now names
  the team's data (`hi data`, `--data`) and `hi net expose`, and its
  introduction points to `hi data help` and `hi net expose help`. Refresh
  installed skills with `hi skill --global`, and `hi skill` in projects.

### v0.27.0 — `hi net expose`: a local service on a temporary public address

- [x] `hi net expose <port>` publishes a service on this machine with
  NetBird's reverse proxy, through a relay on the NetBird address so
  services on `localhost` work, with a request log.
- [x] Protected by a generated password by default; `--pin`, `--groups`
  (NetBird SSO), or `--public` instead. Ends after `--max` (default 1h, at
  most 24h) or on Ctrl+C.
- [x] `--detach` and `--json` for agents; `ls` and `stop`; each exposure in
  a connected hi server's audit log and live feed; the agent skill asks the
  user first and never goes `--public` without a yes.
  Tested on aiw9 with NetBird 0.74.4.
  Spec: [hi_net_expose.md](specs/ideas/hi_net_expose.md). Later: TCP
  services and a `net_expose` group policy (release 2 of the spec).

### v0.26.0 — The team's data in cloud jobs, through `netbird expose`

- [x] The server opens a second listener with only the `hi data` proxy and
  publishes it with `netbird expose` while cloud runs need it, closing it
  when no run token is valid and nothing has called for 10 minutes.
- [x] Run tokens: for the repositories named with `--data`, until the
  run's time limit, accepted only through the public address.
- [x] `hi compute run --data` on Hugging Face Jobs uses it: the token goes
  as an encrypted job secret, there is no size limit or one-hour expiry,
  and the script can call `load_dataset` itself. Signed links remain for
  Colab and when the server can't expose. `hi server expose [stop]`.
  Tested on a real Hugging Face job through a server on vmhiserver.
  Spec: [hi_server_expose.md](specs/ideas/hi_server_expose.md).

### v0.25.2 — A clear message from an older server

- [x] `hi compute run --data` against a server older than v0.25.1 says to
  update the server, instead of "404 page not found".

### v0.25.1 — The team's data on rented GPUs

- [x] `hi compute run --data <org>/<name>[/<pattern>]` downloads the team's
  data into `data/<name>` on the instance before the script starts. The
  instance can't reach the hi server, so the server hands out a signed
  link per file (about an hour, no token) and `hi` ships them in a wrapper
  that then runs the script. Tested with real links and a 548 MB file,
  and on a real Hugging Face job with 150 private files from the team.

### v0.25.0 — `hi data` in code, projects, and boxes

- [x] `hi data run -- <command>` and `eval "$(hi data env)"`: `load_dataset`,
  `from_pretrained`, `hf_hub_download`, and `hf://` paths in pandas read
  the team's repositories directly. Tested with datasets 5.0.1 and pandas.
- [x] `hi data get` pins a branch or tag to its commit and records the
  download in the project's `.hifin/data.json`; `hi data get` with no name
  fetches everything recorded, and warns when a bucket changed since.
- [x] `hi box --data`: the box gets a placeholder token and hi's proxy adds
  a hi data token on the way to the server, and allows Hugging Face's
  download hosts. Tested with rootless Podman and a 548 MB Xet file.
- [x] The spec is approved: [hi_data.md](specs/approved/hi_data.md).

### v0.24.3 — Day-long data tokens, and `hi data` in the docs

- [x] A hi data token lasts a day instead of an hour, so long downloads
  don't stop when `hf` asks for its next Xet token; it still works only
  from its device, while the device and user are enrolled.
- [x] Laptops older than v0.24.2 don't see buckets, which they would try
  to download as models; counts say "1 bucket".
- [x] The guide (Download the team's data), README, website, llms.txt, and
  the agent skill cover `hi data`.

### v0.24.2 — Buckets in `hi data`

- [x] `hi data` lists the organizations' buckets with their size and file
  count; `hi data get` syncs one with `hf buckets sync`, and `info` lists
  its largest files. The proxy passes a bucket's reads and refuses uploads,
  deletes, and settings. Tested with a public bucket through the proxy,
  and with a private 36 GB bucket from the team's server on 2026-10-03.

### v0.24.1 — Search in `hi data`

- [x] `hi data`'s list has a search field: typing filters at once, every
  word must match (`gemma 27b`), and at most 15 rows show, with a count.
  Without a terminal it asks for words first and numbers only the matches.

### v0.24.0 — `hi data`: the team's Hugging Face datasets and models

- [x] `hi server data add <org>...` stores a read token per organization
  after checking it with the Hub; `list`, `test`, `remove`, and a menu.
- [x] A proxy under `/hf` passes only a repository's read calls on with the
  organization's token, so devices run the official `hf` with
  `HF_ENDPOINT` pointing to the server and a hi data token that lasts an
  hour. The Hugging Face token never reaches a device; file bytes come from
  the CDN and Xet storage directly.
- [x] `hi data` (a picker), `ls`, `info`, and `get`; `data` patterns per
  group in policy, everything when unset; token grants in the audit log,
  downloads in `data_usage.jsonl`.
  Tested on 2026-10-03 with `hf` 1.32.0 against the real Hub.
  Spec: [hi_data.md](specs/approved/hi_data.md).

## Planned

### v0.29.0 — The team's data on every cloud machine

- [ ] `hi compute up --data <org>/<name>` for RunPod and Shadeform: the
  machine gets `HF_ENDPOINT` and a run token for its lifetime, so
  `hf download`, `load_dataset`, and `hi data get` work over SSH. On a
  managed provider the server issues the token when the start is approved;
  Community Cloud refuses `--data` or asks first.
  Spec: [hi_server_expose.md, Cloud machines](specs/ideas/hi_server_expose.md#cloud-machines) and
  [Releases](specs/ideas/hi_server_expose.md#releases) (2).
- [ ] `hi server expose revoke <run>` ends one run's token early; the server
  keeps the revoked run IDs until they would have expired.
  Spec: [hi_server_expose.md, Run tokens](specs/ideas/hi_server_expose.md#run-tokens) and
  [Commands](specs/ideas/hi_server_expose.md#commands).
- [ ] "1 file", not "1 files", in `hi compute run --data`'s output and on
  the instance.
- [ ] Try the `hi server data` admin menu in a real terminal: add, test,
  and remove an organization.
  Spec: [hi_data.md, Adding organizations](specs/approved/hi_data.md#adding-organizations).
- [ ] Approve [hi_server_expose.md](specs/ideas/hi_server_expose.md) and move it to
  `specs/approved/` once this release is out.

### v0.23.0 — `hi box`: local agent boxes

- [x] `hi box claude|codex [prompt]`, `shell`, and `run -- <command>`
  start a rootless container with the project, or a new git worktree of it,
  and nothing else from the home folder; with a prompt the agent runs on its
  own, without permission prompts.
- [x] No route out but hi's proxy, with `locked`, `dev`, and `open`
  presets, domains from `customizations.hi` in `devcontainer.json` after
  asking once, and `hi box allow`.
- [x] Claude Code's token is added at the proxy; the box only holds a
  placeholder. Codex's sign-in goes in for now.
- [x] `ls`, `attach`, `diff` (flagging files that run on the host), `stop`,
  and `rm`; `--gpu` for the Strix Halo; read-only git hooks and config.
- [x] The safe parts of `devcontainer.json`: `image`, `build.dockerfile`,
  `containerEnv`, and `postCreateCommand`.
  Tested on 2026-10-03: Claude Code and Codex with Docker, and Claude Code
  with rootless Podman on aiw9; `--gpu` with Podman on aiw11 (gfx1151).
  Approved spec: [hi_box.md](specs/approved/hi_box.md).

Still open from the container ideas, for later releases: one image on the
workstation and every provider (`hi compute run --image`), prebuilt project
images, pinned serving images
([hi_compute_serve_pinning.md](specs/ideas/hi_compute_serve_pinning.md)),
and image policy on the server. Podman arrived in `hi install` in v0.22.2.

### v0.30.0 — Learn from agent-machine services

- [ ] Study [boxd](https://docs.boxd.sh/) and similar services (Fly's
  Sprites, E2B, Daytona, Modal Sandboxes) and write down which ideas fit
  `hi compute` and `hi server`. Candidates from boxd:
  - Checkpoints before risky changes, and forking a running machine into an
    identical copy.
  - Suspending idle machines and resuming them quickly, which pairs with the
    idle GPU flag deferred from v0.13.0.
  - An HTTPS URL for every machine or `serve`, instead of an SSH tunnel.
  - Environment variables and secrets set once and injected into every
    machine the user owns, never on Community Cloud (v0.12.2).
  - Sharing a running machine with a teammate, recorded in the audit log.
  - A live desktop view that a person can watch and take over from an agent.
- [ ] Turn the ideas worth building into draft specs in `specs/ideas/`.
  Started: [hi_compute_idle.md](specs/ideas/hi_compute_idle.md) has the
  idle GPU flag and why suspend and resume doesn't fit the current
  providers; [hi_compute_ssh_run.md](specs/ideas/hi_compute_ssh_run.md)
  covers runs on RunPod, Shadeform, and managed providers. The services
  were compared on 2026-10-01 in [hi_box.md](specs/approved/hi_box.md#services).

### v0.31.0 — skills.sh

- [ ] Explore integrating [skills.sh](https://www.skills.sh/), Vercel Labs'
  open directory of agent skills installed with `npx skills add <owner/repo>`
  into Claude Code, Codex, and 20+ other agents. Ideas to weigh:
  - Publish the `hi` skill from v0.7.1 so `npx skills add hifinab/cli`
    installs the same files as `hi skill`, and link it from `llms.txt`.
  - Add community skills to a project with `hi skill add <owner/repo>`, or
    list them in `hi init` templates, written into `.agents/skills` and
    linked from `.claude/skills` like the `hi` skill.
  - Pin each added skill to a commit, so a skill that worked keeps working
    and a changed upstream skill shows up as a diff.
  - Keep the private skills in `hifinab/templates` (v0.17.0) off the public
    directory, and let `policy.json` list the skill sources a group may add.
- [ ] Turn the ideas worth building into a draft spec in `specs/ideas/`.

Dependency: extends the v0.7.1 `hi skill` and the v0.17.0 server templates.

### Later — `hi agent`

- [ ] Tiers (`--tier read|edit|full`), `--max`, `resume`, a configured
  agent order that moves on when one is out of quota, version checks, and
  nested calls through the box socket with depth, concurrency, and time
  limits. Spec: [hi_agent.md, Releases](specs/approved/hi_agent.md#releases) (2).
- [ ] Agents' sign-ins kept on the hi server, with spend limits, audit, and
  the live view. Spec: [hi_agent.md](specs/approved/hi_agent.md#sign-ins-on-the-server-release-3) (3).
- [ ] `--on <machine>` and `--remote`, Antigravity CLI, and `hi agent race`.
  Spec: [hi_agent.md, Releases](specs/approved/hi_agent.md#releases) (4).

### Later — `hi data` and `hi server expose`

Follow-ups to v0.24–v0.28, not scheduled yet.

- [ ] On hold (decided 2026-10-03): a line per organization in the weekly
  Slack report, from `data_usage.jsonl`.
  Spec: [hi_data.md, Records](specs/approved/hi_data.md#records).
- [ ] On hold (decided 2026-10-03): `hi data upload`, with its limits
  (which repositories or buckets, approval, review) to be decided then.
  Spec: [hi_data.md, Later: uploads](specs/approved/hi_data.md#later-uploads).
- [ ] A stable public address for `hi server expose`: a custom domain
  (`--with-custom-domain`), so a restart of `netbird expose` doesn't cut
  off runs already going.
  Spec: [hi_server_expose.md, Open questions](specs/ideas/hi_server_expose.md#open-questions) (2) and
  [The exposure's lifetime](specs/ideas/hi_server_expose.md#the-exposures-lifetime).
- [ ] `hi server expose on|off`, in place of `"expose_listen": "off"`.
  Spec: [hi_server_expose.md, Commands](specs/ideas/hi_server_expose.md#commands).
- [ ] Colab through the proxy: a way to hand Colab the run token, so its
  runs get no one-hour links and can call `load_dataset`.
  Spec: [hi_data.md, Known limits](specs/approved/hi_data.md#known-limits) and
  [hi_server_expose.md, Cloud machines](specs/ideas/hi_server_expose.md#cloud-machines).
- [ ] `--data` for container runs (`hi compute run <image> -- <command>`),
  with a shell-only downloader.
  Spec: [hi_data.md, Known limits](specs/approved/hi_data.md#known-limits).
- [ ] Boxes running longer than a day: the box proxy asks for a fresh hi
  data token before the old one expires.
  Spec: [hi_data.md, Known limits](specs/approved/hi_data.md#known-limits) and
  [`hi box --data`](specs/approved/hi_data.md#hi-box---data).
- [ ] More routes on the instance listener, each its own scope on the run
  token: the team model for `hi q` on cloud machines, and an idle signal so
  a machine can stop itself (with [hi_compute_idle.md](specs/ideas/hi_compute_idle.md)).
  Spec: [hi_server_expose.md, Releases](specs/ideas/hi_server_expose.md#releases) (3).
- [ ] Decide one exposure for all runs or one per run, and whether to add
  NetBird's dashboard-only header check as a second lock.
  Spec: [hi_server_expose.md, Open questions](specs/ideas/hi_server_expose.md#open-questions) (1, 3).
- [ ] The S3-compatible gateway for buckets (`s3.hf.co`), for tools that
  only speak S3.
  Spec: [hi_data.md, Open questions](specs/approved/hi_data.md#open-questions).

## Deferred until everything else is done

More providers come last, after every planned release above, and only if
they are needed. RunPod, Shadeform, Hugging Face Jobs, and Colab already
cover most needs.

### Deferred — More compute providers

On-demand providers to add to `hi compute`, in priority order. Ranked
2026-09-29 on three criteria in order: an easy API, a low on-demand price,
and coverage across many countries. Prices are USD per GPU-hour for a single
GPU, checked 2026-09-28. Spot pricing played no part in the ranking. Specs
not yet written.

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

### Deferred — More `hi q` backends

- [ ] Codex and opencode as `hi q` backends. Deferred on 2026-10-02: with the
  team model through `hi server` and OpenRouter, they add a second, slow
  path that mainly helps people without a team server.

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

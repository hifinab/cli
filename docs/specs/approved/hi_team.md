# `hi team` specification

Status: Approved (2026-10-10). The first slice is built (v0.37.0).

Dependencies: `hi box` (boxes, the proxy, token injection, the allowlist),
`hi agent` (Claude Code, Codex, and Hermes in a box, reports, stats, models),
[hi_agent_best_of.md](hi_agent_best_of.md) (checked and ranked runs),
[hi_agent_bundles.md](hi_agent_bundles.md) (bundles and briefs), and
optionally `hi server` (Slack approvals, budgets, `hi server ai`).

## Goal

A hi team is a virtual team of agents that lives in one Slack channel and
builds and runs one project for the people in it: an app, a tool, a data
pipeline, an integration.

```sh
hi team new payments     # a wizard: Slack app, channel, people, members, keys
hi team up payments      # one line: the team starts and stays up
```

The people in the channel may not read code. They may be admins who are
experts in their own field and want "an app that does X". They talk to the
team in plain language, try what it builds, and say whether it works. The
team does the rest.

- **One team, one channel, one Slack app, one host.** A team never spans
  hosts or channels. A new project is a new team.
- **The team is a folder.** Everything a team knows and has made lives in
  one folder on the host. The containers are disposable, rebuilt from the
  folder. Backing up, restoring, or moving a team works on the folder.
- **No docker compose.** A team is hi boxes, started and kept up by hi.

Wording: in hi's docs, "your team" is always the people (and "your team's
hi server" their server). "A hi team" or "the payments team" is always the
virtual one.

## Members

A team is a set of members. A member is a role: an agent, a model, a
bundle, and standing instructions.

```jsonc
// ~/.local/share/hi/teams/payments/team.json
{
  "name": "payments",
  "purpose": "An app for the finance admins to track supplier payments",
  "channel": "C07PAYMENTS",
  "members": {
    "lead":       { "agent": "hermes", "model": "qwen/qwen3.8-max-0902" },
    "coder":      { "agent": "claude", "model": "opus",   "bundle": "web-dev" },
    "researcher": { "agent": "codex",  "bundle": "research" },
    "reviewer":   { "agent": "codex",  "model": "gpt-5.5" },
    "devops":     { "agent": "claude", "model": "sonnet", "bundle": "devops" }
  },
  "budget_usd_month": 300
}
```

- **The lead** (Hermes) is the only member that runs all the time. It is
  the communicator and coordinator: it talks in the channel, keeps the
  project's memory, specs, and docs, breaks work into tasks, and decides
  which member gets each one. It uses Hermes' own Slack gateway, memory,
  cron, and subagents.
- **Every other member** is called for one task at a time: the lead hands
  it a brief, it works in a box of its own, and it leaves a report, as
  `hi agent` does today. Continuity comes from the lead's memory and from
  resuming the member's last session for the same piece of work
  (`claude --resume`), not from a process that stays up.
- In the channel the lead speaks of members by role ("I've asked the
  researcher to compare the two payment providers"), so the channel reads
  like a team at work.
- `hi team add <team> <role>` and `hi team rm <team> <role>` hire and drop
  members without running the wizard again.

The default roster is lead, coder, reviewer, and devops; the researcher and
others are optional. The reviewer must be a different agent or model from
the coder, so the code is checked by someone other than its author.

## The lead bridges the gap

The lead keeps two versions of every piece of work:

- **For the people:** what it will do, in their words, and how they will
  know it works: a short list of things they can try ("Open the payments
  page, add a supplier, and the supplier shows in the list"). The people
  agree to this list before work starts. It is the feature's acceptance
  list.
- **For the members:** the technical spec and the brief: files, data
  model, tests, the checks that must pass, and the acceptance list turned
  into automated tests where it can be.

Both live in the team folder (`specs/`) and in the repository's docs, so a
new member, a restored team, or a person reading later sees the same
history. The lead never asks the people a code question; when it needs a
decision it asks it as a choice about behavior.

## Definition of done

A feature is done when it runs on the live app and the people agree it
works. Getting there:

1. **Spec.** The lead and the people agree the acceptance list.
2. **Build.** The coder works on a branch in its own box, with tests for
   the acceptance list.
3. **Review.** The reviewer reads the diff against the spec and runs the
   checks; for important work, `hi agent best-of` runs the task more than
   once and keeps the best. A rejected change goes back to the coder with
   the reviewer's notes, up to a set number of rounds, then to the lead.
4. **Merge.** Only the lead merges to `main`, and only after the review
   passes. No person reviews code. `main` is always deployable.
5. **Preview.** Devops deploys `main` to the team's preview app, and the
   lead asks the people to try the acceptance list there, with a link.
6. **Live.** When the people say it works, devops deploys to the live
   app. Small fixes (a typo, a broken button, a wrong label) go live without
   waiting: the lead decides what is small, deploys, and tells the channel
   what changed. Every live deploy
   is a release with a number, and a snapshot is taken first (below).
7. **Done, or a bug.** The lead marks the feature done in its notes. A bug
   found later by a person, the lead, or the reviewer becomes a new task
   linked to the feature, and goes through the same steps.

To the people this looks like "straight to main": nobody waits for a human
code review. The branch, the review, and the merge cost the agents little,
and they keep `main` working, give every change a clear point to roll back
to, and keep the coder's mistakes out of the live app.

Who decides what is set in the wizard: who may ask for work (members of
the channel by default), who may approve a live deploy, and who may roll
back or change the budget (the team's owners).

## The team folder

```text
~/.local/share/hi/teams/payments/      (HI_TEAMS_DIR moves it)
  team.json          members, channel, owners, budget (no secrets)
  secrets.json       Slack tokens, keys (0600, never exported in the clear)
  slack-manifest.json
  lead/              Hermes' home: memory, state.db, SOUL.md, skills, cron
  specs/             what the lead and the people agreed, feature by feature
  repo/              the project's git repository (main, branches, tags)
  tasks/             each task's brief (t-3.md) and record (t-3.json)
  app/               the running app's data: databases, uploads
  run/               the lead box's home and its proxy's allowlist and log
```

The broker's socket is in the user's runtime folder
(`/run/user/<uid>/hi-team-<name>`), since a socket's path must be short.

Bind-mounted into the boxes; no Docker volumes, so a copy of the folder is
a copy of the team.

## Commands

```text
hi team new <name>              the wizard
hi team up <name>               start the team and keep it up (also after a reboot)
hi team down <name>             stop it; the folder stays
hi team ls                      teams on this host, up or down, spend this month
hi team status <name>           members, running tasks, preview and live versions
hi team logs <name> [<role>]    the lead's log, or a member's
hi team add <name> <role>       hire a member
hi team rm <name> <role>        drop a member
hi team snapshot <name>         take a snapshot now
hi team snapshots <name>        list them
hi team rollback <name> <release|snapshot|date>
hi team export <name> <file>    the folder as one encrypted file
hi team import <file>           a team from an export, on this or another host
hi team manifest <name>         print the Slack app manifest again
hi team remove <name>           delete the team (asks; offers a last export)
```

The lead runs these in its box, through the broker:

```text
hi team task <role> <brief.md|-> [--on <task>]   start a task; --on continues a task on its branch
hi team review <task> [<notes.md>]               the reviewer checks a task's latest commit
hi team wait <task>                              wait for a task and print its report
hi team show <task>                              its state and report, without waiting
hi team tasks                                    list the tasks
hi team merge <task>                             merge it, once a review approved its latest commit
hi team stop <task>                              stop a task's box
```

- A brief gets a heading before it is saved, so front matter in it can't
  widen the member's box.
- A review starts on the task's branch (`hi agent --from`). Its verdict is
  approve only when the reviewer finished and its summary starts with
  "Approve".
- A merge is `git merge --no-ff` into the team's branch, then a push when
  the repository was cloned. The merged tasks' boxes and branches, and
  their reviews', are removed.

## The wizard

`hi team new <name>` asks, one step at a time, in a terminal:

1. **Name and purpose.** One sentence; the lead's first memory, and the
   start of its SOUL.md.
2. **Slack app.** hi prints a manifest with the team's name: the bot is
   "payments team", the description is the purpose, and the scopes,
   events, and slash commands are those Hermes needs, from
   `hermes slack manifest` with hi's additions. hi tells the user to
   create the app from it (Create New App → From a manifest) and install
   it, then asks for the bot token and the app token without echo, and
   checks both.
3. **Channel.** The channel ID; hi checks the bot is in it.
4. **People.** Owners and who may ask for work, as Slack members.
5. **Members.** The default roster, with each member's agent and model;
   Enter keeps the defaults.
6. **Model keys.** Through your team's hi server (`hi server ai`), which
   records the team's spend, or keys of its own. Either way the keys stay
   outside the boxes, through the proxy.
7. **Code.** A new repository, or an existing one and a token limited to
   it.
8. **Backups.** The S3-compatible bucket offsite snapshots go to (its
   endpoint, bucket, and keys, without echo), and how long to keep them.
9. **Budget.** Dollars a month; the lead stops starting tasks when it is
   spent and says so in the channel.

Each answer can also come from a flag or a file, for setting up a team
without a terminal (`hi team new payments --from payments.json`).

At the end hi prints what it set up and the one line to start it.

## How it runs

- **The lead's box** runs all the time: Hermes' Slack gateway, with the
  team folder's `lead/` as its home. Slack over Socket Mode is outbound
  only, so the host needs no public address.
- **Member boxes** start for a task and stop after it, as `hi agent` boxes
  with a worktree of `repo/`.
- **A broker on the host** is the lead's only way to start member boxes:
  a socket in the lead's box that accepts "run this brief as this role",
  "status", "report", and "stop". hi checks the role and the budget, then
  starts the box. hi sets no limit on how many boxes run at once; capacity
  is up to whoever runs the host. The lead never holds
  Claude, Codex, or GitHub credentials.
- **The app** runs in boxes of its own on the team's host: preview and
  live, reachable over NetBird, or on a public address when the team allows
  it. It stays on the host until someone running the host decides to move
  it.
- **Staying up.** `hi team up` installs a systemd user unit for the team,
  so it starts at boot and restarts after a crash, as `hi server` does.

## Backups and rollback

A team has three kinds of state, each kept differently:

| State | Where | How it is kept |
|---|---|---|
| Code | `repo/` | git: every merge is a commit, every release a tag, pushed to the remote |
| The lead's memory, specs, docs | `lead/`, `specs/` | snapshots |
| The app's data | `app/` | snapshots; this is the people's real work and matters most |

**Snapshots** cover the whole folder except secrets, and are taken:

- **before every live deploy**, so each release pairs its code with the
  data and memory as they were (the snapshot is named after the release);
- **every day**, kept rolling: 7 daily, 4 weekly, 6 monthly by default;
- **by hand** with `hi team snapshot`.

A snapshot is consistent: SQLite files are copied with SQLite's backup API
and app databases with their own dump, not as raw files while running.
Snapshots are encrypted and deduplicated (restic, or an equivalent hi
ships), kept on the host and copied offsite to the S3-compatible bucket chosen
in the wizard.

Not per commit: commits to `main` are already kept by git, and the app's
data changes on its own schedule, so the snapshots that matter are before
a deploy and daily.

The containers are never snapshotted. They are rebuilt from the base image
and the folder.

**Rollback.** `hi team rollback payments r12` puts back the release's code
and its snapshot of data and memory, and deploys it. `--code-only` and
`--memory-only` put back just one. In Slack the lead may propose a
rollback ("the last release broke the export; roll back to yesterday?"),
and only an owner's button carries it out.

## Security

- Anyone who can post in the channel can steer the team, so only the
  listed people are heard, and nothing the people say can widen what a box
  may reach.
- Each box reaches only its allowlist, through the proxy.
- Model keys never enter a box; tokens for Slack and the repository are
  scoped to this team only.
- Live deploys, rollbacks, budget changes, and new hosts on the allowlist
  need an owner.
- Every task, review, merge, deploy, and rollback is in the team's log.

## Reused and new

Reused: boxes, the proxy and token injection, `hi agent` and its reports
and stats, best-of, bundles, the per-agent models, Hermes' Slack gateway,
memory, cron, and manifest, `hi server ai` for spend, and the systemd
pattern from `hi server`.

New: the team folder, the wizard, the broker, keeping the lead up, the
preview and live app boxes, snapshots and rollback, and export and import.

## First slice

`hi team new`, `up`, `down`, `ls`, `status`, `logs`, and `manifest`: the
lead in one channel, its memory in the folder, and the coder and reviewer
through the broker, merging to the team's branch. Built in v0.37.0; the
wizard asks for purpose, Slack, channel, people, members, and code, and
leaves backups and the budget to later slices. Then the app's preview and
live boxes, then snapshots and rollback, then export and import.

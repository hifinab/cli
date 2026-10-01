---
title: Agent skill
description: How hi skill writes an Agent Skill that teaches Claude Code, Codex, and other coding agents to use hi safely, and where the files go.
---

`hi skill` writes a [skill](https://agentskills.io) that coding agents load
when a task involves remote compute or workstation setup. It contains the
safety rules and a compact guide to the commands.

## Commands

```sh
hi skill            # into the current folder
hi skill --global   # into your home folder, for every project
hi skill --print    # show it without writing anything
hi skill --force    # replace a hi skill that hi did not write
```

Using Codex or another agent on a new machine? Run `hi skill --global` once,
and every session there can load the skill. An agent that doesn't have `hi`
yet can start from [hifin.sh/llms.txt](/llms.txt), which tells it to install
`hi` and follow this skill.

## Where the files go

| Path                          | What                                    | Read by                              |
|-------------------------------|-----------------------------------------|--------------------------------------|
| `.agents/skills/hi/SKILL.md`  | The skill                               | Codex and other Agent Skills readers |
| `.claude/skills/hi`           | A link to `../../.agents/skills/hi`     | Claude Code                          |

Claude Code follows the Agent Skills file format but looks for skills only in
`.claude/skills`, so `hi` links that location to the one real copy. Commit both
paths; Git stores the link as a link.

`--global` writes the same paths under your home folder.

## What agents learn

- **Rules:** ask before starting paid hardware and show a `--dry-run` first;
  only then pass `--yes`; always set `--max`; stop what they start and check
  `hi compute ls`; pass credentials with `--secret`, never `--env`; ask before
  `hi install`, `hi adduser`, or `hi net`.
- **How to:** choose providers and hardware, run jobs, use instances and
  tunnels, serve models, read logs, and fix common errors.
- **Managed compute:** on a machine that has joined a
  [hi server](/guide/compute/managed/), send requests with `--reason` and
  `--no-wait`, wait with `hi compute requests <id> --wait`, report a denial
  instead of working around it, and never approve anything.

Read the whole skill with `hi skill --print`.

## Updating

Each file starts with a marker such as
`<!-- Written by hi v0.7.1; rerun hi skill to update. -->`. Running `hi skill`
again replaces files with that marker. A `.claude/skills/hi` folder from an
earlier `hi` version is replaced by the link. A `hi` skill that someone wrote
by hand is kept unless you add `--force`.

# `hi skill` specification

Status: Released in v0.7.1. Extended by [hi_skills_sh.md](hi_skills_sh.md): skills from skills.sh, `hi skill add`, `update`, and the selector.

Dependencies: none; the skill is embedded in the `hi` binary.

## Goal

Teach coding agents (Claude Code, Codex, omp, and others that read Agent
Skills) how to use `hi` safely, especially `hi compute`, which spends money.

## Commands

```text
hi skill            Write the skill into the current folder
hi skill --global   Write it into the home folder, for every project
hi skill --print    Print it without writing anything
hi skill --force    Replace a hi skill that hi did not write
```

`hi skill` writes `SKILL.md` once and links it where Claude Code looks:

| Path                           | What                        | Read by                               |
|--------------------------------|-----------------------------|---------------------------------------|
| `.agents/skills/hi/SKILL.md`   | The skill                   | Codex and other Agent Skills readers  |
| `.claude/skills/hi`            | Symlink to `../../.agents/skills/hi` | Claude Code                  |

Claude Code follows the Agent Skills file format but, as of 2.1.283, only
discovers skills in `.claude/skills`; a folder holding only
`.agents/skills/hi` was not found, and the symlink was. A `.claude/skills/hi`
folder from an earlier `hi skill` is replaced by the link.

`--global` uses the same paths under `$HOME`. A project copy can be committed
so the whole team's agents get it.

## Content

The skill source is `skills/hi/SKILL.md` in this repository. It has the
standard `name` and `description` frontmatter (description at most 1024
characters) and covers:

- Rules: ask the user before starting paid hardware and show `--dry-run`
  first; only then pass `--yes`; always set `--max`; stop what you start;
  pass credentials with `--secret` and never print tokens; ask before
  `hi install`, `hi adduser`, or `hi net`.
- Choosing providers and hardware, runs, interactive machines, tunnels,
  serving, listing and stopping, and a troubleshooting table.

## Updates

Each written file carries `<!-- Written by hi <version>; rerun `hi skill` to
update. -->` after the frontmatter. Rerunning `hi skill` overwrites files with
that marker and refuses to replace a `hi` skill without it unless `--force`
is given.

## Acceptance criteria

1. `hi skill` writes both files in the current folder and names them.
2. Claude Code started in that folder lists a skill named `hi` (verified with
   Claude Code on 2026-09-27).
3. Rerunning updates the files; a foreign `hi` skill is kept unless `--force`.
4. `--global` writes under `$HOME`; `--print` writes nothing.

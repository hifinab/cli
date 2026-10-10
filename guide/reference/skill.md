---
title: Skills
description: Find, install, and update agent skills from skills.sh and any git repository with hi skill, pinned to a commit with their audits shown, and write the hi skill that teaches agents to use hi safely.
---

[Skills](https://agentskills.io) teach coding agents how to do something:
work with PDFs, query data with DuckDB, drive a browser, or use `hi`.
`hi skill` finds them on [skills.sh](https://skills.sh), installs them from
their git repositories at a recorded commit, and keeps them up to date. It
also writes the `hi` skill, which comes with `hi`.

## Search and pick

```sh
hi skills
```

In a terminal, `hi skills` (or `hi skill`) opens one screen to search,
browse, and pick:

```text
 Skills for ~/projects/pricing                                   g: everywhere
  Search skills.sh › pdf▏

 Installed
   ✓ hi                     built in                                  up to date
 Results for "pdf"
 › ◼ pdf                    anthropics/skills ✓                  207k  safe safe medium
   ◻ docx                   anthropics/skills ✓                  197k  safe critical low
   ◻ pdf                    openai/skills ✓                       13k  safe safe medium

 ── pdf · anthropics/skills ──────────────────────────────────────────────────
 Use this skill whenever the user wants to do anything with PDF files…
 License: Proprietary. LICENSE.txt has complete terms
 Audits: Agent Trust Hub safe · Socket safe · Snyk medium (2026-09-15)

 type: search · ↓↑: move · space: pick · enter: install · 1 picked
 u: update · x: remove · g: here/everywhere · esc: quit
```

- **Type** to search skills.sh; results are sorted by installs, and ✓
  marks the maker of the tool the skill is about (Anthropic, Vercel, DuckDB,
  Hugging Face, and a few more). Many skills on skills.sh are copies of
  others with large install counts, so look at the source.
- **↓** moves from the search into the list. There, **space** picks a skill,
  **enter** installs what you picked (or the highlighted one), **u** updates
  and **x** removes an installed skill, and **g** switches between this
  project and every project. Any other letter goes back to the search.
- Before you type, the list shows suggestions: the `hi` skill when it's
  missing, and well-made skills from the tools' makers.
- Installing leaves the full screen, shows each skill with its license,
  files, and audits, and asks once.

Every action is also a command, for scripts and agents.

## Commands

```sh
hi skill find pdf                            # search skills.sh
hi skill add anthropics/skills --skill pdf   # install from a repository
hi skill add anthropics/skills/pdf           # the same, as skills.sh shows it
hi skill ls                                  # what's installed, from where, at which commit
hi skill show pdf                            # description, license, files, audits
hi skill update                              # move skills to their source's newest commit
hi skill update --check                      # only list what would change; exit 1 if anything would
hi skill rm pdf
hi skill add hi                              # the hi skill
```

| Option          | Meaning                                                          |
|-----------------|------------------------------------------------------------------|
| `--global`      | In your home folder, for every project                           |
| `--skill a,b`   | `add`: which skills, from a source with several                  |
| `--yes`         | Don't ask; `add` takes every skill a source has                   |
| `--accept-risk` | Add or update a skill that an audit rates `high` or `critical`    |
| `--ref <ref>`   | `add`: a branch or tag instead of the default branch             |
| `--force`       | Replace a skill `hi` didn't install, or changes made to one here  |
| `--json`        | `find` and `ls`: JSON on stdout                                  |
| `--bundles`     | `update`: move the skills in `bundles/<name>.json` instead       |

Without a terminal, `add`, `update`, and `rm` need `--yes`, and a source
with several skills needs `--skill` or `--yes`.

## Sources

| Source                                    | Example                                               |
|-------------------------------------------|-------------------------------------------------------|
| GitHub, `owner/repo`                      | `anthropics/skills`                                   |
| One skill, `owner/repo/skill`             | `duckdb/duckdb-skills/query`                          |
| A folder in a GitHub repository           | `https://github.com/owner/repo/tree/main/skills/web`  |
| Any git repository                        | `https://gitlab.com/org/skills`, `git@host:org/skills.git` |
| A folder on this machine                  | `./team-skills`                                       |
| The `hi` skill                            | `hi`                                                  |

`hi` fetches the source with git, using your own git credentials, so
private repositories work as they do for `git clone`. It finds skills as
`npx skills` does: a `SKILL.md` at the top, under `skills/`, or in an
agent's skills folder. Nothing from the source runs while it's fetched or
installed.

## Where the files go

| Path                                  | What                                       | Read by                              |
|---------------------------------------|--------------------------------------------|--------------------------------------|
| `.agents/skills/<name>/`              | The skill                                  | Codex and other Agent Skills readers |
| `.claude/skills/<name>`               | A link to `../../.agents/skills/<name>`    | Claude Code                          |
| `skills-lock.json`                    | Each skill's source, commit, and file hash | `hi skill` and `npx skills`          |

These are the folders and the lock file `npx skills` uses, so your team can
use either tool. `hi` adds each skill's commit to `skills-lock.json`, which
`npx skills` doesn't record. Commit all three; Git stores the links as
links. With `--global`, the skills go under your home folder, and `hi`
keeps its list in `~/.config/hi/skills.json`.

## Updating and trust

A skill is instructions an agent follows without asking, often with
scripts it runs, so `hi` never changes one by itself:

- **Pinned.** Each skill stays at its recorded commit until
  `hi skill update`, which shows what changed (`d` shows the diff) and the
  latest audits, and asks.
- **Audits.** skills.sh has each skill checked by partners (Agent Trust
  Hub, Socket, Snyk, and others). `hi` shows their verdicts, and asks before
  adding or updating a skill any of them rates `high` or `critical`; without
  a terminal it needs `--accept-risk`. Even well-known skills sometimes get
  such a rating, so read the audit rather than refuse by habit.
- **Licenses.** `hi` shows each skill's license. Anthropic's `docx`,
  `xlsx`, `pptx`, and `pdf` skills, for example, are proprietary and
  licensed for use with Anthropic's services. `hi` doesn't ship any of
  these skills; it fetches them from their repository when you ask.
- **Changes made here** are kept: `update` skips a skill whose files you
  changed, unless you add `--force`.
- **No tracking.** `hi` sends skills.sh your searches and asks it for audits
  of public repositories, but no install events. `DO_NOT_TRACK=1` turns off
  searches and audits too; installing and updating still work, through git.

## Bundles

A bundle is a named set of skills for one kind of work, with the packages
each skill needs. `hi agent --bundle web,office` puts the skills in the
box's home folder and runs the box on an image with those packages; see
[Bundles](/guide/agent/#bundles-skills-and-the-tools-they-need).
`hi` has three, in [`bundles/`](https://github.com/hifinab/cli/tree/main/bundles):

| Bundle   | Skills                                                                 |
|----------|------------------------------------------------------------------------|
| `web`    | `agent-browser` from vercel-labs/agent-browser                         |
| `office` | `docx`, `xlsx`, `pptx`, `pdf` from anthropics/skills (proprietary)      |
| `data`   | `query`, `read-file`, `convert-file` from duckdb/duckdb-skills; `data-visualization` from anthropics/knowledge-work-plugins |

A bundle file lists each skill's source, its commit, and what it needs:

```json
{
  "name": "web",
  "description": "Browse and automate websites with agent-browser.",
  "skills": [
    {
      "source": "vercel-labs/agent-browser",
      "skill": "agent-browser",
      "commit": "0207911f1bd4d0393eddaa90f2e50f96e0fb8974",
      "needs": {
        "layer": "heavy",
        "npm": ["agent-browser@0.38.2"],
        "browsers": ["chrome"],
        "network": {"mode": "open", "reason": "visits whatever sites the task needs"}
      }
    }
  ]
}
```

`needs` can have `layer` (`base`, `heavy`, or `light`: the order the
packages go in), `apt`, `pip` (`name==version`), `npm` (`name@version`),
`browsers` (`chromium` from Playwright, `chrome` from
`agent-browser install`), `env`, `network` (`mode`, `hosts`, `reason`), and
`gpu`. Each is one fixed install step, so a bundle can't run commands of
its own; an unknown key or an unpinned package refuses the bundle. The
`hi` skill goes in every bundle.

Your team keeps its own bundles in `bundles/` of a template source on its
[hi server](/guide/compute/managed/), next to `skills/`; there, a skill
whose `source` is `"."` is the source's own `skills/<name>`. Your own go in
`~/.local/share/hi/bundles/bundles/` (or `HI_BUNDLES_DIR`), with the same
layout. A project that always needs a bundle names it in its
`devcontainer.json`, under `customizations.hi.bundles`; see
[Run code in a box](/guide/box/#devcontainerjson).

Whoever looks after the bundles moves the commits in the folder that has
`bundles/`:

```sh
hi skill update --bundles --check    # what would change; exit 1 if anything would
hi skill update --bundles            # show each change and its audits, ask, rewrite the files
hi skill update --bundles office     # one bundle, or one skill
```

When a newer commit mentions a tool the old one didn't, `hi` says so,
since the bundle's packages may need to change too.

## The hi skill

`hi skill add hi` writes the skill that teaches agents to use `hi`. It comes
with `hi`, so it always matches the version you have:

```sh
hi skill add hi            # into the current folder
hi skill add hi --global   # into your home folder, for every project
hi skill --print           # show it without writing anything
```

Without a terminal, plain `hi skill` writes it too, as it always has. Using
Codex or another agent on a new machine? Run `hi skill add hi --global`
once, and every session there can load the skill. An agent that doesn't
have `hi` yet can start from [hifin.sh/llms.txt](/llms.txt), which tells it
to install `hi` and follow this skill.

What agents learn:

- **Rules:** ask before starting paid hardware and show a `--dry-run` first;
  only then pass `--yes`; always set `--max`; stop what they start and check
  `hi compute ls`; pass credentials with `--secret`, never `--env`; ask before
  `hi install`, `hi adduser`, `hi net`, or adding skills.
- **How to:** choose providers and hardware, run jobs, use instances and
  tunnels, serve models, read logs, and fix common errors.
- **Managed compute:** on a machine that has joined a
  [hi server](/guide/compute/managed/), send requests with `--reason` and
  `--no-wait`, wait with `hi compute requests <id> --wait`, report a denial
  instead of working around it, and never approve anything.
- **Sharing a local service:** ask before `hi net expose`, keep its
  password, use `--public` only with the user's yes, keep `--max` short,
  and stop it when done.
- **Your team's data:** get your team's datasets and models with `hi data`, never
  with a Hugging Face token of their own; check sizes first; use
  `hi data run` for code that reads them, and `--data` on cloud runs.

Each file starts with a marker such as
`<!-- Written by hi v0.7.1; rerun hi skill to update. -->`. `hi skill update`
rewrites it after you update `hi`, and `hi skill ls` says when it's from an
older `hi`. A `hi` skill that someone wrote by hand is kept unless you add
`--force`.

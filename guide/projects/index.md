---
title: Start a project
description: Create a new repository from a hi template with hi init, so coding agents get a check they can run, safe settings, and the hi skill from the first commit.
---

`hi init` creates a repository that coding agents work well in from the
start. Every template gives you:

- **`make check`**, one command that runs the lockfile check, formatting,
  lint, type check, and tests. Agents are told to run it before saying a
  change is done.
- **`AGENTS.md`** with a short block of rules, read by Claude Code through
  `CLAUDE.md` and by Codex and other agents directly.
- **Safe agent settings**: Claude Code may not read `.env` or `secrets/`, and
  `.gitignore` keeps them, `data/`, and `results/` out of Git.
- **The `hi` skill**, so agents know how to rent a GPU with `hi compute`.
- **CI** that runs `make check` on every push and pull request.

## Create a project

```sh
hi init                         # choose a template, name, and directory
hi init python pricing-tools    # or say it directly
cd pricing-tools
make check
```

Before writing anything, `hi init` shows every file it will write and every
command it will run, and asks you to confirm. Add `--dry-run` to only see the
plan, or `--yes` to skip the question (for agents and scripts).

## Templates

| Template | For                              | Stack                                                    |
|----------|----------------------------------|----------------------------------------------------------|
| `python` | Libraries and scripts            | uv, ruff, pyrefly, pytest; the package lives in `src/`   |
| `web`    | Internal web apps and dashboards | React and TypeScript on Vite, npm, Biome, Vitest         |

`hi init --list` shows them. `python` needs `uv`, and `web` needs Node.js
with npm; `hi install` sets up both.

These built-in templates are public and generic. The firm's own templates,
such as quant research, will come from the firm's `hi server` to connected
devices.

## Options

| Option                  | Meaning                                                     |
|-------------------------|-------------------------------------------------------------|
| `--name <name>`         | Project name; by default the directory's name               |
| `--dry-run`             | Print the plan and stop                                     |
| `--yes`                 | Do not ask for confirmation                                 |
| `--no-setup`            | Write the files but run no setup command (`uv sync`, `npm ci`) |
| `--github <owner/repo>` | Also create a private GitHub repository and push the first commit |

Project names use lowercase letters, digits, and dashes, starting with a
letter. In `python`, `pricing-tools` becomes the package `pricing_tools`.

## In an existing directory

`hi init python .` applies a template to the current directory. It never
deletes or overwrites a file that has other content: it lists every such
file and stops before writing anything. Running it again when everything
already matches does nothing.

## What hi records

`.hifin/template.json` records the template, the `hi` version, and a hash of
every file it wrote. It holds no paths, user names, or secrets. Commit it; a
later `hi` uses it to bring the repository up to date with the templates.

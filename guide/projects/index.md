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
| `service` | Backend services and APIs       | `python` + FastAPI, SQLAlchemy and Alembic, `/healthz` and `/readyz`, Dockerfile, compose |
| `pipeline` | Data sources and ingestion     | `python` + one idempotent `run --date`, raw files landed before parsing, offline parser tests |
| `ml`     | Model training                   | `python` + PyTorch for CPU, CUDA, or ROCm, a typed config, a two-step training test |
| `autoresearch-ml` | Agents improving model training overnight | PyTorch as in `ml`; `train.py` trains for a fixed five minutes, a fixed evaluation scores it, a holdout outside the repo |
| `autoresearch-quant` | Agents improving a trading strategy | pandas; `strategy.py` scored by a fixed, lookahead-free backtest net of fees, a holdout outside the repo |

The two `autoresearch` templates are shaped for `hi agent best-of` rounds;
see [Start from a template](/guide/autoresearch/templates/).

`hi init --list` shows them. The Python ones need `uv`, and `web` needs
Node.js with npm; `hi install` sets up both.

Each adds its own checks to `make check`: `service` checks that the health
endpoints answer and that migrations match the models, `pipeline` that a
rerun loads nothing new, and `ml` trains for two steps on the CPU. In `ml`,
`make` picks the PyTorch build for the machine: `rocm` on a Strix Halo
(AMD's build for gfx1151), `cuda` with an NVIDIA GPU, `cpu` otherwise.
`make sync` installs it and `make train` trains on the GPU; `make help`
shows which build it chose, and `TORCH = cpu` in `local.mk` overrides it.

`hi init` checks in the background whether a newer `hi` is out, and says so:
the built-in templates are part of `hi`, so `hi update` is how you get their
newest versions.

These built-in templates are public and generic. On a device connected to
your team's `hi server` (`hi connect`), `hi init` also offers the templates
from the team's private repo, such as `research`. The menu marks them
`(private repo)`, and `hi init --list` shows the source and commit they come
from. `hi` checks that they are signed by the server and keeps a copy, so
they still work when the server can't be reached.

## Options

| Option                  | Meaning                                                     |
|-------------------------|-------------------------------------------------------------|
| `--name <name>`         | Project name; by default the directory's name               |
| `--dry-run`             | Print the plan and stop                                     |
| `--yes`                 | Do not ask for confirmation                                 |
| `--no-setup`            | Write the files but run no setup command (`uv sync`, `npm ci`, or `make sync` in `ml`) |
| `--github <owner/repo>` | Also create a private GitHub repository and push the first commit |
| `--force`               | With `--update` or `--adopt`: overwrite files `hi` owns even when edited by hand |
| `--strict`              | With `--check`: fail when private layers can't be checked   |

Project names use lowercase letters, digits, and dashes, starting with a
letter. In `python`, `pricing-tools` becomes the package `pricing_tools`.

## In an empty or new directory

`hi init python .` applies a template to the current directory. It never
deletes or overwrites a file that has other content: it lists every such
file and stops before writing anything. Running it again when everything
already matches does nothing.

## Keep a project up to date

Templates improve over time: a new `hi` brings new built-in templates, and
the team's private repo changes on its own schedule. Bring a project up to
date from its folder:

```sh
hi init --update --dry-run    # what would change
hi init --update              # change it, after asking
```

`hi` sorts every file it ever wrote into three kinds, and treats each
differently:

| Kind      | Examples                                         | On update |
|-----------|--------------------------------------------------|-----------|
| Owned     | `CLAUDE.md`, `.claude/settings.json`, CI, skills | Replaced, unless someone edited it |
| Managed   | `AGENTS.md`, `.gitignore`, `Makefile`            | Only the block between `hi:begin` and `hi:end` is replaced; the rest is yours |
| Seeded    | Source, tests, `README.md`, `pyproject.toml`     | Never touched |

- A file `hi` owns, or a managed block, that someone edited by hand is a
  **conflict**: `hi` leaves it alone, says so, and exits with an error.
  Make the change in the template instead, or rerun with `--force` to
  overwrite it.
- When the template changed a seeded file, `hi` writes what changed to
  `docs/upgrades/<version>.md`: a diff and short instructions. Ask an agent
  to apply what fits in a pull request, then delete the file.
- Owned files the template no longer has are deleted, if nobody edited them.

### Check it in CI

`hi init --update --check` changes nothing and exits with an error when the
project is behind the templates or has conflicts. Add it to CI:

```yaml
- run: curl -fsSL https://hifin.sh/install.sh | sh
- run: ~/.local/bin/hi init --update --check
```

CI can't reach your team's `hi server`, so there it checks only what came
from the built-in templates and says which private layers it skipped.
`--strict` makes that an error, for a check on a connected machine.

When an admin renames a private template source, `--update` notices that the
recorded source is gone, uses the renamed one with the same layers, and
records its new name.

## Adopt an existing repository

`hi init --adopt <template>` brings a repository that predates `hi init`
under a template, without moving or rewriting any of its code:

```sh
cd legacy-tool
hi init --adopt python --dry-run
hi init --adopt python
```

- It adds the files `hi` owns (agent settings, CI, skills) and puts the
  managed blocks at the top of `AGENTS.md`, `.gitignore`, and `Makefile`,
  keeping what was there.
- It never adds the template's starting files, such as `pyproject.toml`; it
  lists the ones the repository doesn't have, so you know why `make check`
  may not pass yet.
- An existing file that `hi` would own, or a `Makefile` that already has
  `check`, `test`, `lint`, `fix`, or `help` targets, is a conflict: nothing
  is written until you move it aside or pass `--force`.

From then on, `hi init --update` works on it like on any generated project.

## What hi records

`.hifin/template.json` records the template, the `hi` version or private
commit of each layer, and a hash of every file it wrote with its kind and
source. It holds no paths, user names, or secrets. Commit it: `--update`
and `--check` depend on it.

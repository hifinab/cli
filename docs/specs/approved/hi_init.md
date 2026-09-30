# `hi init` specification

Status: Draft revision (2026-09-29). Replaces the approved version once
reviewed; the open decisions at the end must be settled first.

Dependencies: `uv` on `PATH` for Python templates; `gh` signed in with access
to the private template repositories; the release installation.

## Goal

Every new repository starts in a shape that coding agents work well in, and
stays in that shape as the firm's conventions improve. One command creates a
repository for any kind of project the firm builds: quant research, model
training, data pipelines, services, web and mobile apps, and CLI tools.

The value is not the folders. It is three things no folder template gives:

1. **A check an agent can run.** Every repository answers `make check` with a
   non-zero exit on any formatting, lint, type, or test failure. Agents are
   told to run it before claiming work is done.
2. **Guardrails as code, not prose.** Rules that must never break (no
   lookahead in a backtest, no credentials in the repository, no committed
   data) are tests, hooks, and deny rules that ship with the template, not
   paragraphs in `AGENTS.md`.
3. **Firm knowledge where agents find it.** Firm skills (data sources,
   validation methodology, compute) are installed from one maintained source,
   pinned, and updated in every repository by `hi`, instead of copied by hand
   and left to drift.

## Principles

- **Short, hand-written instructions.** Generated `AGENTS.md` content is kept
  to what an agent cannot infer from the code: the commands, the rules that
  differ from defaults, the paths not to touch, and links into `docs/`.
  Directory tours and generic advice are left out; measurements show they add
  cost without improving results (see Evidence).
- **Templates are runnable projects.** Each template, once composed, is a
  valid project with real manifests and lockfiles, so linters, Renovate, and
  CI work on the template repository itself. Parameters are sentinel names
  replaced at generation, not a templating language spread across files.
- **One default per type.** A template never asks which framework to use. The
  stack is decided once, here and in the template repository, and changed for
  everyone at once.
- **Build a template when a real project needs it.** Types without prior art
  at the firm (web, mobile, CLI) get a template when the first real project of
  that type starts, shaped by that project, rather than speculatively.

## Where templates and skills live

Two private repositories in the `hifinab` organization:

- **`hifinab/templates`**: every template in one repository, as a shared
  `base` layer plus one layer per type. One repository because most changes
  are to the shared base (agent instructions, CI, ignores, checks), and one
  pull request should update and test every type at once. `CODEOWNERS` gives
  each type's directory an owner.
- **`hifinab/skills`**: firm skills in the Agent Skills format, one flat
  folder per skill (`<name>/SKILL.md`, the name matching the folder). Separate
  from templates because existing repositories use skills too, and skills
  change on their own schedule.

Both are private because they carry firm knowledge. `hi` is public, so neither
is embedded in the binary. `hi` fetches a pinned release of each through the
GitHub API with the token from `gh auth token`, read in memory as allowed by
[hi_login.md](hi_login.md), verifies the tag's commit, and caches it under
`~/.cache/hi/templates/<commit>/`. A cached release works offline.

Each `hi` release names a default templates release. `--ref <tag>` chooses
another. Templates declare the oldest `hi` they need; an older `hi` refuses
with the version to update to.

The GitHub "template repository" feature is not used: it has no parameters
and no way to update a repository later.

### Layout of `hifinab/templates`

```text
index.toml              catalog: name, summary, owner, status
base/                   shared by every type
python/                 extends base
research/               extends python
ml/                     extends python
pipeline/               extends python
service/                extends python
web/                    extends base
mobile/                 extends base
cli/                    extends python
migrations/<type>/      schema changes, e.g. 2-to-3.toml
.github/workflows/      generate every template and run its check
```

Layers apply in order (`base`, then `python`, then `research`); a later layer
replaces an earlier layer's file. Fragments such as ignore rules concatenate
into one managed block.

### Template manifest

Each layer has a `template.toml`:

```toml
name = "research"
summary = "Quant strategy research with backtesting"
extends = "python"
schema = 1              # raised only for a layout change that needs a migration
requires_hi = ">=0.15.0"

[params.name]
pattern = "^[a-z][a-z0-9-]*$"

[substitute]            # sentinel → value, in file contents and paths
"hifin_template_pkg" = "{{ .name | snake }}"

[files]
owned = ["CLAUDE.md", ".claude/settings.json", ".github/workflows/hi-*.yml"]
managed = ["AGENTS.md", ".gitignore", "Makefile"]
# everything else is seeded

skills = ["time-series-validity", "firm-data", "hi"]

[commands]              # argument lists, shown in the plan; never shell strings
setup = [["uv", "sync"]]
check = [["make", "check"]]
```

## Commands

```text
hi init                          guided: type, name, directory
hi init <type> [directory]       e.g. hi init research alpha-momentum
hi init --list                   templates in the pinned release, with status
hi init --update                 bring this repository up to a newer release
hi init --adopt <type>           bring an existing repository under a template
```

Options: `--name`, `--ref <tag>`, `--yes` (no confirmation; for agents and
CI), `--dry-run` (print the plan and stop), and `--github <owner/repo>` to
also create a private GitHub repository with `gh repo create` and push the
first commit.

Template names are reserved arguments, never project names. With no directory,
the target is `./<name>`; `.` targets the current directory.

The guided form and the direct form use the same planner. Before any change,
`hi` prints the template and release, the target, every file it will write,
and every command it will run, and asks for confirmation.

## Every repository (the `base` layer)

```text
AGENTS.md                   managed block (firm rules) + the project's own text
CLAUDE.md                   "@AGENTS.md"
.agents/skills/<name>/      firm skills, pinned; owned by hi
.claude/skills -> ../.agents/skills
.claude/settings.json       deny reading .env and secrets; allow make check
Makefile                    help, check, fix, test (managed block + project targets)
.pre-commit-config.yaml     format, lint, secret scan (run by prek)
.github/workflows/check.yml make check on every push and pull request
.env.example                names of the standard variables, no values
.gitignore                  .env*, data/, results/, caches, notebook outputs
docs/specs/                 feature specifications
docs/roadmap.md             planned work, decisions required, completed
docs/decisions/             short decision records
.hifin/template.json        what generated this repository
README.md
```

- **`AGENTS.md`** starts with a block between `<!-- hi:begin -->` and
  `<!-- hi:end -->` holding the firm-wide rules for this type (at most about
  40 lines), followed by a short project section the team grows from observed
  agent mistakes. Claude Code reads it through `CLAUDE.md`'s `@AGENTS.md`
  import, which works on every version and surface; other agents read
  `AGENTS.md` directly.
- **Skills** are copied from `hifinab/skills` into `.agents/skills`, with a
  symlink for Claude Code, which reads only `.claude/skills`. Copies, rather
  than a Claude plugin marketplace, because they work for every agent and in
  cloud sessions, where marketplace plugins do not load. Each copy carries the
  `hi` marker used by `hi skill`, its release is recorded in
  `.hifin/template.json`, and a template installs at most a handful, since
  every skill description costs context on every turn.
- **`make check`** runs, in order: lockfile check, format check, lint, type
  check, tests. It is offline and deterministic. Make is used because the firm
  already uses Makefiles and every machine has it.
- **Secrets** stay in `.env` (ignored) and the environment. `.claude/settings.json`
  denies reading `.env*` and `secrets/`; this is a guard for Claude's own
  tools, not a boundary against scripts, and `AGENTS.md` says so.

## Template types

Stacks are defaults for the first release of each template. Versions are
pinned in the template repository, not here.

| Type | Use for | Default stack | Adds to `make check` | Status |
|---|---|---|---|---|
| `python` | Libraries and scripts | Python, uv, ruff, pyrefly, pytest | — | v0.16.0 |
| `research` | Strategy research and backtesting | `python` + DuckDB, marimo notebooks, MLflow, backtest harness (engine: decision 1) | lookahead, cost, and holdout guard tests; a golden backtest on synthetic data | v0.16.0 |
| `ml` | Model training, fine-tuning, benchmarks | `python` + PyTorch (ROCm and CUDA extras), typed config, MLflow, training image for `hi compute` | a two-step CPU training smoke test | v0.17.0 |
| `pipeline` | Data sources, scrapers, ingestion | `python` + one idempotent `run --date` entry point, raw landing before parsing, scheduler (decision 3) | offline parser tests against recorded responses; a rerun loads nothing new | v0.17.0 |
| `service` | Backend services and APIs | `python` + FastAPI, pydantic-settings, SQLAlchemy and Alembic, `/healthz` and `/readyz`, Dockerfile, compose | migrations match models; health endpoints answer | v0.17.0 |
| `web` | Internal web apps and dashboards | `service` backend + React on Vite with a client generated from OpenAPI, pnpm, Biome, Vitest, Playwright | generated client is current; Playwright smoke test | with the first project |
| `mobile` | Mobile apps | Expo (expo-router, EAS), Biome, Jest | `expo-doctor` | with the first project |
| `cli` | Command-line tools | `python` + Typer, installed with `uv tool install` | exit codes and `--json` output | with the first project |

### `research`

The template's value is its guards. The backtest harness in
`source/<pkg>/harness.py` is owned by the project but ships with tests the
project keeps:

1. **Lookahead.** Scrambling data after time *t* leaves positions up to *t*
   unchanged. The harness applies the execution lag itself.
2. **Survivorship.** The universe comes from a point-in-time membership table.
3. **Costs.** Fees and slippage are required arguments with no zero default.
4. **Overfitting.** Walk-forward splits with an embargo; a locked final
   holdout that is evaluated only with an explicit flag, and each evaluation
   is logged; reports include the deflated Sharpe ratio from the trial count
   recorded in MLflow.
5. **Reproducibility.** Every run logs the commit, the lockfile hash, the data
   snapshot, parameters, and seed. Data snapshots are immutable and hashed.

Notebooks are marimo files (plain Python, diffable, checked by ruff), split
into `notebooks/explore/` and `notebooks/pipeline/`. Reusable logic belongs in
`source/`.

### Firm skills for the first release

Rewritten for the firm, not copied from public collections:

- `time-series-validity`: lookahead, leakage, point-in-time data,
  survivorship, purging and embargo, walk-forward validation.
- `backtest-evaluation`: costs, deflated Sharpe, overfitting, information
  coefficient, sensitivity.
- `firm-data`: which data sources exist, how to reach them, the standard
  variables, and the schema documentation.
- `hi`: the existing `hi skill`, for compute.

Each is under about 100 lines, names when not to use it, and points at the
firm's own libraries and documentation.

## Metadata and file ownership

`.hifin/template.json` records what generated the repository:

```json
{
  "template": "research",
  "layers": ["base", "python", "research"],
  "schema": 1,
  "release": "v1.0.0",
  "commit": "<templates commit>",
  "generatedBy": "hi 0.15.0",
  "params": {"name": "alpha-momentum"},
  "skills": {"release": "v1.0.0", "installed": ["time-series-validity", "firm-data", "hi"]},
  "files": {
    "CLAUDE.md": {"class": "owned", "sha256": "…"},
    "AGENTS.md": {"class": "managed", "sha256": "…"}
  }
}
```

It never contains absolute paths, usernames, hostnames, or credentials.

Every generated file belongs to one class:

- **Owned**: `hi` writes the whole file and may replace it. Agent
  configuration, shared CI workflows, and installed skills. The recorded hash
  shows whether anyone edited it.
- **Managed**: `hi` writes only a marked block; the rest belongs to the
  project. `AGENTS.md`, `.gitignore`, `Makefile`.
- **Seeded**: written once, then the project's. Source, tests, README,
  roadmap, `pyproject.toml`.

## Updating: `hi init --update`

1. Read `.hifin/template.json`; stop if it is missing or invalid.
2. Resolve the target release (newest, or `--ref`).
3. Apply migrations between the recorded and target schema: declarative
   renames, moves, and deletions of owned paths only.
4. Replace owned files whose hash still matches the record; replace managed
   blocks.
5. An owned file or managed block that was edited by hand is a conflict:
   print the difference, change nothing else in it, and suggest making the
   change in `hifinab/templates` instead. `--force` overwrites.
6. Never write seeded files. Instead, write the template's changes to them as
   `docs/upgrades/<release>.md`: the diff and plain instructions an agent can
   apply in a pull request.
7. Record the new release and hashes.

`hi init --update --check` changes nothing and exits non-zero when the
repository is behind or has conflicts, for CI. `hi repo doctor`
([idea](../ideas/hi_repo_doctor.md)) builds on the same records.

## Adopting an existing repository: `hi init --adopt <type>`

Most of the firm's repositories predate `hi init`. Adoption adds the base
layer's owned and managed files, the chosen type's skills and check targets,
and `.hifin/template.json`. It never moves or rewrites existing source, and an
existing file that an owned file would replace is a conflict listed before
any change. The plan says which checks the repository does not yet pass.

## The template repository's own CI

On every pull request and nightly:

1. Build `hi` from the pinned release and from `main`.
2. Generate every template with `hi init <type> --yes`.
3. Run each generated repository's `make check`.
4. Update a repository generated from the previous release to the new one
   with `hi init --update`, which tests migrations and ownership.

Renovate keeps dependency pins current in the template repository and opens
grouped pull requests per type.

## Safety

- Resolve and normalize the target before presenting the plan.
- Never delete existing content, and never overwrite a non-empty file except
  an owned file whose recorded hash matches, or with `--force`.
- If every planned file already matches, report that the template is already
  applied and succeed.
- List every conflicting path before running any command or writing any file.
- Write through temporary files inside the target and rename atomically.
- Print each command and file; never print environment values or tokens.
- Generate no credentials. `.env.example` holds names and safe examples only.
- Upload nothing and send no analytics.

## Output

Success ends with the template and release, the target, the commands run, the
files written, and the next commands: enter the directory, `make check`, and
start an agent.

## Acceptance criteria

1. The guided and direct forms produce identical plans.
2. Cancelling, or `--dry-run`, leaves the filesystem unchanged.
3. `hi init python` and `hi init research` produce repositories whose
   `make check` passes offline straight after generation.
4. `CLAUDE.md` imports `AGENTS.md`; skills are in `.agents/skills` with the
   `.claude/skills` link; `.env` is ignored and denied to Claude's file tools.
5. The research template's guard tests fail on a strategy that looks ahead,
   omits costs, or reads the holdout without the flag.
6. Conflicting files are listed before any command runs or file is written.
7. A repository that already matches is a successful no-op.
8. `hi init --update` replaces unedited owned files and managed blocks, never
   seeded files, and reports hand-edited ones as conflicts.
9. `hi init --adopt` adds files without moving or rewriting existing source.
10. Failures from `uv`, `gh`, or the network stop generation with the command
    that failed.
11. `.hifin/template.json` contains no machine-specific or secret data.
12. With no network and a cached release, generation succeeds; with neither,
    it fails with the command to run once online.

## Decisions required

1. **Backtest engine for `research`.** The firm's existing research library
   wraps LEAN, which is mature but runs C# in Docker. vectorbt is Python,
   vectorized, fast to iterate with, and familiar to agents; its license is
   Apache-2.0 with the Commons Clause (internal use is fine). Recommendation:
   vectorbt behind the harness for research, with LEAN as a later validation
   step.
2. **One data access path.** Projects reach firm data in two ways today, and
   each repository writes its own access code. The templates should ship one
   blessed helper and document it in the `firm-data` skill.
3. **Pipeline scheduling.** Use the firm's existing orchestrator, or start
   pipelines on systemd timers with a heartbeat and adopt an orchestrator
   later. Recommendation: the existing orchestrator, since it already runs.
4. **Web stack.** Recommendation above: FastAPI backend and a React single-page
   app on Vite, keeping all quant logic in Python. Settle when the first web
   project starts.

## Evidence

- Instruction files: ETH Zurich and LogicStar,
  [arXiv 2602.11988](https://arxiv.org/abs/2602.11988) (context files raised
  cost over 20% without improving success; only non-standard, repository-
  specific content helps); Anthropic,
  [best practices](https://code.claude.com/docs/en/best-practices) (give the
  agent a check it can run; enforce hard rules with hooks).
- `AGENTS.md` and Claude Code:
  [agents.md](https://agents.md/),
  [Claude Code memory](https://code.claude.com/docs/en/memory).
- Skills: [Agent Skills specification](https://agentskills.io/specification),
  [Claude Code skills](https://code.claude.com/docs/en/skills).
- Updating generated projects: Copier's
  [update model](https://copier.readthedocs.io/en/stable/updating/), Nx
  [sync generators](https://nx.dev/docs/concepts/sync-generators), and
  projen's owned files.
- Backtest validity: Bailey and López de Prado,
  [deflated Sharpe ratio](https://papers.ssrn.com/abstract=2460551) and
  [probability of backtest overfitting](https://papers.ssrn.com/abstract=2326253).

# `hi init` specification

Status: Approved (2026-10-01). Built-in templates and `hi init` shipped in
v0.16.0, server templates in v0.17.0; `--update`, `--check`, and `--adopt`
are planned for v0.18.0.

Dependencies: `uv` for Python templates and Node.js with npm for the `web`
template, both on `PATH`; for private templates, a device connected to
a `hi server` ([hi_server.md](hi_server.md#template-sources)).

## Goal

Every new repository starts in a shape that coding agents work well in, and
stays in that shape as the team's conventions improve. One command creates a
repository for any kind of project the team builds: quant research, model
training, data pipelines, services, web and mobile apps, and CLI tools.

The value is not the folders. It is three things no folder template gives:

1. **A check an agent can run.** Every repository answers `make check` with a
   non-zero exit on any formatting, lint, type, or test failure. Agents are
   told to run it before claiming work is done.
2. **Guardrails as code, not prose.** Rules that must never break (no
   credentials in the repository, no committed data, and for research no
   lookahead in a backtest) are tests, hooks, and deny rules that ship with
   the template, not paragraphs in `AGENTS.md`.
3. **Team knowledge where agents find it.** Private skills (data sources,
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
  CI work on the templates themselves. Parameters are sentinel names replaced
  at generation, not a templating language spread across files.
- **One default per type.** A template never asks which framework to use. The
  stack is decided once, in the template, and changed for everyone at once.
- **Public patterns are public; private knowledge is not.** Well-known project
  shapes ship inside `hi`. Anything quant-related or specific to the team
  lives only in a private repo and reaches devices through
  `hi server`.

## Where templates and skills live

Templates and skills come from **sources**. There are two kinds.

### Built-in: `templates/` in `hifinab/cli`

Generic, well-known project shapes (`python`, `web`, and later `service`,
`cli`, `ml`, `mobile`) live in the public `hi` repository under `templates/`
and are embedded in the binary. They need no network, no token, and no
connection, so `hi init python` works on any machine with `hi`.

- The built-in templates' version is the `hi` version. A template fix ships
  in a `hi` release, and `hi update` brings it to a device.
- Nothing private or team-specific may be added: no internal hostnames, data sources,
  vendors, schemas, or research methods. The repository's CI rejects known
  internal names (such as `.hi.fin` addresses) under `templates/`.
- A never-connected `hi` sees only the built-in templates.

### Server sources: private repositories behind `hi server`

Private templates and skills live in private git repositories that an admin
registers on the team's `hi server`:

```text
hi server templates add private https://github.com/hifinab/templates
```

The server holds the read access, mirrors the repository, and serves its
layers and skills to enrolled devices over the signed client API. Users never
need access to the repository on GitHub, and agents enrolled with
`hi connect --agent` get the same templates as people. How the server stores,
syncs, signs, and limits sources is in
[hi_server.md](hi_server.md#template-sources).

`hifinab/templates` is Hifin's private repo, registered as `private`. Everything quant-related is there
and only there: the `research` template and its backtest guards, private skills
such as `time-series-validity`, `backtest-evaluation`, and `data-access`, the
data-access helper, and the decisions behind them. This specification
covers the mechanism; that repository documents its own templates.

| Belongs in | Examples |
|---|---|
| Built-in (`hifinab/cli/templates/`) | Python library layout, React web app, CI, `make check`, agent configuration, secret scanning |
| Server source (`hifinab/templates`) | `research`, backtest harness and guard tests, private skills, data access, the team's orchestrator and infrastructure |

The GitHub "template repository" feature is not used: it has no parameters
and no way to update a repository later.

### Layout of a source

Built-in and server sources have the same layout:

```text
<layer>/layer.json       the layer's manifest
<layer>/...              the layer's files
skills/<name>/SKILL.md   skills in the Agent Skills format
```

Built-in layers today are `base` (hidden: never chosen on its own), `python`,
and `web`.

### Layers and names

A template is a chain of layers applied in order (`base`, then `python`, then
`research`); a later layer replaces an earlier layer's file. A file named
`<path>.fragment` is added to `<path>` instead: inside its managed block,
just before the `hi:end` marker, or at the end when the file has none. That
is how each layer adds its own ignore rules and `AGENTS.md` rules.

- A server layer may extend a built-in layer (`research` extends `python`) or
  another layer in the same source. A built-in layer never extends a server
  layer.
- Built-in names are reserved. A server source cannot replace a built-in
  layer; a server layer with a built-in name is listed as an error by
  `hi server templates sync` and not served.
- Names from different server sources may clash; a clashing name must be
  written `<source>/<name>`, such as `private/research`. A unique name needs no
  prefix.

### Layer manifest

Each layer has a `layer.json`. JSON, like every other `hi` file, so `hi`
needs no extra parser.

```json
{
  "name": "research",
  "summary": "Quant strategy research with backtesting",
  "extends": "python",
  "hidden": false,
  "schema": 1,
  "requires_hi": ">=0.17.0",
  "params": {"name": {"pattern": "^[a-z][a-z0-9-]*$"}},
  "substitute": {"hifin-template-name": "{{name}}", "hifin_template_name": "{{name_snake}}"},
  "files": {
    "owned": ["CLAUDE.md", ".claude/settings.json"],
    "managed": ["AGENTS.md", ".gitignore", "Makefile"]
  },
  "skills": ["time-series-validity", "data-access", "hi"],
  "commands": {
    "setup": [["uv", "sync"]],
    "check": [["make", "check"]]
  }
}
```

- `substitute` replaces sentinel names in text file contents and paths.
  Values may use `{{name}}`, `{{name_snake}}`, and `{{name_title}}`. The
  built-in `base` layer defines `hifin-template-name`, `hifin_template_name`,
  and `Hifin Template Name`, so every layer can use them; a layer's
  lockfiles are generated with the sentinel name and stay valid after
  substitution.
- Files not listed as owned or managed are seeded. Lists from every layer in
  the chain are merged.
- `skills` names skills from the layer's own source or the built-in source.
  The `hi` skill is always the one `hi skill` writes.
- `commands` are argument lists, shown in the plan; never shell strings. A
  later layer's commands replace an earlier layer's.
- `remove` drops files an earlier layer wrote, such as the `python` layer's
  example code that a `research` layer replaces.
- A layer that needs a newer `hi` than the device has is listed with the
  version to update to and cannot be chosen.

## Commands

```text
hi init                          guided: type, name, directory
hi init <type> [directory]       e.g. hi init python pricing-tools
hi init --list                   templates from every source, with source and status
hi init --update                 bring this repository up to the current templates
hi init --adopt <type>           bring an existing repository under a template
```

Options: `--name`, `--yes` (no confirmation; for agents and CI), `--dry-run`
(print the plan and stop), `--no-setup` (write files but run no commands),
and `--github <owner/repo>` to also create a private GitHub repository with
`gh repo create` and push the first commit.

Template names are reserved arguments, never project names. With no directory,
the target is `./<name>`; `.` targets the current directory.

In a new directory, `hi init` runs `git init` before the template's commands,
since the checks respect `.gitignore`. The guided form and the direct form
use the same planner. Before any change,
`hi` prints the template, each layer with its source and version (the `hi`
version, or the server source's commit), the target, every file it will
write, and every command it will run, and asks for confirmation.

## Every repository (the `base` layer)

```text
AGENTS.md                   managed block (team rules) + the project's own text
CLAUDE.md                   "@AGENTS.md"
.agents/skills/<name>/      skills, pinned; owned by hi
.claude/skills/<name> -> ../../.agents/skills/<name>
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
  `<!-- hi:end -->` holding the rules for this type (at most about 40 lines),
  followed by a short project section the team grows from observed agent
  mistakes. Claude Code reads it through `CLAUDE.md`'s `@AGENTS.md` import,
  which works on every version and surface; other agents read `AGENTS.md`
  directly. Each layer adds its own rules to the block.
- **Skills** are copied into `.agents/skills`, with a symlink per skill for
  Claude Code, which reads only `.claude/skills`. One link per skill, as
  `hi skill` writes them, so the two never fight over the folder. Copies, rather than a Claude plugin
  marketplace, because they work for every agent and in cloud sessions, where
  marketplace plugins do not load. Each copy carries the `hi` marker used by
  `hi skill`, its source and version are recorded in `.hifin/template.json`,
  and a template installs at most a handful, since every skill description
  costs context on every turn.
- **`make check`** runs, in order: lockfile check, format check, lint, type
  check, tests. It is offline and deterministic once setup has installed the
  dependencies. Make is used because the team already uses Makefiles and
  every machine has it.
- **Secrets** stay in `.env` (ignored) and the environment. `.claude/settings.json`
  denies reading `.env*` and `secrets/`; this is a guard for Claude's own
  tools, not a boundary against scripts, and `AGENTS.md` says so.

## Built-in template types

Stacks are defaults for the first release of each template. Versions are
pinned in the template's own manifests and lockfiles, not here.

| Type | Use for | Default stack | Adds to `make check` | Status |
|---|---|---|---|---|
| `python` | Libraries and scripts | Python, uv, ruff, pyrefly, pytest | — | v0.16.0 |
| `web` | Internal web apps and dashboards | React and TypeScript on Vite, npm, Biome, Vitest | type check, production build | v0.16.0 |
| `service` | Backend services and APIs | `python` + FastAPI, pydantic-settings, SQLAlchemy and Alembic, `/healthz` and `/readyz`, Dockerfile, compose | migrations match models; health endpoints answer | v0.18.0 |
| `ml` | Model training, fine-tuning, benchmarks | `python` + PyTorch (ROCm and CUDA extras), typed config, training image for `hi compute` | a two-step CPU training smoke test | v0.18.0 |
| `pipeline` | Data sources, scrapers, ingestion | `python` + one idempotent `run --date` entry point, raw landing before parsing | offline parser tests against recorded responses; a rerun loads nothing new | v0.18.0 |
| `cli` | Command-line tools | `python` + Typer, installed with `uv tool install` | exit codes and `--json` output | with the first project |
| `mobile` | Mobile apps | Expo (expo-router, EAS), Biome, Jest | `expo-doctor` | with the first project |

### `python`

A `src/` layout package built with `uv_build`, a `tests/` folder, and a
`py.typed` marker. `make check` runs `uv lock --check`, `ruff format
--check`, `ruff check`, `pyrefly check`, and `pytest`. Tools are dev
dependencies in `pyproject.toml`, so they are pinned by the lockfile and
`uv sync` is the only setup step.

### `web`

A single-page app: React and TypeScript on Vite, Biome for format and lint,
Vitest with Testing Library for tests. npm, because it ships with Node.js
and needs no extra install. Setup is `npm ci`; `make check` runs `npm ls`
(installed packages match the lockfile), `biome ci`, `tsc --noEmit`,
`vitest run`, and `vite build`. A backend, when the app needs one, comes from the `service`
template; quant logic stays in Python. Playwright is left out of the default
check because it downloads browsers; a project adds it when it has flows
worth testing end to end.

## Metadata and file ownership

`.hifin/template.json` records what generated the repository:

```json
{
  "template": "research",
  "schema": 1,
  "generatedBy": "hi 0.17.0",
  "layers": [
    {"name": "base", "source": "builtin", "version": "hi 0.17.0"},
    {"name": "python", "source": "builtin", "version": "hi 0.17.0"},
    {"name": "research", "source": "private", "commit": "<commit>"}
  ],
  "params": {"name": "alpha-momentum"},
  "skills": {
    "time-series-validity": {"source": "private", "commit": "<commit>"},
    "hi": {"source": "builtin", "version": "hi 0.17.0"}
  },
  "files": {
    "CLAUDE.md": {"class": "owned", "sha256": "…"},
    "AGENTS.md": {"class": "managed", "sha256": "…"}
  }
}
```

It never contains absolute paths, usernames, hostnames, server addresses, or
credentials. A server source is recorded by its name only.

Every generated file belongs to one class:

- **Owned**: `hi` writes the whole file and may replace it. Agent
  configuration, shared CI workflows, and installed skills. The recorded hash
  shows whether anyone edited it.
- **Managed**: `hi` writes only a marked block; the rest belongs to the
  project. `AGENTS.md`, `.gitignore`, `Makefile`.
- **Seeded**: written once, then the project's. Source, tests, README,
  roadmap, `pyproject.toml`, `package.json`.

## Server templates on the device

When the device is connected (`hi connect`), `hi init` also asks the server
for its catalog: each source's name, current commit, layers, and skills, as
far as the user's group may see them.

- `hi` downloads a source's bundle for a commit once, checks the server's
  signature against the server key pinned at `hi connect`, and caches it
  under `~/.cache/hi/templates/<source>/<commit>/`. A bundle that fails the
  check is deleted and nothing is generated.
- Offline, or with the server unreachable, `hi init` lists the built-in
  templates and the server templates in the cache, marked `cached`.
- After `hi disconnect`, server templates are no longer listed and the cache
  is deleted. Repositories already generated keep working; only `--update`
  of their server layers needs a connection again.
- Generating from a server template sends the server one activity event with
  the template name, like other client activity. No file names, parameters,
  or paths are sent.

## Updating: `hi init --update`

1. Read `.hifin/template.json`; stop if it is missing or invalid.
2. Resolve each layer's target: the running `hi` for built-in layers, the
   server source's current commit for server layers. A server layer whose
   source is unreachable keeps its recorded commit, and the output says so.
3. Apply migrations between the recorded and target schema: declarative
   renames, moves, and deletions of owned paths only, from
   `<layer>/migrations/<from>-to-<to>.json`.
4. Replace owned files whose hash still matches the record; replace managed
   blocks.
5. An owned file or managed block that was edited by hand is a conflict:
   print the difference, change nothing else in it, and suggest making the
   change in the template's source instead. `--force` overwrites.
6. Never write seeded files. Instead, write the template's changes to them as
   `docs/upgrades/<version>.md`: the diff and plain instructions an agent can
   apply in a pull request.
7. Record the new versions and hashes.

`hi init --update --check` changes nothing and exits non-zero when the
repository is behind or has conflicts, for CI. In CI, which has no server
connection, it checks the built-in layers against the installed `hi` and
reports server layers as `not checked (no server)` without failing;
`--strict` makes that a failure, for runs on a connected device.
`hi repo doctor` ([idea](../ideas/hi_repo_doctor.md)) builds on the same
records.

## Adopting an existing repository: `hi init --adopt <type>`

Most of the team's repositories predate `hi init`. Adoption adds the base
layer's owned and managed files, the chosen type's skills and check targets,
and `.hifin/template.json`. It never moves or rewrites existing source, and an
existing file that an owned file would replace is a conflict listed before
any change. The plan says which checks the repository does not yet pass.

## Testing templates

- **Built-in:** `hifinab/cli`'s CI generates every built-in template and
  runs each generated repository's setup and `make check`, on every change
  under `templates/` (`HI_TEMPLATE_CHECK=1 go test -run Template .`). It also
  runs the internal-name check.
  Renovate keeps the templates' pins current with grouped pull requests per
  type.
- **Server sources:** each source repository tests its own layers in its own
  CI by building `hi` from a pinned release and pointing it at the checkout
  with `HI_TEMPLATES_DIR=<path>`, which `hi init` treats as a local source
  named `local`. The same variable lets template authors try a change before
  it reaches the server.

## Safety

- Resolve and normalize the target before presenting the plan.
- Never delete existing content, and never overwrite a non-empty file except
  an owned file whose recorded hash matches, or with `--force`.
- If every planned file already matches, report that the template is already
  applied and succeed.
- List every conflicting path before running any command or writing any file.
- Write through temporary files inside the target and rename atomically.
- Print each command and file, and the source each comes from; never print
  environment values or tokens.
- Never run a command from a server bundle whose signature does not match the
  pinned server key.
- Generate no credentials. `.env.example` holds names and safe examples only.
- Upload nothing and send no analytics beyond the activity event above, and
  only to a server the device has connected to.

## Output

Success ends with the template and its sources, the target, the commands
run, the files written, and the next commands: enter the directory,
`make check`, and start an agent.

## Acceptance criteria

1. The guided and direct forms produce identical plans.
2. Cancelling, or `--dry-run`, leaves the filesystem unchanged.
3. `hi init python` and `hi init web` produce repositories whose `make check`
   passes straight after generation, with no connection to a server.
4. `CLAUDE.md` imports `AGENTS.md`; skills are in `.agents/skills` with the
   `.claude/skills` link; `.env` is ignored and denied to Claude's file tools.
5. A never-connected `hi` lists only built-in templates and makes no network
   call to list them.
6. On a connected device, server templates are listed with their source, and
   a server layer that extends a built-in layer generates one repository
   from both.
7. A bundle whose signature does not match the pinned server key is refused
   before any file is written or command run.
8. Conflicting files are listed before any command runs or file is written.
9. A repository that already matches is a successful no-op.
10. `hi init --update` replaces unedited owned files and managed blocks, never
    seeded files, and reports hand-edited ones as conflicts.
11. `hi init --update --check` in CI, with no server, checks built-in layers
    and does not fail on server layers unless `--strict` is given.
12. `hi init --adopt` adds files without moving or rewriting existing source.
13. Failures from `uv`, `npm`, `gh`, or the server stop generation with the
    command or request that failed.
14. `.hifin/template.json` contains no machine-specific or secret data.
15. The built-in templates contain no internal names, enforced in CI.

## Decisions required

1. **Template visibility per group.** Whether students see the private source
   at all. The server spec lets policy limit sources per group; the default
   chosen there is that every group sees every source.

Decisions about the private templates (backtest engine, data access,
pipeline scheduling) are tracked in `hifinab/templates`.

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

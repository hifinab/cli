# Roadmap

Working ideas for `hi`. These describe intended outcomes; exact stacks and generated
files should be chosen before implementation so templates remain small and
predictable.

## Project initialization

### `hi init`

Create a default repository for agent-driven development.

Initial shape:

- `README.md` for the project contract and entry points
- `AGENTS.md` for repository-specific agent instructions
- `docs/` for durable architecture and operational documentation
- `specs/` for scoped feature specifications and acceptance criteria
- `roadmap.md` for planned work and unresolved product decisions
- A minimal `.gitignore` and initialized Git repository when needed

The command should be safe to rerun, refuse to overwrite non-empty files, and
print every file it creates. Before implementation, decide whether it targets
the current directory only or accepts a separate project-directory argument.

### `hi init quant`

Create a quantitative-research repository that extends the default template.

Candidate additions:

- A Python project managed and locked with `uv`
- Separate `research/`, `src/`, `tests/`, `data/`, and `backtests/` areas
- Notebook support without making notebooks the source of production logic
- Reproducible experiment configuration, seeds, datasets, and result metadata
- Data and generated-result ignore rules
- Example strategy, backtest, and evaluation entry points
- A documented adapter boundary for market-data providers and brokers

Evaluate the backtesting frameworks we actually use before fixing the recipe.
Candidates include Backtesting.py, Backtrader, vectorbt, and LEAN. Prefer one
primary framework with optional adapters over installing several overlapping
frameworks by default.

### `hi init webapp`

Create a deterministic web-application repository that extends the default
template.

The recipe should pin the runtime and package manager, commit lockfiles, define
the application and test layout, include environment-variable examples, and
provide repeatable development, build, migration, and production commands. It
should also include the chosen database, migration tool, formatter, linter,
test runner, container setup, and CI checks.

Choose and document the Hifin web stack before implementation. The template
should encode one supported path rather than offer an interactive matrix of
framework combinations.

## Authentication

### `hi login`

Start GitHub authentication through the installed GitHub CLI:

1. Verify that `gh` is available.
2. Run `gh auth login` with the user's terminal attached.
3. Preserve the GitHub CLI's native browser, device-code, and host prompts.
4. Finish by reporting `gh auth status` without copying or storing credentials
   in `hi`.

### OMP and additional providers

OMP provider login is possible through its existing credential broker. A future
login surface can delegate directly to:

```text
omp auth-broker list
omp auth-broker login [provider]
```

Potential commands:

```text
hi login                 GitHub login through `gh auth login`
hi login github          Explicit GitHub login
hi login omp             Interactive OMP provider selection
hi login omp <provider>  Login to one OMP-supported provider
```

`hi` should never accept, print, or persist provider tokens itself. It should
only check dependencies and hand control to `gh` or OMP so those tools retain
ownership of credential storage, refresh, logout, and account selection.

## Proposed order

1. Define and implement the minimal default `hi init` file contract.
2. Select the quant stack and add `hi init quant`.
3. Select the web stack and add `hi init webapp`.
4. Add GitHub-backed `hi login`.
5. Add OMP provider discovery and delegated login.

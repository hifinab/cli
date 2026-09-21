# `hi init quant` specification

Status: Draft

Dependencies: `hi init py` planner, metadata, conflict detection, and `uv` setup.

## Goal

Create a reproducible quantitative-research repository without installing several
overlapping research or backtesting stacks by default.

## Command

```text
hi init quant [directory]
```

The interactive `hi init` wizard can select the same template. Both forms must
produce the same plan and files.

## Structure

The template extends the Python structure with:

```text
research/       Exploration and notebooks
backtests/      Strategy and evaluation entry points
source/         Reusable production logic
source/data/    Market-data provider adapters
source/brokers/ Broker/execution adapters
tests/          Deterministic behavioral tests
data/           Untracked local datasets
docs/specs/     Research and feature specifications
docs/roadmap.md Planned research and engineering milestones
```

Tracked purpose files preserve empty directories. Dataset, result, notebook
checkpoint, cache, and credential paths are ignored by default.

## Environment

Use `uv` for the project, virtual environment, dependency resolution, and lock
file. The selected recipe must pin one primary backtesting framework. Candidate
frameworks are Backtesting.py, Backtrader, vectorbt, and LEAN; implementation
must choose and document one before generating dependencies. Optional adapters
must be explicit additions rather than default duplicates.

The baseline should include tabular storage/query tools, notebook support, and a
test runner only when each has a defined repository role. Provider SDKs and
broker credentials are never installed or generated speculatively.

## Reproducibility

Experiments record configuration, random seeds, input dataset identity, time
range, fees, slippage, and result metadata. Notebook cells may explore ideas,
but reusable strategy, data, and evaluation logic belongs under `source/`.

## Safety

The template inherits the no-overwrite, preflight, cancellation, atomic-write,
and matching-rerun behavior from `hi_init.md`. It must never place API keys,
broker credentials, or downloaded market data in tracked files.

## Acceptance criteria

1. Direct and wizard invocation generate identical plans.
2. The result includes the base Python template and quant-specific structure.
3. `uv` produces a locked environment with one documented backtesting stack.
4. A generated example can run a deterministic backtest without network access.
5. Data, results, credentials, and notebook checkpoints are ignored.
6. Existing conflicting files stop the operation before mutation.

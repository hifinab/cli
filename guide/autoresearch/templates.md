---
title: Start from a template
description: hi init autoresearch-ml and hi init autoresearch-quant create projects already shaped for autoresearch rounds, with a fixed, tested evaluation, frozen data, a holdout outside the repository, and a box image that runs offline.
---

A loop is only as good as its evaluation. The autoresearch templates start you
from one that is fixed, tested, and hard to game, so you change the
experiment, not the plumbing.

```sh
hi init autoresearch-ml charlm         # train a better model in the same five minutes
hi init autoresearch-quant momentum    # find a better trading strategy, net of fees
```

| Template | Agents change | Scored by | The example |
|---|---|---|---|
| `autoresearch-ml` | `train.py` | `val_bpc` after five minutes of training, lower is better | a small GPT on Tiny Shakespeare, character by character |
| `autoresearch-quant` | `strategy.py` | Sharpe ratio net of fees, higher is better | Hybrid Asset Allocation (Keller and Keuning, 2023) on ten ETFs |

## What every research project has

| File | Role | During a run |
|---|---|---|
| `train.py` or `strategy.py` | the experiment | the only file agents change (`--edit`) |
| `evaluate.py` | the fixed evaluation; prints the score last | never changes |
| `prepare.py` | the data, its split, and constants such as the time budget | never changes |
| `eval/` | the frozen data the score is measured on, committed | never changes |
| `../<folder>-holdout/` | the data after the split, outside the repository | never seen by boxes |
| `program.md` | the task agents read each round | yours to write before |
| `.devcontainer/` | the box image: the locked dependencies, built once | rebuilt when `uv.lock` changes |
| `Makefile` | `data`, `score`, `holdout`, `loop`, `check`, and `results` (quant) | |

Three things make the evaluation trustworthy:

- **The editable file gets data only through its argument.** `evaluate.py`
  calls `train(data, …)` or `allocate(prices)` and refuses an editable file
  that opens files, imports modules outside a short list, or runs code
  dynamically. `autoresearch-quant` passes `allocate()` only the prices up to
  each month-end, so it can't look ahead; `autoresearch-ml` refuses a
  `train()` that runs past its time budget.
- **The data is frozen and committed.** `eval/` is in git (unlike `data/`),
  so every box scores the same thing, offline.
- **The holdout is out of reach.** `make data` writes the data after the
  split to a folder next to the project. Boxes only see the repository.

Each template's `make check` proves this on synthetic data: the backtest
matches an independent implementation, weights earn only the next month's
return, fees are charged on every trade, a uniform model scores exactly
log2 of the vocabulary, training past the budget fails, and every kind of
cheating the evaluation refuses is refused. hi's CI generates both templates
and runs their checks on every change.

## From template to running loop

The tutorial [Autoresearch: a trading strategy](/guide/tutorials/autoresearch-quant/)
walks through these steps with a real run and its output.

```sh
hi init autoresearch-quant momentum && cd momentum
make data                      # downloads the prices; the holdout goes to ../momentum-holdout
make score                     # the baseline
git add -A && git commit -m "Prices and baseline"
make loop                      # rounds in the background
hi agent best-of watch b-1
```

`hi init` installs the dependencies (`make sync` or `uv sync`). The loop
needs a commit: boxes start from it.

## Results

In `autoresearch-quant`, `make loop` ends with `make results`, and you can
run it yourself after a run that stopped early: it covers every round that
finished.

```sh
make results           # the newest run of this project
make results ID=b-2    # another run
make results HOLDOUT=none   # without the holdout
```

It re-runs every attempt from its files with your `evaluate.py`, in-sample
and on the holdout, checks each score against what hi recorded, and writes
`results/<run>/`:

| File | Holds |
|---|---|
| `report.html` | the report; open it from disk, no server needed |
| `attempts.parquet` | one row per attempt: agent, result, idea, and every metric in-sample and on the holdout |
| `returns.parquet` | each attempt's monthly returns, net and gross, with the fees and borrow between them, and the benchmark's (attempt -1) |
| `weights.parquet` | the weights each attempt held each month |
| `robust.parquet` | `make robust`'s checks, in-sample, for the baseline and every kept version |
| `regimes.parquet` | each month's market regime: volatility (two and three states) and trend |
| `capacity.parquet` | how much money the baseline and the best could run, from daily volume |
| `rounds.parquet`, `run.parquet` | each round, and the run with its limits |
| `strategies/` | `strategy.py` as each attempt left it |
| `run.json` | `hi agent best-of show --json`, as it was |

The report charts the score of every attempt with the best so far, the
growth of $1 and the drawdown of each round against the baseline and the
benchmark (`BENCHMARK` in `evaluate.py`), the weights held, returns by
regime, and returns by year (or the excess over the baseline or the
benchmark). At the top of the chart, a fees slider (0 to 5 times the
backtest's) and a date range recompute every chart and number. Below them: `make robust`'s checks for the baseline and the round
you pick, a decision log of every kept change with the checks before and
after, what helped and what isn't proven yet, how much money it could run
(a position at 1% of a day's dollar volume), and what the backtest charges
and doesn't. A page opened from disk can't read the files next to it,
so `make results` puts the Parquet tables and a small Parquet reader inside
`report.html`; the same tables are there for DuckDB, pandas, or polars:

```sh
duckdb -c "select round, agent, result, is_sharpe, ho_sharpe from 'results/b-1/attempts.parquet'"
```

`results/` stays out of git: it holds holdout scores, and boxes start from
commits, so agents never see them. Scoring every attempt on the holdout
spends it; read those columns as a check on the loop, not to pick a version.

## How much of the score is luck

In `autoresearch-quant`, a score is one backtest: one start month, one
rebalance day, one level of fees. `make robust` redraws it, in seconds, for
the current `strategy.py`:

| Check | What it asks |
|---|---|
| Start sets | a backtest from every month that leaves 3 years: how often is it ahead of the benchmark? |
| Tranches | the same rules rebalancing 5, 10 and 15 trading days early: how far does the Sharpe move with the day alone? |
| End windows | every 1, 3 and 5-year stretch: how often is it ahead? |
| Periods | three equal blocks of the period: does it hold in each? |
| Costs | the Sharpe at 0, 1, 2 and 5 times the fees, and the multiple at which the benchmark pulls ahead |
| Sensitivity | each numeric constant in capitals moved to its neighbours: is the chosen setting the top of its grid? |
| Placebo | 200 random portfolios of the same size: what share does the strategy beat? |
| Noise | a `--min-gain` for the loop |

On the run in the [tutorial](/guide/tutorials/autoresearch-quant/), it
showed what the holdout later confirmed: moving the rebalance day five
trading days earlier took the best version from 1.53 to 1.17, and its
`TOP = 4` was the top of its grid.

`SCORE` in `evaluate.py` picks what the loop improves. The default,
`sharpe`, rewards fitting one stretch of history; `worst_period`,
`median_start` and `tranches` must hold across stretches, start months or
rebalance days. `make score` prints all four, so a run's log shows them.
Agents can run `make robust` in their boxes too; `program.md` asks them to.

## Adapt it before a run

Change these first, then commit, then start the loop. Never during a run:
scores before and after wouldn't compare.

1. **The data.** In `prepare.py`: `TICKERS`, `START`, and `SPLIT`
   (`autoresearch-quant`), or `URL` and `SHA256` or your own `download()`
   (`autoresearch-ml`). Run `make data` again.
2. **The backtest and the score** (`autoresearch-quant`). In `evaluate.py`:
   `REBALANCE`, shorts and their limits, `FEES`, `BORROW`, `BENCHMARK`, and
   `SCORE`. Run `make robust` on the baseline for a `--min-gain`.
3. **The starting point.** Replace the example in `strategy.py` or
   `train.py` with yours. Agents improve what you give them.
4. **The metric.** In `evaluate.py`: the fee and risk-free rate, or the time
   budget in `prepare.py`. Keep the last printed line a number.
5. **The task.** `program.md` is what agents read every round: the goal,
   what's allowed, and what good judgement means here.
6. **The run.** The `loop` target in the `Makefile`: `BOXES`, `ROUNDS`, and
   `BUDGET` (`make loop BOXES=4 ROUNDS=50`), and the flags themselves.

Then run `make check`: the tests still hold for your changes, or tell you
what broke.

## The GPU (autoresearch-ml)

On a Strix Halo, `make sync` (run by `hi init`) sets the box image to AMD's
PyTorch build and gives boxes the GPU, in `.devcontainer/devcontainer.json`.
With the GPU, hi scores boxes one at a time, so the five-minute training
runs don't share it. Elsewhere boxes get the CPU build; a CPU run trains a
smaller model in the same five minutes, which is fine for trying the loop.

## Your own research project

Any project can run rounds; the templates are one way to get the pieces
right. You need:

- a score command whose last printed number is the score, the same for
  the same code;
- a file or folder agents may change (`--edit`), and an evaluation they
  can't;
- data the score needs, committed, or reachable from a locked box;
- a holdout outside the repository if the score stands in for something
  you can't measure in-sample;
- for speed, a box image with the dependencies (`devcontainer.json` with a
  Dockerfile that copies `uv.lock`; hi rebuilds it when the lock changes).

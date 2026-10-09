---
title: "Autoresearch: a trading strategy"
description: Step by step from an empty folder to a report - start a project from the autoresearch-quant template, download prices, score a baseline, let Claude Code and Codex improve the strategy in rounds, and check the result on years they never saw.
---

This tutorial follows one real run from an empty folder to a finished
report. Every command is shown as it was typed, with the output it printed.
At each step you'll see what it does, why, and what you could change before
running it.

The project is a monthly ETF rotation strategy. Agents improve its Sharpe
ratio in rounds, Karpathy's [autoresearch](https://github.com/karpathy/autoresearch)
loop: try an idea, measure it, keep it only if it's better, repeat. hi does
the measuring and the keeping, not the agents. See
[How it works](/guide/autoresearch/) for the idea.

**You need:**

- hi 0.35 or later (`hi update`), on a workstation with Podman (`hi install`
  sets it up) for the boxes the agents work in;
- Claude Code or Codex, signed in (Claude Code is enough);
- an internet connection for the price download.

**Time:** about an hour, most of it waiting for the run.
**Cost:** only the agents' tokens; this run's Claude Code boxes spent $15.43
(Codex reports only tokens, so its share isn't in that number).

| # | Step | Command | Costs |
|---|---|---|---|
| 1 | [Create the project](#1-create-the-project) | `hi init autoresearch-quant test2` | nothing |
| 2 | [Download the prices](#2-download-the-prices) | `make data` | nothing |
| 3 | [Score the baseline](#3-score-the-baseline) | `make score` | nothing |
| 4 | [Run the checks](#4-run-the-checks) | `make check` | nothing |
| 5 | [Commit the starting point](#5-commit-the-starting-point) | `git commit` | nothing |
| 6 | [Start the rounds](#6-start-the-rounds) | `hi agent best-of …` | agent tokens |
| 7 | [Read the results](#7-read-the-results) | `make results` (runs by itself) | nothing |
| 8 | [Check the holdout once](#8-check-the-holdout-once) | `make holdout` | nothing |

## 1. Create the project

**What and why.** `hi init` writes a project from a template. The
`autoresearch-quant` template is a project already shaped for autoresearch:
a fixed backtest that agents can't change or fool, one file they may
change, and a place outside the project for the years they must never see.

```sh
cd ~/projects
hi init autoresearch-quant test2
```

```text
$ git init -q
$ uv sync
Using CPython 3.14.5
Creating virtual environment at: .venv
Installed 12 packages in 26ms
 + numpy==2.5.3
 + pandas==3.0.6
 + pyarrow==25.0.1
 …
Created /home/hi/projects/test2 from the autoresearch-quant template (33 files).
```

**What it made.** The files that matter:

| File | Role |
|---|---|
| `strategy.py` | the rules: the only file agents may change |
| `evaluate.py` | the fixed backtest; prints the Sharpe ratio last |
| `prepare.py` | which prices, from when, and where the holdout starts |
| `program.md` | the task the agents read every round |
| `results.py`, `report/` | `make results`: every attempt re-run into a report |
| `Makefile` | `data`, `score`, `check`, `loop`, `results`, `holdout` |
| `tests/` | 32 tests that prove the backtest is honest |

The starting strategy is Hybrid Asset Allocation (HAA, Keller and Keuning
2023): when TIP's momentum turns negative, everything goes to the better of
cash (BIL) or bonds (IEF); otherwise the four ETFs with the best momentum
get 25% each.

**You could change:** the project name (`--name`), or add
`--github owner/repo` to also create a private GitHub repository.

## 2. Download the prices

**What and why.** The backtest needs daily prices. `make data` downloads
them and splits them in two: the years agents may see, inside the project,
and the years after `SPLIT`, outside it. Agents work in boxes that only
contain the project, so they can't see the later years. That makes those
years a fair test at the end.

```sh
cd test2
make data
```

```text
eval/prices.csv: 2007-06-01 to 2022-12-30
/home/hi/projects/test2-holdout/prices.csv: 2007-06-01 to 2026-09-30
```

| File | Period | Who sees it |
|---|---|---|
| `eval/prices.csv` | 2007-06 to 2022-12, 3,925 days | the loop and the agents |
| `../test2-holdout/prices.csv` | 2007-06 to 2026-09, 4,864 days | only you, at the end |

VEA has no prices for the first weeks: the fund started in July 2007. The
backtest refuses to buy a fund before it has a price.

**You could change** in `prepare.py`, before downloading:

| Setting | Here | Meaning |
|---|---|---|
| `TICKERS` | SPY IWM VEA VWO VNQ DBC IEF TLT BIL TIP | the ETFs the strategy can hold |
| `START` | 2007-06-01 | the first day of prices |
| `SPLIT` | 2022-12-31 | the last day agents see; later years are the holdout |

If you change `TICKERS`, change `strategy.py` to match: it names them.

## 3. Score the baseline

**What and why.** Before agents change anything, measure where you start.
`make score` runs the backtest: at each month's last trading day the
strategy sees only the prices up to that day and picks next month's
holdings. Every trade pays a 0.1% fee. The last line is the score the loop
will try to beat.

```sh
make score
```

```text
period:            2008-07 to 2022-12 (174 months)
cagr_pct:          9.37
ann_vol_pct:       9.21
max_drawdown_pct:  -9.32
turnover_per_year: 3.33
sharpe:            0.855089
```

| Line | Meaning |
|---|---|
| `period` | the scored months; the first 12 only build history |
| `cagr_pct` | average growth a year, after fees |
| `ann_vol_pct` | how much monthly returns swing, a year |
| `max_drawdown_pct` | the worst fall from a previous high |
| `turnover_per_year` | about how many times a year the whole portfolio changes |
| `sharpe` | (CAGR − 1.5%) / volatility: **the number the loop improves** |

**You could change**, now and never during a run: the starting rules in
`strategy.py`, and `FEE`, `RF` (the risk-free rate), or `WARMUP` in
`evaluate.py`.

## 4. Run the checks

**What and why.** `make check` runs the formatter, linter, type checker, and
tests. The tests prove the backtest can be trusted: the strategy only sees
past prices, fees are charged on every trade, the returns match an
independent calculation, and a strategy that tries to read files, import
other modules, or run hidden code is refused. Agents must keep it passing.

```sh
make check
```

```text
uv run --frozen ruff format --check
17 files already formatted
uv run --frozen ruff check
All checks passed!
uv run --frozen pyrefly check
 INFO 0 errors (2 suppressed, 2 warnings not shown)
uv run --frozen pytest -q
................................                                         [100%]
32 passed in 5.71s
```

Nothing to change here.

## 5. Commit the starting point

**What and why.** Every box starts from a commit, so the strategy, the
prices in `eval/`, and the task in `program.md` must be committed. The
holdout is outside the folder, and `results/` is ignored by git, so neither
can reach a box.

```sh
git add -A && git commit -m "Prices and baseline"
```

```text
8d78c1d Prices and baseline
```

**You could change** `program.md` first. It tells agents the goal (the
highest in-sample Sharpe from `make score`), what they may change (only
`strategy.py`: tickers, lookbacks, the risk-off rule, weights, cash), what
they may not do, and how to judge an idea: prefer rules with an economic
reason and few round numbers, because the result is checked on years they
never see. You can add your own limits, such as a maximum number of tunable
parameters.

## 6. Start the rounds

**What and why.** This is the loop. Each round starts boxes from the best
version so far; each agent reads `program.md` and a table of every earlier
attempt, tries an idea, and stops. Then hi, not the agent, runs `make score`
in each box. If the best box beats the best so far by enough, hi commits it
to the run's own branch, `best-of/<run>/best`. Otherwise the round leaves
no trace but its line in the history.

```sh
hi agent best-of 3 --agents claude,codex --rounds 20 --budget 20 --patience 10 \
  --min-gain 0.01 --max 20m --score "make score" --higher --edit strategy.py \
  --then "make results" program.md
```

`make loop` runs the same command with the template's defaults; this run
added `--agents claude,codex` and `--min-gain 0.01`.

| Option | Here | Meaning |
|---|---|---|
| `3` | 3 boxes a round | more boxes, more ideas a round, more cost |
| `--agents claude,codex` | taken in turn: Claude, Codex, Claude | different agents fail in different ways |
| `--rounds 20` | at most 20 rounds | |
| `--budget 20` | stop once agents report $20 | Codex reports only tokens, so only Claude's spend counts |
| `--patience 10` | stop after 10 rounds without a gain | |
| `--min-gain 0.01` | a gain under 0.01 Sharpe doesn't count | small gains are mostly noise fitted to the in-sample years |
| `--max 20m` | each agent gets 20 minutes a round | |
| `--edit strategy.py` | a box that changes any other file is thrown out | agents can't touch the backtest, the data, or the tests |
| `--then "make results"` | runs on your machine when the rounds end | step 7 |

```text
Box test2-b3-base: run in …/test2-b3-base/work (branch best-of/b-3/baseline), network locked.
Baseline: 0.855089, in 1s.
b-3 runs in the background.
  hi agent best-of watch b-3   follow it
  hi agent best-of show b-3    where it is
  hi agent best-of stop b-3    stop after the current round
```

hi measured the baseline again inside a box, on a locked network: the same
0.855089. The run is `b-3` because hi numbers runs across all your
projects. It goes on if you close the terminal; `hi agent best-of watch b-3`
shows it live, and `q` leaves the view without stopping it.

### What happened

The run went all 20 rounds in 48 minutes. You can follow it with `hi agent
best-of show b-3`; this is the list at the end, with each idea as the
winning agent summed it up:

| Round | Result | Sharpe | Kept idea |
|---|---|---|---|
| 1 | kept | 0.884 | go defensive when TIP's momentum is no better than cash's, instead of below zero |
| 2 | kept | 1.025 | drop the per-asset momentum filter: the canary alone decides |
| 3 | kept | 1.111 | rank ETFs by momentum divided by 6-month volatility |
| 4 | kept | 1.164 | 3-month volatility for the ranking, 12-month momentum for the defensive pick (Codex) |
| 5 | kept | 1.203 | two canaries, TIP and VWO; each one that fails sends half to defense |
| 6 | kept | 1.219 | canaries on a 12-month moving average (Faber's trend rule) |
| 7 | kept | 1.267 | VWO only signals; it is never held |
| 8–10 | unchanged | – | no box beat 1.267 by 0.01 |
| 11 | kept | 1.372 | the VWO canary as VWO's price relative to SPY |
| 12 | kept | 1.413 | IEF only as the defensive asset, never ranked with the offensive ones |
| 13 | unchanged | – | |
| 14 | kept | 1.464 | halve offense when SPY's 21-day volatility is 1.5 times its 1-year (Codex) |
| 15 | kept | 1.488 | measure that shock on all offensive ETFs, not SPY alone |
| 16 | kept | 1.530 | go fully defensive on a shock instead of halving |
| 17–20 | unchanged | – | no box beat 1.530 by 0.01 |

Twelve rounds kept a gain, ten by Claude Code and two by Codex. In the
other eight, agents found nothing worth 0.01 and most left the file as it
was. A round cost $0.47 to $1.08 of Claude time. `strategy.py` grew from 49
to 73 lines (+39, −15): every gain is one commit on `best-of/b-3/best`, so
`git log -p 8d78c1d..best-of/b-3/best` shows each change with its idea and
score.

## 7. Read the results

**What and why.** When the rounds end, for any reason, hi runs the
`--then` command on your machine. `make results` re-runs every attempt from
its saved files, kept or not, with the same `evaluate.py`: in-sample, on the
holdout, and month by month. It writes everything to `results/<run>/`. If a
run stops early, run `make results` yourself; it covers every finished
round.

The end of the run's log:

```text
2026-10-09 09:04:29: round 20 unchanged, score -,
2026-10-09 09:04:29: done: finished 20 rounds
2026-10-09 09:04:29: then: make results
results/b-3/report.html: 61 attempts
2026-10-09 09:04:36: then done
```

```text
results/b-3/
  report.html        the report; open it from disk, no server needed
  attempts.parquet   61 rows: every attempt, its idea, and every metric in-sample and on the holdout
  returns.parquet    each attempt's net monthly returns, and SPY's
  weights.parquet    what each attempt held each month
  rounds.parquet     20 rows, one per round
  run.parquet        the run: its settings, limits, and how it ended
  strategies/        strategy.py as each attempt left it
  run.json           hi agent best-of show b-3 --json
```

All 61 attempts re-ran to exactly the score hi recorded.

**[Open this run's report](/guide/tutorials/autoresearch-quant/results/b-3/report.html)**,
the same file `make results` wrote, served as it is. It has:

- the Sharpe of every attempt, with the best so far and that version's
  holdout Sharpe beneath it;
- the growth of $1 and the drawdown of each round against the baseline and
  SPY, one round at a time or all at once, with the holdout marked;
- one small chart per kept version, and returns by year for every version;
- facts computed from the tables, and the log of all 61 attempts.

![The report's progress chart: the in-sample Sharpe climbs from 0.855 to 1.530 while the same strategy's holdout Sharpe stays between 0.68 and 1.01](/guide/tutorials/autoresearch-quant/progress.png)

The tables open in anything that reads Parquet, for example DuckDB:

```sh
duckdb -c "select round, agent, result, is_sharpe, ho_sharpe
           from 'results/b-3/attempts.parquet' where version >= 0"
```

The [Parquet files](/guide/tutorials/autoresearch-quant/results/b-3/attempts.parquet)
of this run are next to the report.

**Why `results/` stays out of git.** It holds holdout scores, and boxes
start from commits; ignored files never reach them. Scoring every attempt
on the holdout also spends it: once you've read the holdout column, picking
a version by it fits the holdout. Read it as a check on the loop.

## 8. Check the holdout once

**What and why.** The in-sample score is what the loop optimized, so it
will look good whatever happens. The real test is the years after `SPLIT`,
which no agent saw. `make holdout` scores the best version on them. It runs
on your machine and refuses to run in a box.

```sh
git checkout best-of/b-3/best && make holdout && git checkout -
```

```text
period:            2023-01 to 2026-09 (45 months, holdout)
cagr_pct:          8.45
ann_vol_pct:       8.01
max_drawdown_pct:  -8.36
turnover_per_year: 3.17
sharpe:            0.868117
```

| | Baseline (round 0) | Best (round 16) | SPY, bought and held |
|---|---|---|---|
| Sharpe 2008–2022 (what the loop saw) | 0.855 | **1.530** | 0.523 |
| Sharpe 2023–2026 (the holdout) | 0.935 | **0.868** | 1.618 |
| CAGR on the holdout | 9.8% | 8.5% | 21.7% |
| Max drawdown on the holdout | −8.0% | −8.4% | −8.3% |

**What it means.** The loop raised the in-sample Sharpe by 79%, and the
holdout Sharpe fell a little. The rules fitted 2008–2022 (the 2008 crash,
2020, 2022's bond losses) better than they carried over to 2023–2026. The
report shows the same in every version: round 12 had the best holdout
(1.011); every later round was better in-sample and worse on the holdout.
And 2023–2026 was a strong bull market, which no defensive rotation keeps
up with: SPY alone did 21.7% a year.

Forty-five months is also short: a Sharpe measured on them has a standard
error of about 0.6, so 0.868 and 0.935 are not really different. The honest
summary is that this run found no strategy that is clearly better out of
sample, and `--min-gain 0.01` alone didn't prevent fitting.

That is a result, not a failure of the tool: hi kept the agents to one file
and a fixed backtest, measured every attempt itself, and kept the years
that tell the truth out of reach.

**To make the next run more robust**, change these before it starts, in a
new commit:

- a score that must hold in more than one period, for example the worse of
  the 2008–2015 and 2016–2022 Sharpe ratios, in `evaluate.py`;
- a complexity limit in `program.md`, such as at most eight tunable
  numbers;
- a new holdout: this one has been looked at, so the next clean test is
  the future. Paper-trade the version you choose from today.

## What to try next

- Change the task in `program.md`, the ETFs, or the starting strategy, and
  run again from a new commit.
- [Rounds that keep only gains](/guide/autoresearch/rounds/) has every
  option, and [Start from a template](/guide/autoresearch/templates/)
  describes `make results` and its files.
- [Advanced examples](/guide/autoresearch/advanced/): an overnight model
  training run on a GPU with `autoresearch-ml`.

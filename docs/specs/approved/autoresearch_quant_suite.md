# A research suite for `autoresearch-quant`

Status: approved by the user on 2026-10-09 ("add them in a detailed form to
the roadmap and start implementing all of these step by step").

## Why

The first real run in a user project (run b-3 in the
[tutorial](../../../guide/tutorials/autoresearch-quant.md)) raised the
in-sample Sharpe from 0.86 to 1.53 while the holdout Sharpe fell from 0.94
to 0.87. The template scores one backtest from one start date, at one cost,
with one rebalance day. Nothing in it tells a gain from luck.

The ideas come from a strategy decision record the user shared on
2026-10-09: a long/short Swedish equity strategy judged across 83 start
sets, at several costs, against placebos and nearby settings, with a
decision log and stated caveats. Its general lessons:

- Never judge on one start date: single starts differed by about 25 points.
- Check end dates too: report how often a strategy is ahead over 1, 3 and
  5-year windows.
- Fix rules before looking, then test the neighbourhood: expect the median
  of nearby settings, not the best one.
- Sensitivity grids and placebos show how much is luck.
- Test at realistic and harsh costs: rankings changed with costs, mostly
  through turnover.
- Use volatility regimes to set exposure; two states are enough to trade on.
- Say what isn't charged (dividends, borrow fees) and what isn't proven yet.

## What the template gets

Each part is a step on the roadmap, released together as v0.36.0.

### 1. A backtester that carries real strategies (`evaluate.py`)

- `REBALANCE`: `"M"` (each month's last trading day, today's behavior),
  `"W"` or `"D"`. Metrics annualize by the number of periods a year.
- `LONG_ONLY`, `MAX_GROSS` and `MAX_NET`: shorts when `LONG_ONLY` is false,
  with the sum of absolute weights at most `MAX_GROSS` and the sum of
  weights at most `MAX_NET`. Defaults keep today's rules (long only, at
  most 100% invested).
- `FEES`: a fee per ticker, defaulting to `FEE`. `BORROW`: a yearly fee on
  short positions, charged each period.
- `OFFSET`: rebalance this many trading days before the period's last
  trading day, for tranches (part 2).
- `simulate()` returns the whole path: net and gross returns, costs,
  turnover, borrow and the weights held. `backtest()` stays a thin wrapper.
- `BENCHMARK` moves from `results.py` to `evaluate.py`.
- Tests: the independent HAA implementation still matches with the
  defaults; a separate vectorized reference checks shorts, borrow, per-
  ticker fees and weekly rebalancing.

### 2. Robustness checks (`robust.py`, `make robust`)

`make robust` prints, for the current `strategy.py`, in seconds:

- **Start sets:** a backtest from every month of the in-sample period that
  leaves at least `MIN_YEARS` (3) to its end. The share of starts ahead of
  the benchmark (CAGR), and the median and worst Sharpe.
- **Tranches:** the backtest with the rebalance day moved 0, 5, 10 and 15
  trading days earlier, and the Sharpe of the four tranches held together.
  How far the Sharpe moves with the day alone.
- **End windows:** the share of rolling 1, 3 and 5-year windows in which the
  strategy beats the benchmark.
- **Periods:** the in-sample period in `PERIODS` (3) equal blocks, the
  Sharpe of each, and the worst.
- **Costs:** the Sharpe at 0, 1, 2 and 5 times the fees, and the break-even
  multiple at which the strategy's CAGR falls to the benchmark's.
- **Sensitivity:** every numeric constant in capitals in `strategy.py`
  (such as `TOP = 4`) moved to its neighbours (integers by 1 and 2, floats
  by 25% and 50%), the Sharpe at each, and whether the chosen setting sits
  at the top of its grid. The median of the neighbourhood is the number to
  expect.
- **Placebo:** 200 portfolios of random but persistent picks, with the
  strategy's median number of holdings and average invested share, redrawn
  one name at a time. The strategy's percentile among them.
- **Noise:** a suggested `--min-gain`: how far the Sharpe moves when only
  the rebalance day moves, rounded up to 0.01.

### 3. A score that must hold (`SCORE` in `evaluate.py`)

`SCORE` picks the number `make score` prints last and the loop improves:

- `"sharpe"`: the Sharpe of the in-sample period (the default, as today).
- `"worst_period"`: the lowest Sharpe of the `PERIODS` blocks.
- `"median_start"`: the median Sharpe of the start sets.
- `"tranches"`: the Sharpe of the four tranches held together.

Every score prints all four above its last line, so a run's log shows
them. The default stays `"sharpe"` so the tutorial's numbers hold; the
README and `program.md` say when to switch.

### 4. Regimes (`regimes.py`)

Causal labels from the benchmark's own prices, known at each period's end:

- **Volatility, 2 states:** the benchmark's 21-day volatility above or
  below its expanding median: calm or volatile.
- **Volatility, 3 states:** below the expanding 33rd percentile, between,
  or above.
- **Trend:** the benchmark above or below its 10-month average.

`results.py` writes each month's labels, and each version's returns by
state next to the benchmark's. No model with fitted states (HMM): the
template allows only numpy and pandas, and the record found two volatility
states enough.

### 5. Liquidity and capacity

`make data` also saves daily volume (`eval/volume.csv`, and the holdout's
next to its prices). `results.py` reports, per ticker the best version
held: its average weight, median daily dollar volume, and the fund size at
which its largest position reaches 1% of a day's dollar volume. Projects
made before v0.36.0 have no volume file and skip the table.

### 6. A report that records decisions (`report/report.html`)

- **Decision log:** each kept round: the idea, the in-sample change, the
  holdout change, and the robustness numbers before and after.
- **What helped, what didn't:** kept ideas ranked by their in-sample gain;
  discarded, unchanged and failed attempts listed with their ideas.
- **Cost slider:** 0 to 5 times the fees, recomputing every curve and
  number from gross returns and costs.
- **Date range:** a start and end month that rebase the curves and the
  numbers.
- **Weights:** the selected version's weights over time, as stacked areas.
- **Regime shading:** volatile months shaded behind the curves, and a table
  of returns by regime.
- **Robustness:** the start sets, end windows, periods, costs, sensitivity
  grid and placebo of the baseline and the best version.
- **Capacity:** the liquidity table, when volume exists.
- **Assumptions:** what the backtest charges and what it doesn't
  (dividends are in adjusted closes; cash earns 0%; borrow as set), from
  the run's own settings.

### 7. Docs and release

`program.md`, the README and the guide (`templates.md`, the tutorial's
next-run advice) describe the checks; v0.36.0 is released.

## Not in scope

- Hidden models or fitted regime models, which need libraries boxes don't
  allow.
- Intraday data, and data sources beyond Yahoo Finance (still on the
  roadmap).
- The same suite for `autoresearch-ml` (seeds and cross-validation stay a
  roadmap item).

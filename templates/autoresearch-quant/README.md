# Hifin Template Name

Autoresearch on a trading strategy. Agents change `strategy.py`; a fixed
backtest scores it; `hi agent best-of` keeps a change only when the score
beats the best so far, and commits it.

| File | |
|---|---|
| `strategy.py` | the rules; the only file agents change during a run |
| `evaluate.py` | the fixed backtest: no lookahead, fees; prints its four scores, `SCORE` last |
| `robust.py` | `make robust`: how much of the score is luck |
| `regimes.py` | market regimes for the report: volatility and trend |
| `prepare.py` | which prices, from when, and where the holdout starts |
| `eval/prices.csv` | the frozen in-sample prices (`make data` writes them) |
| `program.md` | the task the agents get |
| `results.py`, `report/` | `make results`: every attempt re-run into `results/<run>/`, with `report.html` |

```sh
make data       # download the prices, keep the holdout outside the repo
make score      # the in-sample Sharpe of the example strategy
make robust     # start sets, rebalance days, costs, neighbours, placebo
git add -A && git commit -m "Prices and baseline"
make loop       # rounds in the background; hi agent best-of watch b-1
make results    # every attempt re-run: results/<run>/report.html (make loop runs it at the end)
make holdout    # once, at the end, on the gains' branch
```

## Before a run

- **Data:** `TICKERS`, `START`, and `SPLIT` in `prepare.py`. Prices after
  `SPLIT` go only to `../<folder>-holdout/`, outside the repository, so no
  agent sees them.
- **Strategy:** replace the example (HAA, Keller and Keuning 2023) in
  `strategy.py` with the rules you start from.
- **Backtest:** in `evaluate.py`, `REBALANCE` (`M`, `W` or `D`), shorts
  (`LONG_ONLY`, `MAX_GROSS`, `MAX_NET`, `BORROW`), fees (`FEE`, and `FEES`
  per ticker), `RF`, and `BENCHMARK`. The defaults: monthly, long only,
  0.1% fees.
- **Score:** `SCORE` is what the loop improves. `sharpe` (the default) is
  one backtest from one start; `worst_period` (the lowest of three equal
  periods), `median_start` (the median over start months) and `tranches`
  (four rebalance days held together) must hold across stretches, starts
  or days, so they are harder to fit. `make score` prints all four.
- **Noise:** `make robust` on the baseline suggests a `--min-gain`: how far
  the Sharpe moves when only the rebalance day moves. Change it, and
  everything above, now; never during a run.
- **Task:** `program.md` is what the agents read each round.
- **Run:** the `loop` target in the `Makefile` holds the flags: boxes per
  round, rounds, budget, `--edit strategy.py`.

## After a run

`make loop` ends with `make results`; after a run that stopped early, run it
yourself. `results/<run>/report.html` opens from disk and shows every
attempt, the growth and drawdown of each round against the baseline and
SPY, the weights held, returns by regime and by year, `make robust`'s checks
for the baseline and each kept round, a decision log, what helped and what
isn't proven, how much money it could run, and the backtest's assumptions.
A fees slider and a date range recompute the charts. The numbers are in
Parquet files next to it.
`results/` stays out of git: it holds every attempt's holdout score, and
boxes start from commits, so agents never see it.

The gains are commits on `best-of/<run>/best`. Check them on the holdout
before you merge:

```sh
git checkout best-of/b-1/best && make holdout && git checkout -
git merge best-of/b-1/best
```

A higher in-sample Sharpe with a lower holdout Sharpe is overfitting. Each
look at the holdout spends some of its value; look once.

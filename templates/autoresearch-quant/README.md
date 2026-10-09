# Hifin Template Name

Autoresearch on a trading strategy. Agents change `strategy.py`; a fixed
backtest scores it; `hi agent best-of` keeps a change only when the score
beats the best so far, and commits it.

| File | |
|---|---|
| `strategy.py` | the rules; the only file agents change during a run |
| `evaluate.py` | the fixed backtest: monthly, no lookahead, fees; prints `sharpe:` last |
| `prepare.py` | which prices, from when, and where the holdout starts |
| `eval/prices.csv` | the frozen in-sample prices (`make data` writes them) |
| `program.md` | the task the agents get |
| `results.py`, `report/` | `make results`: every attempt re-run into `results/<run>/`, with `report.html` |

```sh
make data       # download the prices, keep the holdout outside the repo
make score      # the in-sample Sharpe of the example strategy
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
- **Metric:** `evaluate.py` scores Sharpe net of 0.1% fees. Change `FEE`,
  `RF`, or the metric now; never during a run.
- **Task:** `program.md` is what the agents read each round.
- **Run:** the `loop` target in the `Makefile` holds the flags: boxes per
  round, rounds, budget, `--edit strategy.py`.

## After a run

`make loop` ends with `make results`; after a run that stopped early, run it
yourself. `results/<run>/report.html` opens from disk and shows every
attempt, the growth and drawdown of each round against the baseline and
SPY, and returns by year. The numbers are in Parquet files next to it.
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

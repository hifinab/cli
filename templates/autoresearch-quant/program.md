# Improve the strategy

You are improving a monthly asset allocation strategy. The current rules
are in `strategy.py`.

**The goal is the highest Sharpe ratio** that `make score` prints on its
last line: the in-sample period in `eval/prices.csv`, net of fees. It takes
seconds; run it as often as you like.

## What you can change

`strategy.py`, and nothing else. Everything inside `allocate()` is fair
game: which tickers it uses, the momentum filter and its lookbacks, the
risk-off rule, how many assets it holds, how it weights them (equal, inverse
volatility, by momentum), volatility targeting, holding cash.

## What you can't do

- Change `evaluate.py`, `prepare.py`, or anything in `eval/`. A result that
  changes any file but `strategy.py` is thrown away.
- Use any data but `allocate()`'s argument: the daily closes up to the
  month's last trading day. No files, no network. `evaluate.py` allows only
  numpy, pandas, math, statistics, functools, itertools, collections,
  dataclasses, and typing, and refuses anything else.
- Go short, invest more than 100%, or trade tickers outside the data.

## Judgement

**The result is checked on later years you never see.** A rule with an
economic reason (trend, risk parity, a crash signal) carries over; a
constant tuned to the second decimal to fit the in-sample years doesn't, and
will do worse on the later data than the original rules. Prefer few, round
parameters (3, 6, 12 months; 4 assets) over fitted ones.

**Simpler is better.** A small gain that adds a lot of code isn't worth
it; the same Sharpe with fewer rules is a gain. Turnover costs real money in
the score: 0.1% of every trade.

Try one idea, or a few that belong together, and say in one line what you
tried.

---
title: Advanced examples
description: Longer hi agent best-of runs - overnight model training on a Strix Halo from the autoresearch-ml template, a trading strategy with a holdout from autoresearch-quant, a safe speed-up with a noisy benchmark, and extending a run.
---

These start from the [autoresearch templates](/guide/autoresearch/templates/)
or follow their pattern: a fixed evaluation, one file agents may change,
and a holdout outside the repository.

## Train a better model overnight on a Strix Halo

The aim: the lowest validation loss after exactly five minutes of training,
improved by agents all night, on the workstation's GPU.

```sh
hi init autoresearch-ml charlm && cd charlm
```

`hi init` runs `make sync`, which on a Strix Halo installs AMD's PyTorch
build and sets the box image to it, with the GPU.

**Adapt it.** To train on your own text, change `URL` and `SHA256` in
`prepare.py` (or write your own `download()`), and put the model you start
from in `train.py`. Then:

```sh
make data          # eval/train.txt and eval/val.txt; the last 10% goes to ../charlm-holdout
make score         # the baseline: five minutes of training, then val_bpc
git add -A && git commit -m "Data and baseline"
```

Run `make score` twice. The difference is the noise; use it as the gain hi
should ignore.

**Run it.** Edit the `loop` target, or pass the flags yourself:

```sh
hi agent best-of 3 --agents claude,codex --for 9h --budget 60 --patience 15 \
  --min-gain 0.005 --max 30m --score "make score" --lower --edit train.py program.md
```

- Three agents work at once; then hi scores the three boxes one after
  another on the GPU, five minutes each, so the clock is fair.
- A round takes the agents' time plus about 18 minutes of scoring; nine
  hours is about 15 to 20 rounds.
- `--patience 15` ends a run that has stopped improving; `--budget 60`
  caps the spend.

```sh
hi agent best-of watch b-1    # q leaves; the run goes on through the night
```

**In the morning:**

```sh
hi agent best-of show b-1
git log --oneline main..best-of/b-1/best    # one commit per gain, with its idea and score
git checkout best-of/b-1/best && make holdout && git checkout -
```

If the holdout score improved about as much as the validation score, merge.
If validation improved and the holdout didn't, the gains fit the validation
text; keep only the early commits, or none.

## A trading strategy with a holdout

The aim: a better monthly allocation strategy, judged net of fees, without
fitting the backtest.

```sh
hi init autoresearch-quant rotation && cd rotation
```

**Adapt it.** In `prepare.py`, set `TICKERS` to your universe and `SPLIT`
to the last day agents may see; everything after it stays in
`../rotation-holdout/`. Put your starting rules in `strategy.py`, and in
`program.md` say which kinds of ideas are welcome (for example "no more than
five parameters").

```sh
make data && make score && make check
git add -A && git commit -m "Universe, rules, and baseline"
```

**Run it** with mixed agents and a cap on the spend:

```sh
make loop BOXES=4 ROUNDS=30 BUDGET=40
hi agent best-of watch b-1
```

The evaluation passes `allocate()` only the prices up to each month-end,
refuses code that reads files or imports anything but numpy, pandas, and a
few standard modules, and charges 0.1% of every trade. So a gain is a better
rule, not a peek at the future or a cheaper trade.

**Check it once:**

```sh
git checkout best-of/b-1/best && make holdout && git checkout -
```

Compare it with the holdout score of your starting rules, measured once
before the run. Every look at the holdout spends some of its value; if you
change the strategy because of what it showed, it's no longer a holdout.

## A speed-up with a noisy benchmark

The aim: faster code, the same results, in a project that isn't a template.

1. **A score that's stable.** Make `make bench` print the median of several
   runs, and run it ten times. If it moves by 3%, a gain under 3% is noise.
   `--min-gain` is in the score's own units: 3% of a 0.7-second baseline is
   about 0.02.
2. **A gate that's strict.** `make check` with the tests that pin the
   output, so a faster wrong answer fails before it is scored.
3. **A fence.** `--edit src/`: results that change the benchmark, the tests,
   or the `Makefile` are discarded.

```sh
hi agent best-of 3 --agents claude,codex --rounds 12 --budget 25 \
  --check "make check" --score "make bench" --lower --min-gain 0.02 \
  --edit src/ "make the backtest engine faster; results must stay byte-identical"
```

Benchmarks with a fixed workload don't overfit the way models do, so no
holdout is needed; the check is what keeps results right.

## Extend, stop, and start again

```sh
hi agent best-of stop b-1                 # finish this round, then stop
hi agent best-of resume b-1 --rounds 10   # ten more, from the best so far
hi agent best-of resume b-1 --for 4h --budget 20
hi agent best-of stop b-1 --now           # stop at once; the round's boxes are removed
```

A resumed run keeps its gains' branch, its history, and its spend; agents
still see every earlier attempt. To start over from what a run found, merge
its branch and start a new run: its baseline is the merged result.

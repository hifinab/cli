# Train a better model in the same time

You are improving how a model trains. The model and its training are in
`train.py`.

**The goal is the lowest val_bpc** that `make score` prints on its last
line: bits per character on validation text the model never trains on.
Every score trains for exactly the same time (`prepare.TIME_BUDGET`, 5
minutes), so a better score means a better model or better training, not a
longer run. A `train()` that runs much past the budget fails.

## What you can change

`train.py`, and nothing else. Everything is fair game: the architecture,
its size, the optimizer, the learning rate and its schedule, the batch size,
how batches are drawn, mixed precision, `torch.compile`.

## What you can't do

- Change `prepare.py`, `evaluate.py`, or anything in `eval/`. A result that
  changes any file but `train.py` is thrown away.
- Read any data but `train()`'s argument: no files, no network, no
  pretrained weights. `evaluate.py` allows only torch, math, time,
  dataclasses, functools, itertools, collections, and typing.
- Install packages.

## Judgement

**Simpler is better.** A tiny gain that adds twenty lines of tricks isn't
worth it; the same score with less code is a gain. Memory is a soft limit:
some more is fine for a real gain.

`make score` takes about six minutes; while you work, a shorter run tells
you whether an idea trains at all. Try one idea, or a few that belong
together, and say in one line what you tried.

---
title: Start from a template
description: hi init autoresearch-ml creates a project already shaped for autoresearch rounds, with a fixed, tested evaluation, frozen data, a holdout outside the repository, and a box image that runs offline.
---

A loop is only as good as its evaluation. The autoresearch template starts
you from one that is fixed, tested, and hard to game, so you change the
experiment, not the plumbing.

```sh
hi init autoresearch-ml charlm         # train a better model in the same five minutes
```

| Template | Agents change | Scored by | The example |
|---|---|---|---|
| `autoresearch-ml` | `train.py` | `val_bpc` after five minutes of training, lower is better | a small GPT on Tiny Shakespeare, character by character |

On a device connected to your team's `hi server`, `hi init` may list more
research templates from the team's private repo, marked `(private repo)`.
They are built on the same base layer, `autoresearch-base`.

## What every research project has

| File | Role | During a run |
|---|---|---|
| `train.py` | the experiment | the only file agents change (`--edit`) |
| `evaluate.py` | the fixed evaluation; prints the score last | never changes |
| `prepare.py` | the data, its split, and constants such as the time budget | never changes |
| `eval/` | the frozen data the score is measured on, committed | never changes |
| `../<folder>-holdout/` | the data after the split, outside the repository | never seen by boxes |
| `program.md` | the task agents read each round | yours to write before |
| `.devcontainer/` | the box image: the locked dependencies, built once | rebuilt when `uv.lock` changes |
| `Makefile` | `data`, `score`, `holdout`, `loop`, and `check` | |

Three things make the evaluation trustworthy:

- **The editable file gets data only through its argument.** `evaluate.py`
  calls `train(data, …)` and refuses an editable file that opens files,
  imports modules outside a short list, or runs code dynamically, and a
  `train()` that runs past its time budget.
- **The data is frozen and committed.** `eval/` is in git (unlike `data/`),
  so every box scores the same thing, offline.
- **The holdout is out of reach.** `make data` writes the data after the
  split to a folder next to the project. Boxes only see the repository.

`make check` proves this on synthetic data: a uniform model scores exactly
log2 of the vocabulary, training past the budget fails, and every kind of
cheating the evaluation refuses is refused. hi's CI generates the template
and runs its checks on every change.

## From template to running loop

```sh
hi init autoresearch-ml charlm && cd charlm
make data                      # downloads the text; the holdout goes to ../charlm-holdout
make score                     # the baseline
git add -A && git commit -m "Data and baseline"
make loop                      # rounds in the background
hi agent best-of watch b-1
```

`hi init` installs the dependencies (`make sync` or `uv sync`). The loop
needs a commit: boxes start from it.

## Adapt it before a run

Change these first, then commit, then start the loop. Never during a run:
scores before and after wouldn't compare.

1. **The data.** In `prepare.py`: `URL` and `SHA256`, or your own
   `download()`. Run `make data` again.
2. **The starting point.** Replace the example in `train.py` with yours.
   Agents improve what you give them.
3. **The metric.** In `evaluate.py`, or the time budget in `prepare.py`.
   Keep the last printed line a number.
4. **The task.** `program.md` is what agents read every round: the goal,
   what's allowed, and what good judgement means here.
5. **The run.** The `loop` target in the `Makefile`: `BOXES`, `ROUNDS`, and
   `BUDGET` (`make loop BOXES=4 ROUNDS=50`), and the flags themselves.

Then run `make check`: the tests still hold for your changes, or tell you
what broke.

## The GPU

On a Strix Halo, `make sync` (run by `hi init`) sets the box image to AMD's
PyTorch build and gives boxes the GPU, in `.devcontainer/devcontainer.json`.
With the GPU, hi scores boxes one at a time, so the five-minute training
runs don't share it. Elsewhere boxes get the CPU build; a CPU run trains a
smaller model in the same five minutes, which is fine for trying the loop.

## Your own research project

Any project can run rounds; the template is one way to get the pieces
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

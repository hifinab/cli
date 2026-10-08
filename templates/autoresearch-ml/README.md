# Hifin Template Name

Autoresearch on model training, after Karpathy's
[autoresearch](https://github.com/karpathy/autoresearch). Agents change
`train.py`; every attempt trains for the same five minutes and is scored on
text it never saw; `hi agent best-of` keeps a change only when the score
beats the best so far, and commits it.

| File | |
|---|---|
| `train.py` | the model and its training; the only file agents change during a run |
| `evaluate.py` | trains for the time budget, refuses longer runs, prints `val_bpc:` last |
| `prepare.py` | the text, its split, the time budget, and the measurement |
| `eval/` | the frozen training and validation text (`make data` writes it) |
| `program.md` | the task the agents get |

```sh
make data       # download the text, keep the holdout outside the repo
make score      # five minutes of training, then the baseline val_bpc
git add -A && git commit -m "Data and baseline"
make loop       # rounds in the background; hi agent best-of watch b-1
make holdout    # once, at the end, on the gains' branch
```

## Before a run

- **Data:** `URL` and `SHA256` in `prepare.py`, or your own `download()`.
  The example is Tiny Shakespeare, character by character. The last 10%
  goes only to `../<folder>-holdout/`, outside the repository.
- **Budget:** `TIME_BUDGET` in `prepare.py`, five minutes. Shorter means
  more rounds a night and noisier scores.
- **Model:** the example in `train.py` is a small GPT; start from yours.
- **GPU:** on a Strix Halo, `make sync` (run by `hi init`) gives boxes
  AMD's PyTorch build and the GPU, in `.devcontainer/devcontainer.json`.
  With the GPU, hi scores boxes one at a time, so they don't share it while
  the clock runs.
- **Run:** the `loop` target in the `Makefile` holds the flags: boxes per
  round, rounds, budget, `--edit train.py`.

## After a run

The gains are commits on `best-of/<run>/best`:

```sh
git checkout best-of/b-1/best && make holdout && git checkout -
git merge best-of/b-1/best
```

Scores move a little from run to run, as training does; a gain smaller than
that noise isn't one. `--min-gain` in the `loop` target tells hi to ignore
such gains.

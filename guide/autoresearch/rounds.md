---
title: Rounds that keep only gains
description: Give hi agent best-of a score, and it runs rounds that each start from the best result so far, scores every attempt itself, and commits only the ones that beat it. Limits, watching, stopping, the GPU, and overfitting.
---

With `--score`, best-of runs rounds, in the style of Karpathy's
[autoresearch](https://github.com/karpathy/autoresearch). Each round starts
n boxes from the best result so far. hi scores every box itself, and commits
the round's best only if it beats the best so far. The next round starts
from there.

```sh
hi agent best-of 4 --rounds 100 --gpu --edit train.py \
  --score "make score" --lower program.md
```

```text
Best-of b-7 · "program.md" · 4 boxes a round · round 9 of 100 · 2h41m · $31.20 at API prices
Score: make score (lower is better)
Best: 0.9871 from round 6, baseline 0.9979 · on best-of/b-7/best

  ROUND  RESULT     SCORE   BOX           IDEA
  6      kept       0.9871  ml-b7-r6-2    raise the learning rate to 0.04
  7      discarded  0.9951  ml-b7-r7-4    switch to GeLU
  8      unchanged  –       –             no box changed anything

Now: round 9: scoring ml-b7-r9-2 (2 of 4)
```

The quickest way to a project that's ready for this is a
[template](/guide/autoresearch/templates/).

## The score

`--score "<command>"` is any command that prints a number. hi runs it in
each box after the agent ends and takes **the last number it prints**.
`--lower` or `--higher` says which way is better; one is required.

- A score that fails, times out, or prints no number makes that box a
  crash: it can't be kept. A round is `kept`, `discarded` (its best didn't
  beat the best so far), `unchanged` (no box changed anything), or `crash`
  (no box had a usable result).
- `--check "<command>"` still works as a gate: a box that fails it isn't
  scored. With `--score` there is no check unless you give one.
- The score runs under a time limit of twice the baseline's time plus a
  minute, so a hung run ends.
- The score should be the same every time for the same code. Measure its
  noise first (run it a few times); `--min-gain 0.002` makes hi ignore gains
  smaller than that.

## The baseline

Before any agent starts, hi runs the check and the score on your last
commit, in a box with the run's options. A score that fails or prints no
number stops the run there, before it costs tokens.

```text
Measuring the baseline on 3f2a91c0e4b1.
Baseline: 0.9979, in 5m12s.
```

The baseline runs in a box, like every attempt, so a score that depends on
speed (the best loss in five minutes of training) compares fairly within a
run. `make score` on your own machine can differ: in a test, the same
training scored 2.56 on the host and 2.68 in a box, which gets less of the
CPU.

## Only hi commits

The run has a branch for its gains, `best-of/<run>/best`, that only hi
writes. In their boxes, agents may commit, experiment, and run the score as
often as they like; that is scratch. When a round's best beats the best so
far, hi adds one commit to the gains' branch:

```text
best-of b-7 round 6: raise the learning rate to 0.04

Score 0.9871, was 0.9932. From ml-b7-r6-2 (claude), checked and scored by hi.
```

The commit holds the agent's files as it left them, snapshotted before the
check and score ran, so a `run.log` the score writes isn't in it. Every
round's boxes and scratch branches are removed when the round ends.

## What agents see

Each round's task is yours plus what hi adds: the round, the score command
and its direction, the best score so far, the check, the files they may
change, and a table of every earlier attempt with its score, result, and
one-line idea. Agents build on what worked and skip what didn't.

```text
Earlier attempts, oldest first:

round  agent   score   result     idea
0      -       0.9979  baseline   your last commit
1      claude  0.9932  kept       raise the learning rate to 0.04
1      codex   1.0041  discarded  switch to GeLU
```

## Keeping agents to the experiment

`--edit train.py` (files, folders, or globs, comma-separated) is the only
part of the project a result may change. A result that changes anything
else, such as the evaluation or its data, is disqualified, whatever its
score. This enforces what autoresearch only asks of the agent.

The [templates](/guide/autoresearch/templates/) go further: their
evaluation refuses code that reads files or imports modules outside a short
list, so the editable file can't read the data it is scored on.

## Limits

| Option | Ends the run |
|---|---|
| `--rounds 50` | after 50 rounds (default 1; `forever` has no end) |
| `--for 8h` | no round starts after 8 hours |
| `--budget 30` | no round starts once agents have used $30 at API prices, as Claude Code reports it; on a subscription that is a measure of usage, not a bill, and Codex reports no dollars |
| `--patience 10` | after 10 rounds without a gain |
| `--max 30m` | not the run: a time limit for each agent in each round |

`--budget` counts dollars agents report: Claude Code's cost at API prices,
and Hermes' when it has one. Codex reports only tokens, so its runs count as
nothing. After the first round, the view shows what a round costs.

## Watching and stopping

The rounds run in a process of their own, so they go on when you close the
terminal or the SSH session.

```sh
hi agent best-of watch b-7         # the view; q, Esc, or Ctrl+C leave it
hi agent best-of stop b-7          # stop after the current round
hi agent best-of stop b-7 --now    # stop now and remove the round's boxes
hi agent best-of resume b-7 --rounds 20   # 20 more rounds from the best so far
hi agent best-of show b-7 --json   # everything, for scripts and agents
```

Leaving the view never stops the run; in the view, `s` stops it after the
current round. On `resume`, `--rounds`, `--budget`, and `--for` count from
where the run is. After a reboot, `resume` picks up a round whose boxes
still exist.

## One GPU, one score at a time

A score with a time budget, such as "the best loss after five minutes of
training", means nothing if boxes share the GPU while they train: each
gets part of it. With `--gpu`, or `"gpu": true` in the project's
`devcontainer.json`, agents work at once, but hi gives the boxes the turn
to be scored one at a time, after every agent of the round is done. Without
the GPU, boxes are scored at once.

## Overfitting

Rounds keep whatever scores best, so after 100 rounds the result is tuned to
the data it was scored on. That is fine when the score is the goal (a
benchmark on fixed inputs), and a trap when it stands in for something
else (a model's quality, a strategy's future returns).

- **Score on a validation set, not the training data.** Agents train or fit
  on one part and are scored on another.
- **Keep a holdout outside the repository.** Boxes only see the repository,
  so data kept in `../<folder>-holdout/` can't leak into an attempt. Check
  the result on it once, at the end. The templates set this up.
- **Prefer simple changes.** Tell agents so in the task; a small gain from
  a lot of code is usually noise that fits.

## When a run ends

`--then "<command>"` runs a command on your machine, in the project, when
the rounds end for any reason: the last round, a limit, `stop`, `stop
--now`, or a failure. `HI_BEST_OF_RUN` holds the run's name, and the output
goes to the run's log. The templates use it to write their
[results](/guide/autoresearch/templates/#results):

```sh
hi agent best-of 3 --rounds 40 --score "make score" --higher --edit strategy.py \
  --then "make results" program.md
```

hi keeps every attempt's files, kept or not, under
`refs/best-of/<run>/`, so a command run later can still read them:
`git show refs/best-of/b-7/r3-2:train.py`.

## After a run

```sh
git log -p 3f2a91c..best-of/b-7/best   # every kept change, oldest first
git checkout best-of/b-7/best && make holdout && git checkout -
git merge best-of/b-7/best
```

`hi agent best-of rm b-7` removes leftover boxes, and the gains' branch if
the run had no gain.

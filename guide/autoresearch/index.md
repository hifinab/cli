---
title: Autoresearch with hi
description: Let coding agents try many ideas while hi checks and scores every attempt itself and keeps only what works. One round to pick the best of n, or rounds overnight that commit only gains.
---

Agents vary a lot from one attempt to the next. Some attempts are right,
some are wrong, and some look right without being right. `hi agent
best-of` turns that variation into progress: several agents try the same
task in separate [boxes](/guide/box/), **hi** checks and scores what each
one did, and only the results that pass and score well are kept.

It works in two ways:

| | Best of n | Rounds |
|---|---|---|
| Use it for | one task, a few tries, pick the best | a number to improve over many tries: a loss, a benchmark, a Sharpe ratio |
| Judged by | your check (`make check`), then a ranking | a score command that prints a number |
| Who keeps a result | you, with `keep` | hi, only when the score beats the best so far |
| How long | one round, minutes | rounds until a limit, often overnight |
| Command | `hi agent best-of 3 "<task>"` | `hi agent best-of 3 --rounds 50 --score "make score" --lower "<task>"` |

```sh
hi agent best-of 3 --agents claude,codex "make the backtest loader 2x faster"
hi agent best-of 4 --rounds 100 --score "make score" --lower --edit train.py program.md
```

Rounds are Karpathy's [autoresearch](https://github.com/karpathy/autoresearch)
loop (try an idea, measure it, keep it only if it's better, repeat), with
the judging moved from the agent to hi.

## Why you can leave it running

- **hi judges, not the agent.** When an agent ends, hi itself runs your
  check and your score in its box, on the files as the agent left them. An
  agent that says its tests pass, or that it improved the number, is checked,
  not believed.
- **Only hi commits.** In rounds, agents work on scratch branches. hi adds
  a commit to the run's own branch, `best-of/<run>/best`, only when a result
  beats the best so far. Nothing is merged into your branch or pushed;
  `git merge` is your step.
- **Boxes contain the work.** Every attempt runs in its own rootless
  container with only the project, a network limited to what you allow, and
  no credentials. `--edit` limits which files a result may change, and hi
  discards results that touch anything else, such as the evaluation.
- **Limits are yours.** You confirm the cost before anything starts, and a
  run ends at `--rounds`, `--for`, `--budget`, or `--patience`, or when you
  stop it.

## One round, step by step

1. hi starts n boxes from the same commit (for rounds: the best so far),
   each on its own branch, with `--agents` taken in turn.
2. Each agent works on the task, unattended, without permission prompts.
3. When an agent ends, hi stops whatever it left running, snapshots its
   files, then runs `--check` and `--score` on that snapshot.
4. hi ranks the boxes. In best of n, you pick one with `keep`. In rounds, the
   best score is committed if it beats the best so far, and every box is
   removed.
5. In rounds, the next round's task tells the agents the best score and what
   every earlier attempt tried and scored.

## Where to start

- [Several attempts at once](/guide/autoresearch/best-of/): best of n, the
  table, and `keep`.
- [Rounds that keep only gains](/guide/autoresearch/rounds/): scores,
  limits, `watch`, `stop`, and overfitting.
- [Start from a template](/guide/autoresearch/templates/): `hi init
  autoresearch-ml`, a project already shaped for rounds, with a tested
  evaluation and a holdout.
- [Simple examples](/guide/autoresearch/examples/) and
  [advanced examples](/guide/autoresearch/advanced/).

> Every attempt is a full agent run, so a run costs about n times the
> tokens of one, times the rounds. hi shows the plan and asks before it
> starts; agents pass `--yes` only after their person agreed.
{: .warning}

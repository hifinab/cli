---
title: Several attempts at once
description: Run one task in several boxes at once with hi agent best-of, let hi run your check in each, read the ranked table, and keep the best result.
---

`hi agent best-of` runs the same task in n boxes at once, 2 to 8. When an
agent ends, hi runs your check in its box. You get a ranked table and keep
one result; the others are removed.

```sh
hi agent best-of 3 --agents claude,codex "make the backtest loader 2x faster without changing results"
```

```text
Best-of b-4 · "make the backtest loader 2x faster without chang…" · 3 boxes · 23m
Check: make check

  #  BOX             AGENT   CHECK  DIFF          FLAGS     TIME  SPEND     NOTE
  1  myproject-b4-2  codex   ✓ 41s  +38 −12 2f    –         12m   880k tok
  2  myproject-b4-1  claude  ✓ 44s  +112 −40 5f   Makefile  18m   $2.10
  3  myproject-b4-3  claude  ✗ 12s  +65 −30 3f    –         15m   $1.80     check: FAILED test_loader.py::test_hash

Ranked by the check, then fewer flagged files and a smaller diff: read the diff before you keep one.
  hi agent best-of keep b-4 myproject-b4-2     (or: hi box diff myproject-b4-2)
```

## What happens

- hi shows the plan (agents, check, time limit, and that the run costs
  about n times the tokens of one) and asks before it starts. `--yes` skips
  the question; without a terminal it is required.
- Each box starts from your last commit on its own branch,
  `best-of/<run>/<i>`. The project must be a git repository; uncommitted
  changes aren't in the boxes, and hi says so.
- The boxes run at once. `hi box attach <box>` follows one.
- When an agent ends, hi stops whatever it left running, then runs the
  check in that box, on its network. The check is `make check`, or
  `--check "<command>"`.

## Reading the table

| Column | |
|---|---|
| CHECK | ✓ or ✗ and how long the check took |
| DIFF | lines added and removed, and files changed |
| FLAGS | changed files that run on your machine later, such as a `Makefile` or a CI workflow ([why](/guide/box/)) |
| TIME, SPEND | how long the agent worked, and its cost in dollars (Claude Code) or tokens (Codex) |
| NOTE | why a box is where it is: the check's last line, "changed nothing", "ran out of time" |

The order is no judgement of quality: boxes that passed and changed
something come first, then fewer flagged files, then the smaller diff. Read
the diff before you keep one: `hi box diff <box>`, or every box's diff with
`hi agent best-of show <run> --full`.

When every box passes and none changed a test or has a test named after the
files it changed, hi adds a note: the check may not test this task, so the
ranking says little.

## Keep one

```sh
hi agent best-of keep b-4 myproject-b4-2   # or by rank: keep b-4 1
```

`keep` removes the other boxes with their branches and work, after one
question. Work the kept agent left uncommitted is committed on its branch,
so `git merge best-of/b-4/2` takes all of it. Nothing is pushed.

`hi agent best-of rm b-4` removes every box instead.

## Options

| Option | |
|---|---|
| `--agents claude,codex` | the agents, taken in turn; different agents fail in different ways (default: the first agent ready) |
| `--check "<command>"` | the check hi runs in each box (default: `make check`) |
| `--max 45m` | a time limit for each agent |
| `--model <model>` | with one agent only; with several, each uses its own default |
| `--detach` | start and return; `show` has the table later |
| `--json` | the run as JSON, for scripts and agents |
| `--network`, `--allow`, `--gpu`, `--bundle`, `--data`, `--image`, `--memory` | box options, for every box |

The task can be words, a `.md` brief, or `-` for stdin, as for
[hi agent](/guide/agent/#a-task-from-a-file).

## Following a run

```sh
hi agent best-of ls          # runs and their state
hi agent best-of show b-4    # the table again
hi box attach myproject-b4-2 # follow one agent
```

Ctrl+C stops waiting, not the boxes. `hi agent best-of` exits with 1 when
no box passed.

## Limits

- On a Strix Halo, the boxes share 128 GB of memory and one GPU. With
  `--gpu` and more than 2 boxes, hi warns.
- The check is only as good as your tests. A weak check ranks weak results
  high.

---
title: Simple examples
description: Short hi agent best-of recipes - fix a flaky test three ways, get two vendors to compete, keep a speed-up only if a benchmark agrees, and a first small loop.
---

Each of these runs in a git repository with a commit to start from. They
cost real tokens: n attempts cost about n times one.

## Fix a flaky test three ways

```sh
hi agent best-of 3 --check "uv run pytest -q tests/test_sync.py --count 20" \
  "make the flaky test in tests/test_sync.py reliable"
```

The check runs the test 20 times (with `pytest-repeat`), so a box passes only
if the test is reliable, not lucky. Keep the smallest passing fix:

```sh
hi agent best-of show b-1       # the table
hi box diff myproject-b1-2      # read it
hi agent best-of keep b-1 1
git merge best-of/b-1/2
```

## Two vendors on one task

```sh
hi agent best-of 4 --agents claude,codex --max 30m \
  "replace the hand-written CSV parser in loader.py with the csv module, same results"
```

Claude Code and Codex alternate, two boxes each. They fail in different
ways, so the set covers more than four copies of one. `--max 30m` stops an
agent that wanders.

## Keep a speed-up only if the benchmark agrees

```sh
hi agent best-of 3 --check "make check" --score "make bench" --lower \
  --edit src/ "make parse_trades faster without changing its output"
```

With `--score`, a single round becomes all-or-nothing: hi measures your
last commit first, and commits the best box to `best-of/b-3/best` only if
its benchmark beats that. `make check` gates every box, and `--edit src/`
throws away any result that changes the benchmark or the tests.

`make bench` should print one number last, for example the median of
several timings:

```make
bench: ## Median seconds to parse the sample, over 5 runs
	@uv run python -m myproject.bench --runs 5
```

## A first small loop

```sh
hi agent best-of 2 --rounds 5 --budget 5 --score "make bench" --lower \
  --edit src/ "make parse_trades faster without changing its output"
hi agent best-of watch b-4
```

Five rounds of two boxes, at most $5. Each round starts from the best so
far and is told what the earlier rounds tried. q leaves the view; the run
goes on. In the morning:

```sh
hi agent best-of show b-4
git log -p main..best-of/b-4/best
git merge best-of/b-4/best
```

## From a script or another agent

```sh
hi agent best-of 3 --check "make check" --yes --detach --json "<task>"
hi agent best-of show b-5 --json    # later: boxes, ranks, and "best"
```

An agent that drives best-of should ask its person before `--yes`, read the
best box's diff, and let the person choose before `keep`.

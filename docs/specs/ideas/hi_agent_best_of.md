# `hi agent best-of` specification

Status: Draft. Releases 1 and 2 built on 2026-10-08 (see
[As built](#as-built-release-1) and [rounds, as built](#as-built-release-2)).

Dependencies: `hi agent` (agent runs and their reports), `hi box` (boxes,
worktrees, `diff`, the proxy; forks from its release 3 are useful but not
required), `hi server ai` for the judge, `hi ask` for choosing in Slack,
and `make check` from `hi init` templates.

## Goal

Run the same task several times at once, in separate boxes, check each
result the same way, and help a person keep the best one. Agents vary a lot
from one attempt to the next; a few attempts plus a fair check often beat
one careful attempt, and the boxes make it safe to run them all unattended.

```sh
hi agent best-of 4 "make the backtest loader 2x faster without changing results"
```

The output is a ranked table and one kept branch, not four branches to read
by hand.

With a score, best-of becomes a loop in the style of Karpathy's
[autoresearch](https://github.com/karpathy/autoresearch): rounds of n
attempts, each round starting from the best result so far, and a result
kept only when its score beats that ([Rounds](#rounds)).

Many different tasks at once, all kept, are a fan-out instead
([hi_agent_fanout.md](hi_agent_fanout.md)); the two share the queue, the
boxes, and the table.

## Commands

```text
hi agent best-of <n> "<prompt>"     start n boxes on the same task
    [--agents claude,codex]         which agents, round-robin (default: claude)
    [--check "<command>"]           how to check a result (default: make check)
    [--judge | --no-judge]          rank with the team's model (default: on with a server)
    [--max <duration>] [--gpu] [--network <preset>]
hi agent best-of ls                 best-of runs, their boxes, and their state
hi agent best-of show <run>         the table again, with diffs on request
hi agent best-of keep <run> <box>   keep one branch, remove the other boxes
hi agent best-of rm <run>           remove every box of a run and its branches
```

Every box option (`--gpu`, `--network`, `--max`, `--image`) applies to all
boxes in the run. `n` is between 2 and 8, and `policy.json` can lower the
upper limit per group (`max_best_of`), because each box costs agent tokens and
memory.

## How a best-of run works

1. **Start.** hi creates `n` worktrees from the current commit, on branches
   `best-of/<run>/<i>`, and starts one box per worktree with the same prompt.
   With `--agents claude,codex`, boxes alternate between agents: different
   agents fail in different ways, which makes the set more useful than more
   copies of one.
2. **Run.** Each agent runs to completion, as a background box does today.
   `hi agent best-of ls` shows each box's state; `hi box attach` works on any of
   them.
3. **Check.** When an agent finishes, hi itself runs `--check` in that box:
   not the agent, so an agent that claims its tests pass is checked, not
   believed. The check runs on the box's final state, with the network
   preset of the run. It records the exit status, the last 50 lines of
   output, and how long it took.
4. **Measure.** For each box: check passed or not, files and lines changed,
   files flagged by `hi box diff` as running on the host later, agent run
   time, and token spend where the agent reports it.
5. **Judge (optional).** With a server that serves a model, hi sends the
   prompt, each passing diff, and the measurements to the team's model and
   asks for a ranking with one line of reasoning per box. Only boxes whose
   check passed are judged; a failing box is never ranked above a passing
   one. The judge sees diffs, not the agents' transcripts.
6. **Report.** A table, and with Slack a message to the owner:

```text
Best-of b-12 · "make the backtest loader 2x faster…" · 4 boxes · 23m

  #  box        agent   check  diff        flags     judge
  1  loader-2   codex   ✓ 41s  +38 −12 2f   –         best: vectorizes the parse, same output hash
  2  loader-1   claude  ✓ 44s  +112 −40 5f  Makefile  works, but edits the Makefile's test target
  3  loader-4   claude  ✓ 39s  +210 −9 7f   –         adds a cache that changes results on reruns
  4  loader-3   codex   ✗ 12s  +65 −30 3f   –         test_loader fails

  hi agent best-of keep b-12 loader-2     (or: hi box diff loader-2)
```

In Slack, the message has **Keep #1**, **Keep…**, and **Discard all**
buttons. Keeping a box keeps its branch and box, and removes the others
after confirming. Nothing is pushed; pushing stays on the host, after
review, as with any box.

## Costs and limits

- Before starting, hi shows `n` boxes, the agents, the `--max`, and that the
  agents' token spend is multiplied by about `n`, and asks to confirm.
  Agents pass `--yes` only after their person agreed, as with compute.
- On the Strix Halo workstation, `n` boxes share 128 GB of memory and one
  GPU; with `--gpu`, boxes share it and hi warns above 2.
- `--max` applies to each box, and the whole run stops at `--max` plus
  the time to run checks.
- Best-of runs on rented GPUs come with remote boxes (release 4 of `hi box`).

### As built (release 1)

- `hi agent best-of <n> "<task>"` takes the task as words, a `.md` brief,
  or `-` for stdin, like `hi agent`; a brief's front matter applies to
  every box. Without `--agents`, every box gets the first agent that is
  ready, Claude Code first. `--model` goes with one agent only.
- Boxes are named `<project>-b<run>-<i>` and work on `best-of/<run>/<i>`.
  They start one after another, each in the background, so they run at
  once; the first box's network questions (bundles) are not asked again.
- Without `--check`, the project's Makefile needs a `check` target; hi
  reads the Makefile and doesn't run make to find it.
- hi wraps the agent's command in the box: the agent runs under
  `timeout` when `--max` is given, then `kill -9 -1` stops whatever it left
  running, then the check runs from `$HI_CHECK` and hi writes
  `check.json` and `check.log` in the box's `.hi-agent` folder. The box
  exits with the agent's status. So the check runs on the box's network
  and its final state even if nobody is waiting, and an agent can't
  rewrite the result afterwards. Tested on 2026-10-08 in Podman: a
  `setsid` process left by the agent was gone before the check, and
  `--max` gave exit 124, shown as "ran out of time".
- Without a judge, the order is: boxes that passed, changed something, and
  whose agent finished; then other passing boxes (no change, failed, or
  out of time); then running boxes; then failing ones. Within each:
  fewer flagged files, a smaller diff, a shorter run. The table says the
  order is not a judgement, and the suggested `keep` names the first box
  of the top group.
- The weak-check note is a heuristic: every box passed, no box changed a
  test file, and no test file in the project is named after a changed file
  (`loader.py`, `test_loader.py`).
- `keep` takes a box's name or its rank, refuses a box that is still
  working, removes the other boxes with their work and branches after one
  question, and keeps the run's record. `rm` removes every box but a kept
  one. Both need `--yes` without a terminal. `--detach`, `--json`, and
  `show --full` (each box's diff) are there for agents and scripts.
- Not yet: `max_best_of` in `policy.json`. The policy lives on the hi
  server and the boxes run locally, so the per-group limit waits for the
  server to see agent runs (release 2 of `hi agent`); until then `n` is 2
  to 8. Two Codex boxes that both refresh their sign-in during a run can
  invalidate each other's refresh token; mixing agents makes that less
  likely.
- Tested on 2026-10-08 with two Claude Code boxes on Haiku: both finished
  in about 6 seconds, `make check` ran in each, and `keep` removed the
  other box and its branch.

## Rounds

autoresearch runs one agent in one long session: it edits `train.py`,
commits, trains for five minutes, reads `val_bpb`, and keeps the commit if
the number improved or resets it if not, logging every attempt to
`results.tsv`. The rules (edit only `train.py`, never touch the evaluation)
are in the prompt, and the agent judges itself. best-of keeps the loop and
moves the judging to hi, as with the check: an agent that says it improved
the score is scored, not believed.

```sh
hi agent best-of 4 --rounds 100 --score "uv run train.py | grep ^val_bpb:" --lower --edit train.py program.md
hi agent best-of 4 --for 8h --score "…" --lower program.md           # overnight
hi agent best-of 1 --rounds forever --budget 50 --score "…" --lower program.md   # autoresearch's shape
```

| Flag | Meaning |
|---|---|
| `--score "<cmd>"` | hi runs it in each box after the agent ends and takes the last number it prints |
| `--lower` / `--higher` | which way is better; one is required |
| `--check` | still a gate: a result that fails it doesn't count. Without `--score` it defaults to `make check`; with it, there is no check unless given |
| `--rounds n` / `forever` | how many rounds (default 1) |
| `--for 8h` | no round starts after this |
| `--budget 50` | no round starts once agents have spent this many dollars |
| `--patience 10` | stop after 10 rounds without a gain |
| `--edit train.py` | the files and folders an agent may change; a result that changes anything else is disqualified, which enforces what autoresearch only asks |
| `--min-gain 0.001` | a smaller gain is noise, not progress |

**hi alone commits.** The run has one branch for its gains,
`best-of/<run>/best`, that only hi writes. Each round starts n boxes from
its tip. In its box an agent may commit, experiment, and run the score as
often as it likes; that is scratch. When the agents are done, hi scores
each box, and if the best beats the best so far, hi adds one commit to the
gains' branch (`best-of b-7 round 6: raise LR to 0.04`, with the scores in
the message). Otherwise the round is discarded. Every round's boxes and
branches are removed when the round ends. Nothing is merged into your
branch or pushed: `git merge best-of/<run>/best` takes the gains.

**Agents learn through the next task.** Each round's task is your task plus
what hi tells the agents: the round, the score command and its direction,
the best score so far, the check, the files they may change, and a table of
earlier attempts (round, agent, score, kept or discarded or crash, and the
agent's one-line summary from its report), like `results.tsv`.

**A baseline first.** Before any agent starts, hi runs the check and the
score on your last commit in a box with the run's options. A score that
fails or prints no number stops the run there, before it costs tokens. The
baseline is also where the box's questions (bundles' network) are asked,
in the terminal; the rounds reuse the answers.

**Leaving the view is not stopping.** The rounds run in a process of their
own, so they outlive the terminal and SSH. The view (`watch`) shows a row
per round; q, Esc, or Ctrl+C leave it and the run goes on, as Ctrl+C stops
waiting but not the boxes. `stop` ends the run after the current round, so
no work is lost; `stop --now` also removes the round's boxes. `resume` goes
on from the best so far, and its `--rounds`, `--for`, and `--budget` count
from where the run is.

**One GPU, one score at a time.** autoresearch's metric is the best result
in a fixed five minutes of training. Boxes training at once on the Strix
Halo would each get part of the GPU, and the scores would measure the
contention. With `--gpu`, agents work in parallel, but hi gives the boxes
the turn to be scored one at a time, after every agent of the round is
done. Without `--gpu`, they are scored at once.

```text
Best-of b-7 · "program.md" · 4 boxes a round · round 9 of 100 · 2h41m · $31.20 spent
Score: uv run train.py | grep ^val_bpb: (lower is better)
Best: 0.9871 from round 6, baseline 0.9979 · on best-of/b-7/best

  ROUND  RESULT     SCORE   BOX           IDEA
  6      kept       0.9871  ml-b7-r6-2    raise the learning rate to 0.04
  7      discarded  0.9951  ml-b7-r7-4    switch to GeLU
  8      crash      –       –             no box had a usable result

Now: round 9: scoring ml-b7-r9-2 (2 of 4)
```

### As built (release 2)

- `--score` turns any run into rounds; without `--rounds` it is one round,
  kept only if it beats your last commit. `n` can be 1 with a score.
- The box wrapper (release 1) now also snapshots the files the agent left
  with `git add -A` and `git write-tree`, after killing what it left
  running and before the check and score run, into `.hi-agent/tree.txt`.
  What changed, `--edit`, and the commit all use that tree, so files the
  score writes (a `run.log`) are neither committed nor count against
  `--edit`. A box without a snapshot can't be kept. `keep` (release 1)
  commits the work an agent left uncommitted from the same snapshot, so
  `git merge` takes all of it.
- With a score, the check and score wait in the box until hi creates
  `go` in a folder mounted read-only at `/box/turn`, so an agent can't
  start its own scoring early. The score runs under `timeout`, at twice the
  baseline's time plus a minute (at least two minutes).
- The winner is the good box (check passed, a score, inside `--edit`,
  something changed) with the best score, whatever its agent's own status:
  the score is what counts. It is kept if it beats the best so far by more
  than `--min-gain`. The commit is `git commit-tree` of its snapshot on the
  gains' tip, with your git identity, or `hi agent best-of` when there is
  none.
- The rounds' process is `hi agent best-of __loop <run>`, started with
  `setsid` and logging to `loop.log` in the run's state folder. A stop
  request is a `stop` file there; the process checks it every few seconds
  while waiting and before each round. `resume` after a reboot picks up a
  round whose boxes still exist.
- `--budget` counts what agents report in dollars: Claude Code's cost at
  API prices, and Hermes' when it has one. Codex reports only tokens, so
  its runs count as nothing; the confirmation says so.
- Round history keeps the last five lines of each box's check and score
  output, to keep `run.json` small over hundreds of rounds.
- Tested on 2026-10-08 with Podman and Claude Code on Haiku: a run of two
  boxes and two rounds took a number from 100 to 95 to 85, with one commit
  per gain and only the allowed file in it; `watch` left with q while the
  run went on, and `stop --now` removed the round's box. Turns one at a time
  (`--gpu`) are covered by the code path but were not run on the GPU.

### Templates and the guide (2026-10-08)

- `hi init autoresearch-ml` and `hi init autoresearch-quant` start projects
  shaped for rounds: a fixed `prepare.py` and `evaluate.py`, one file agents
  change (`train.py` or `strategy.py`), frozen data in `eval/` (committed,
  since `data/` is ignored and boxes see only commits), a holdout in
  `../<folder>-holdout/`, and a box image that installs `uv.lock` into
  `/opt/venv` so boxes run offline on the locked network. Their evaluation
  refuses an editable file that opens files, imports outside a short list,
  or runs code dynamically; `autoresearch-ml` also fails a `train()` that
  runs past its budget, timed with a clock bound before `train.py` loads.
- hi builds a project's Dockerfile image again when the Dockerfile, its
  `build.args`, or a file it `COPY`s changes, and passes `build.args`.
- best-of takes `"gpu": true` from `devcontainer.json` as `--gpu`, so those
  boxes take turns to be scored.
- A round whose boxes all scored but changed nothing is `unchanged`, not
  `crash`. Seen in a real round: Haiku tried eleven ideas on HAA, found none
  better, and put the file back.
- The guide has an Autoresearch section: how it works, best of n, rounds,
  the templates, and simple and advanced examples.

### Results (2026-10-09)

Asked for after the first long real run (42 rounds on HAA): every number of
a run in files, and a report that reads them, without a web server.

- `--then "<command>"` runs a command on the host, in the project, once the
  rounds end for any reason (finished, a limit, `stop`, `stop --now`, or a
  failure), after the run's state is saved. `HI_BEST_OF_RUN` holds the run;
  the output goes to `loop.log`. `resume --then` replaces it.
- hi keeps every attempt's files: when a round ends, each box's snapshot
  gets a commit under `refs/best-of/<run>/r<round>-<i>`, so git never prunes
  it and results made later can re-run attempts that weren't kept.
  `hi agent best-of rm` leaves these refs.
- `hi agent best-of ls --json` lists the runs, so a script can find the
  newest run of a project.
- `autoresearch-quant` has `make results` (and `make loop` passes `--then
  "make results"`): `results.py` re-runs every attempt from its files with
  the project's `evaluate.py`, in-sample and on the holdout, and writes
  `results/<run>/`: `run`, `attempts`, `rounds`, `returns`, and `weights` as
  Parquet, each attempt's `strategy.py`, hi's JSON, and `report.html`.
  `results/` is ignored by git, since boxes must never see holdout scores.
- The report needs no server: a page opened from disk can't `fetch()` the
  files next to it, so `make results` embeds the Parquet files (base64) and
  a bundled copy of hyparquet (a small Parquet reader in plain JavaScript,
  MIT) in `report.html`. DuckDB-Wasm would work too, but it is several
  megabytes of WebAssembly from a CDN for tables of a few hundred kilobytes.
  Served over HTTP, the same page reads the Parquet files beside it.
- The report: the score of every attempt with the best so far and its
  holdout score, the growth of $1 with a synced drawdown chart for each
  round against the baseline and a benchmark (one round at a time or all at
  once), one small chart per kept version, returns by year for every
  version, computed facts, and the attempt log. Each re-run score is
  compared with what hi recorded.

## Releases

1. `best-of`, `ls`, `show`, `keep` with local boxes, the check run by hi, the
   measurements, and the table. No judge.
2. Rounds: `--score`, the baseline, commits by hi only on the gains'
   branch, the history in each round's task, `--edit`, the limits, the
   rounds' own process, `watch`, `stop`, `resume`, and scoring one box at a
   time on the GPU.
3. The judge through the team's model, and the Slack message with buttons;
   a message when a long run keeps a gain or ends.
4. Mixed agents tuned by results: hi records which agent won which run,
   and `hi agent best-of stats` shows it per project, so the team can see whether
   mixing pays.

## Risks

- **Weak checks pick wrong winners.** If `make check` doesn't test what the
  task changes, every box passes and the ranking falls to the judge. The
  report says when all boxes passed with no test touching the changed
  files.
- **The judge is a model.** It can prefer long or clever diffs. It only
  ranks among passing boxes, and a person always picks.
- **Cost.** n times the tokens for one task. The confirmation says so, and
  the default `n` in the skill is 3.
- **Same mistake n times.** Copies of one agent with one prompt often fail
  the same way; mixing agents is the main defence.

## Open questions

1. Whether to give each box a slightly different prompt (for example
   "prefer the smallest change", "prefer the fastest") for more variety.
2. Whether the judge should also run the code (for example a benchmark the
   prompt names) rather than only read diffs. That is closer to a second
   check than to judging, and may belong in `--check`.
3. Whether a run should stop early when the first box passes, to save
   tokens, as an option (`--first`).
4. Ties in rounds: autoresearch keeps an equal score when the code got
   simpler. hi could keep a result within `--min-gain` that removes lines.
5. Memory across rounds: fresh agents with the history table don't fill
   their context over 100 rounds; resuming each box's session across rounds
   would give real memory, once `hi agent resume` exists.
6. Re-measuring the best every k rounds, so a lucky score on a noisy metric
   doesn't stick, at the cost of GPU time.
7. Scoring on rented GPUs through `hi compute`, so rounds aren't limited to
   one Strix Halo.

## Findings

Checked on 2026-10-02.

- **Products.** Codex runs up to four attempts of a cloud task
  (`codex cloud exec --attempts 1-4`). Cursor's `/best-of-n` runs several
  models, each in its own worktree, and a parent agent comments on the
  results; nothing is merged automatically. Neither runs locally in a
  sandbox with the team's own check and a person's choice in Slack.
- **Selection is the hard part.** In CodeMonkeys, 69.8% of SWE-bench
  Verified issues had at least one correct candidate, but only 57.4% were
  solved after selection, which recovered about half the gap between
  random and perfect choice. SWE-Gym's trained verifier picked the best of
  8 at 32.0% while 42.8% of issues had a correct candidate in 16.
- **Tests and judges work best together.** In R2E-Gym, test-based and
  model-judge selection each reached about 43% (43.7% and 42.8%), and a
  hybrid reached 51.0%, against 34.4% for a single attempt. Tests written by
  a model told candidates apart in under 20% of cases. Agentless filters
  with reproduction and regression tests, then takes a majority vote.

This is the design above: the project's own check first, run by hi, then a
model judge among the passing boxes, then a person. A weak check is the
main way it goes wrong, which is why the report flags runs where no test
touched the changed files.

## Sources

- https://learn.chatgpt.com/docs/developer-commands?surface=cli
- https://cursor.com/docs/configuration/worktrees
- https://scalingintelligence.stanford.edu/blogs/codemonkeys/
- https://arxiv.org/abs/2504.07164 (R2E-Gym)
- https://arxiv.org/abs/2412.21139 (SWE-Gym)
- https://arxiv.org/abs/2407.01489 (Agentless)

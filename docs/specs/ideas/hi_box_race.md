# `hi box race` specification

Status: Draft

Dependencies: `hi box` (boxes, worktrees, `diff`, the proxy, finished-agent
messages; forks from its release 3 are useful but not required), `hi server
ai` for the judge, `hi ask` for choosing in Slack, and `make check` from
`hi init` templates.

## Goal

Run the same task several times at once, in separate boxes, check each
result the same way, and help a person keep the best one. Agents vary a lot
from one attempt to the next; a few attempts plus a fair check often beat
one careful attempt, and the boxes make it safe to run them all unattended.

```sh
hi box race 4 "make the backtest loader 2x faster without changing results"
```

The output is a ranked table and one kept branch, not four branches to read
by hand.

## Commands

```text
hi box race <n> "<prompt>"         start n boxes on the same task
    [--agents claude,codex]        which agents, round-robin (default: claude)
    [--check "<command>"]          how to check a result (default: make check)
    [--judge | --no-judge]         rank with the team's model (default: on with a server)
    [--max <duration>] [--gpu] [--network <preset>]
hi box race ls                     races, their boxes, and their state
hi box race show <race>            the table again, with diffs on request
hi box race keep <race> <box>      keep one branch, remove the other boxes
```

Every box option (`--gpu`, `--network`, `--max`, `--image`) applies to all
boxes in the race. `n` is between 2 and 8, and `policy.json` can lower the
upper limit per group (`max_race`), because each box costs agent tokens and
memory.

## How a race runs

1. **Start.** hi creates `n` worktrees from the current commit, on branches
   `race/<race>/<i>`, and starts one box per worktree with the same prompt.
   With `--agents claude,codex`, boxes alternate between agents: different
   agents fail in different ways, which makes the set more useful than more
   copies of one.
2. **Run.** Each agent runs to completion, as a background box does today.
   `hi box race ls` shows each box's state; `hi box attach` works on any of
   them.
3. **Check.** When an agent finishes, hi itself runs `--check` in that box:
   not the agent, so an agent that claims its tests pass is checked, not
   believed. The check runs on the box's final state, with the network
   preset of the race. It records the exit status, the last 50 lines of
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
Race r-12 · "make the backtest loader 2x faster…" · 4 boxes · 23m

  #  box        agent   check  diff        flags     judge
  1  loader-2   codex   ✓ 41s  +38 −12 2f   –         best: vectorizes the parse, same output hash
  2  loader-1   claude  ✓ 44s  +112 −40 5f  Makefile  works, but edits the Makefile's test target
  3  loader-4   claude  ✓ 39s  +210 −9 7f   –         adds a cache that changes results on reruns
  4  loader-3   codex   ✗ 12s  +65 −30 3f   –         test_loader fails

  hi box race keep r-12 loader-2     (or: hi box diff loader-2)
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
- `--max` applies to each box, and the whole race stops at `--max` plus
  the time to run checks.
- Races on rented GPUs come with remote boxes (release 4 of `hi box`).

## Releases

1. `race`, `ls`, `show`, `keep` with local boxes, the check run by hi, the
   measurements, and the table. No judge.
2. The judge through the team's model, and the Slack message with buttons.
3. Mixed agents tuned by results: hi records which agent won which race,
   and `hi box race stats` shows it per project, so the team can see whether
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
3. Whether a race should stop early when the first box passes, to save
   tokens, as an option (`--first`).

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
main way it goes wrong, which is why the report flags races where no test
touched the changed files.

## Sources

- https://learn.chatgpt.com/docs/developer-commands?surface=cli
- https://cursor.com/docs/configuration/worktrees
- https://scalingintelligence.stanford.edu/blogs/codemonkeys/
- https://arxiv.org/abs/2504.07164 (R2E-Gym)
- https://arxiv.org/abs/2412.21139 (SWE-Gym)
- https://arxiv.org/abs/2407.01489 (Agentless)

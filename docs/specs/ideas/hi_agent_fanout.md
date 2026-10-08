# `hi agent fanout` specification

Status: Draft (2026-10-08).

Dependencies: `hi agent` (runs, reports, `--detach` and `wait`, the live
line, the stats), `hi box` (boxes, worktrees, mounts, the proxy),
[hi_agent_best_of.md](hi_agent_best_of.md) (the table, `ls`, `show`, the Slack
message), and [hi_agent_bundles.md](hi_agent_bundles.md) (front matter in
briefs).

## Goal

Run many different tasks at once, each in its own box, and get one report
for all of them. A best-of run tries one task several ways and keeps one result;
a fan-out does several tasks and keeps them all.

```sh
hi agent fanout briefs/*.md                                   # one run per brief
hi agent fanout "summarize {} into {}.md" --each data/*.csv   # one run per file
hi agent fanout --split tickets.md --then "write one release note from these results"
```

Typical uses:

- **Independent pieces of one job.** Ten failing tests, one ticket each,
  one module each to port.
- **The same work over many inputs.** One agent per CSV file, per PDF, per
  site to screenshot.
- **Map, then reduce.** Many runs each produce a result, and a last run
  combines them (`--then`).

Not for work that needs shared context while it happens: Claude Code and
Codex have subagents for that, as the `hi agent` spec says. A fan-out pays
off when the tasks are independent, long, need their own box, or suit
different agents or models.

## Commands

```text
hi agent fanout <brief.md>...                  one run per brief
hi agent fanout --split <file.md>              one run per "## " section of one file
hi agent fanout "<task with {}>" --each <glob>       one run per matching file
hi agent fanout "<task with {}>" --each-line <file>  one run per line
    [--agents claude,codex,hermes]   take turns (default: the first agent ready)
    [--parallel <k>]                 runs at the same time (default 3, at most 8)
    [--then "<task>"]                a last run that gets every report and result
    [--retry <n>]                    run a failed task again, up to n times (default 0)
    [--model, --bundle, --network, --allow, --data, --gpu, --max, --name]
    [--detach] [--json] [--yes]
hi agent fanout ls                             fan-outs, their tasks, and their state
hi agent fanout show <id> [--json]             the table and the combined report again
hi agent fanout retry <id>                     the failed tasks again
hi agent fanout merge <id>                     in git: merge the done tasks' branches, in order
hi agent fanout rm <id>                        remove its boxes (branches with commits are kept)
```

- `{}` is the matched file's path; `{name}` is its name without the folder
  or extension. A task without `{}` and with `--each` is refused.
- Box options apply to every run. A brief's front matter (agent, model,
  bundles, network, allow, data, gpu) applies to its own run, and is asked
  about as for `hi agent` today. A flag on the command line wins.
- The id is `f-<n>`; its runs are boxes named `<id>-<i>`, so `hi box ls`,
  `attach`, and `diff` work on each.
- At most 50 tasks per fan-out; `policy.json` can lower it per group
  (`max_fanout`), and `--parallel` too (`max_parallel`).

## How a fan-out runs

1. **Plan.** hi reads the tasks, applies the options, and prints a table:
   each task's first line, its agent and model, and its bundles. It says how
   many runs there are, how many at once, and that tokens grow with the
   number of tasks, and asks to confirm. Without a terminal it needs
   `--yes`.
2. **Queue.** hi starts `--parallel` runs and starts the next as soon as one
   finishes. Bundle images are built once, before the first run, so runs
   don't build the same image side by side.
3. **Watch.** One line per running task, as the live line of `hi agent`
   today, and a count of queued and finished ones:

   ```text
   f-7 · 12 tasks · 3 running · 5 done · 1 failed · 3 queued · 9m14s
   ▬▬▬▬▬▬▬▬▬▬▬▬  f-7-6  2m03s · 210k in · 4.1k out · 9 steps · Bash: pytest tests/test_loader.py
   ▬▬▬▬▬▬▬▬▬▬▬▬  f-7-7  1m40s · 120k in · 2.2k out · 5 steps · Edit: loader.py
   ▬▬▬▬▬▬▬▬▬▬▬▬  f-7-8    31s · 44k in · 600 out · 2 steps · Read: README.md
   ```

   Ctrl+C stops waiting, not the runs; queued tasks then start only while
   hi waits, or with `hi agent fanout show <id>`, which picks the queue up
   again. With `--detach`, hi starts a small background process that keeps
   the queue going.
4. **Retry.** With `--retry`, a run that failed (exit status, no report, or
   a report that says `blocked`) runs again once more in a new box. `retry
   <id>` does the same later, by hand.
5. **Then.** With `--then`, a last run starts when every task is done. Its
   task is the `--then` text, followed by each task, its report, and where
   its results are. It works in a box of its own like any run.
6. **Report.** A table and a combined report:

   ```text
   Fan-out f-7 · 12 tasks · 18m · 2.4M tokens in · 61k out

     #   box      agent   status   report     changed            time   tokens
     1   f-7-1    claude  done     complete   2 files            3m10s  210k / 5.1k
     2   f-7-2    codex   done     partial    1 file             4m02s  180k / 3.9k
     3   f-7-3    claude  failed   –          –                  0m41s  20k / 300
     …

   Follow-ups
     f-7-2  the cache test still needs a fixture for empty files
   ```

   With `--json`, one object with the fan-out's fields and a `runs` array of
   the usual `hi agent` reports. With Slack, one message to the owner when
   it's done, not one per run.

## Where the work goes

**In a git repository.** Each run gets its own worktree and branch,
`fanout/<id>/<i>`, as `hi agent` does today, so runs never touch each
other's files. `fanout merge <id>` merges the done tasks' branches into the
current branch in task order, after showing what it will merge, and stops
at the first conflict and says which. Nothing is pushed.

**Outside git.** Today a run outside git works in the folder itself, so
runs side by side would write over each other. In a fan-out the folder is
mounted read-only in each box, and each run gets one writable folder,
`fanout/<id>/<i>/`, mounted on top. The task tells the agent to write its
results there; the box enforces it. The combined report lists each folder's
files.

## Costs and limits

- Tokens grow with the number of tasks; the confirmation says so, and the
  report gives the total, with each run's stats and Claude Code's cost at
  API prices where it gives one.
- Memory: each box defaults to 16 GB (`--memory`), so `--parallel` is
  capped by what the machine has free, and hi says when it lowers it.
- `--gpu`: boxes share the one GPU; hi warns above 2.
- Rate limits: a subscription's limits are shared by every run of that
  agent. A run that hits one fails and can be retried; mixing agents with
  `--agents` spreads the load.
- `--max` is per run. The fan-out has no limit of its own beyond its runs.

## Shared with `hi agent best-of`

A best-of run is a fan-out of one task n times plus a check and a choice, so both
are built on the same parts: the queue, the per-run boxes, the table, `ls`,
`show`, `rm`, the Slack message, and the policy limits. Whichever is built
first builds those; the other adds only its own steps (best-of: check, judge,
keep; fan-out: tasks from files, `--then`, `merge`, the read-only folder).

## Releases

1. Briefs, `--split`, `--each`, `--each-line`; `--parallel`, `--agents`,
   `--retry`; the live lines, the table, the combined report and its JSON;
   `ls`, `show`, `retry`, `rm`; worktrees in git and the read-only folder
   outside it.
2. `--then`, `merge`, `--detach` with the background queue, and the Slack
   message.
3. Fan-outs on remote boxes, when `hi box --remote` exists.

## Risks

- **Cost and rate limits.** Fifty tasks spend fifty runs' tokens and can
  use up a subscription's limit for the day; the confirmation and the
  per-group limits are the defence.
- **Tasks that aren't independent.** Two runs that change the same files
  conflict at `merge`. The plan can't tell; `merge` stops at the first
  conflict, and the report says which files more than one run changed.
- **Prompt injection across tasks.** With `--each`, the inputs are files
  that may come from anyone; with `--then`, the last run reads every
  result. Each run is still a box with its own network, and `--then`
  gets the results as files, not as instructions from hi.
- **Briefs from others.** A fan-out of briefs written by someone else, or
  by an agent, is asked about as `hi agent` asks about front matter today:
  what would widen a box is shown in the plan and needs a yes.

## Open questions

1. Whether `--each` should also take a list from stdin (`ls *.csv | hi agent
   fanout "…" --each -`).
2. Whether a run's result folder outside git should be copied back into
   the folder itself at the end, when no two runs wrote the same file.
3. Whether `--then` should be a fixed "summarize" by default, so
   `--then` alone is enough.
4. Whether the queue should move on to the next agent when one is out of
   quota, sharing the configured agent order from release 2 of `hi agent`.
5. How many runs this workstation can hold at once: memory per box for
   Claude Code, Codex, and Hermes, measured before setting the defaults.

## Findings

From checking the idea against the code on 2026-10-08:

- **The pieces exist.** `hi agent --detach` starts a run and returns, and
  `hi agent wait` gets its report; a shell loop over both is a fan-out today,
  without a queue, a table, or a combined report.
- **Worktrees keep git runs apart.** Every run in a git project already gets
  its own worktree and branch.
- **Outside git they don't.** Runs work in the folder itself, so two runs
  at once can write the same file; hence the read-only folder with one
  writable folder per run.
- **The live line and the stats** read each agent's own session log, so one
  line per run costs no more than one run's line does today.
- **Bundle images** are built once per set of bundles and reused, so
  building them before the first run keeps runs from building the same
  image side by side.

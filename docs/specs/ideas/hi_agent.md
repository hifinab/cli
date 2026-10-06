# `hi agent` specification

Status: Draft

Dependencies: `hi box` (boxes, worktrees, the proxy, `diff`), `hi install`
(Claude Code and Codex today; Antigravity CLI as a new tool), `hi skill`,
and for nested calls the box socket that the `hi box` and `hi ask` specs
assume but release 1 of `hi box` doesn't have. `hi server` adds audit and
the live view; it is not required.

## Goal

One command that hands a task to a coding agent, waits for it, and returns
the same report whichever agent did the work. A person or an agent calls it;
hi deals with each agent's flags, output format, session IDs, and failure
modes, and with cleaning up afterwards.

```sh
hi agent "make the backtest loader handle missing days" --json
hi agent codex "review the diff on branch fix-loader; don't change files" --tier read
```

`hi box claude "<prompt>"` already runs an agent safely, but in the
background: the result is a branch to look at later. What's missing is the
call: start, wait, get a report an agent can act on, and resume the same
session for a follow-up. The reasons to want it:

- **A second vendor.** Codex reviews what Claude Code wrote, or the other
  way round. Different agents fail in different ways.
- **A sandbox for part of the work.** An interactive agent on the host hands
  a risky or long task to a boxed agent and carries on.
- **Running out of quota.** When one agent is rate-limited, hi uses the next
  one instead of the task stopping.
- **Visibility.** Every delegated run is a box, so it shows in `hi box ls`,
  and with a server in the audit log and `hi server live`.

Fanning out inside one agent's session is not a reason: Claude Code and
Codex have their own subagents for that, and they share context that a
separate run doesn't. The skill says so.

## Commands

```text
hi agent [claude|codex|agy] "<task>"   run a task and wait for the report
    [--tier read|edit|full]            what the run may change (default: edit)
    [--max <duration>]                 wall-clock limit (default: 30m)
    [--context <file|->]               extra text added to the task
    [--json] [--detach] [--name <n>]
hi agent resume <id> "<follow-up>"     continue the same agent session
hi agent wait <id> [--timeout <d>]     wait for a detached run, print its report
hi agent ls                            runs, their agent, state, parent, age
hi agent show <id>                     the report again, with the log folder
hi agent stop <id>                     stop a run and its children
```

Without an agent name, hi takes the first agent in the configured order
(`agents` in `~/.config/hi/config.json`, default `claude, codex, agy`) that
is installed and signed in. If that agent fails before starting work because
of its quota or sign-in, hi tries the next one and says so in the report's
`warnings`. Choosing an agent by what the task looks like is left out on
purpose: the order is predictable, and a person or agent who knows better
names one.

`--tier` replaces each agent's permission flags:

| Tier   | Where it runs                                           | Afterwards                     |
|--------|---------------------------------------------------------|--------------------------------|
| `read` | A box on a throwaway worktree, `dev` network            | The worktree is removed         |
| `edit` | A box on a new worktree and branch, `dev` network       | The branch and box are kept     |
| `full` | As `edit`, with the `open` network                      | The branch and box are kept     |

Inside a box every agent already runs without permission prompts, so the
tiers are the same for every agent: the box decides what a run can reach,
not the agent's own flags. That also covers Antigravity CLI, which has no
read-only flag and soft-denies shell commands with exit status 0.

## The report

Every run ends with one report, the same for every agent:

```json
{
  "id": "a-41",
  "agent": "codex",
  "status": "done",
  "session_id": "019a…",
  "text": "the agent's final message",
  "report": {"status": "complete", "summary": "…", "tests": "pass", "follow_ups": []},
  "branch": "agent/a-41",
  "changed_files": ["loader.py", "tests/test_loader.py"],
  "tokens": {"input": 24763, "output": 1220},
  "duration_seconds": 412,
  "warnings": [],
  "parent": null,
  "log_dir": "~/.local/state/hi/agent/a-41"
}
```

- **`report`** comes from a short block hi adds to the end of every task and
  parses out of the agent's final message:

  ```text
  <<<REPORT
  {"status": "complete|partial|blocked", "summary": "…", "tests": "pass|fail|not_run", "follow_ups": []}
  REPORT>>>
  ```

  A prompt contract, not each agent's JSON-schema flag, because the three
  agents take and return schemas differently and a new agent may have none.
  A missing or broken block is a warning, not a failure.
- **`changed_files`** comes from git in the worktree, never from the agent.
- **`status`** is `done`, `failed`, `timeout`, or `stopped`. A non-zero exit,
  Claude Code's `is_error`, Codex's `turn.failed`, and an Antigravity
  `status` other than `SUCCESS` all become `failed`.
- Without `--json`, the output is the summary, the changed files, the
  branch, and the follow-ups, in a few lines.

Exit statuses follow the rest of hi:

| Outcome | Exit |
|---|---|
| `done` | 0 |
| `failed` | 1 |
| Detached, or still running when `wait --timeout` ends | 3 (pending) |
| `timeout` or `stopped` | 5 |

## Adapters

One adapter per agent, in Go, each turning a run into the report. What each
one has to handle:

| | Claude Code | Codex | Antigravity CLI |
|---|---|---|---|
| Run | `claude -p … --output-format json --permission-prompts none` | `codex exec --json -o <file> …` | `agy -p … --output-format json --print-timeout <max>` |
| Final text | `.result` | the `-o` file | `.response` |
| Session ID | `.session_id` | `thread_id` in `thread.started` | `.conversation_id` |
| Resume | `--resume <id>` | `codex exec resume <id>` | `--conversation <id>` |
| Tokens | `.usage` | sum of `turn.completed` usage | `.usage` |
| Failure | exit status, `is_error` | exit status, `turn.failed` | exit status, `status` |
| Turn limit | `--max-turns` | none | none |

hi enforces `--max` itself for every agent, with a wall-clock timer that
stops the box, rather than relying on each agent's own limit. An adapter
checks the agent's version against the range it was tested with and warns
outside it; `hi doctor` reports the same.

Resuming needs the agent's session files, which live in the box's home
folder. `hi agent resume` restarts the same box on the same worktree, so
they are still there. A `read` run's worktree is removed but its home
folder is kept until `hi box rm`, so a read-only conversation can continue
too.

## Agents calling agents

An agent can run `hi agent` itself. On the host that just works. Inside a
box there is no hi device key and no container engine, and boxes inside
boxes are not supported, so:

- `hi` in a box talks to hi on the host through a socket mounted into the
  box. The host starts the child as a sibling box and records the parent.
  This is the socket the `hi box` spec plans for compute requests from a
  box; `hi agent` would be its first user.
- **Depth.** Each run passes `HI_AGENT_DEPTH` to its children; past 2, `hi
  agent` fails with a clear error. `agent_max_depth` in the config, and in
  `policy.json` per group with a server.
- **How many at once.** At most 4 runs per user at the same time on the
  workstation (`agent_max_running`); more wait in a queue, shown in `hi
  agent ls`. Each run is a box with its own memory limit.
- **Time.** A child's `--max` can't be longer than what is left of its
  parent's, and `hi agent stop` stops the whole tree.
- A child's branch starts from the parent's current commit, and its report
  goes back to the parent, not to the person. Merging a child's branch into
  the parent's worktree is the parent's job, through git.

## With `hi server`

- Every run, its agent, parent, tier, duration, and token counts go to the
  audit log, without the task or the report text.
- Running agents appear in `hi server live`, as a tree when they have
  children.
- Requests from a run carry its name, as boxes' do: `iman via Claude Code,
  in agent a-41`.

## Releases

1. `hi agent claude|codex "<task>"` on local boxes with tiers, `--max`,
   the report, `--json`, `--detach`, `wait`, `resume`, `ls`, `show`, and
   `stop`. Choosing the first available agent, and moving on when one fails
   to start. The skill teaches when to delegate. No nesting.
2. The box socket, nested calls with depth, concurrency, and time limits,
   and runs in the server's audit log and live view.
3. Antigravity CLI: `hi install agy`, its sign-in in the box, and its
   adapter. `hi box race` rebuilt as `n` `hi agent` runs plus hi's check.

## Risks

- **Flags change often.** All three CLIs change their headless flags and
  output between releases. The version checks and `hi doctor` catch it;
  adapters need tests against recorded output.
- **Cost of nesting.** A loop that keeps delegating multiplies token spend.
  The depth and concurrency limits are the defence, and the report's token
  counts make it visible.
- **Agents delegate when they shouldn't.** Handing off a task loses the
  caller's context and costs a cold start. The skill limits it to the
  reasons in Goal.
- **Sign-ins in boxes.** Codex's sign-in goes into the box today, and
  Antigravity's cached credentials would too. Only Claude Code's token stays
  at the proxy.
- **The report block is a request, not a guarantee.** Agents sometimes
  leave it out or break its JSON; the changed files and status don't depend
  on it.

## Open questions

1. Whether `hi agent` should be its own command or `hi box` options
   (`hi box claude "<task>" --wait --json`). Its own command reads better
   for agents, picks an agent, and leaves `hi box` about the place a run
   happens; the cost is two commands that both start agents.
2. Whether `read` should be allowed to run on the host without a box, using
   each agent's read-only mode, for speed. Antigravity has no such mode, so
   tiers would stop being the same everywhere.
3. Whether to stream progress (each agent's `stream-json` or JSONL events
   turned into one format) for `hi agent attach`, or only show the box's
   log as `hi box attach` does.
4. Whether a parent should get a child's diff in the report, or only the
   branch name and changed files.

## Findings

Checked on 2026-10-06 against the vendors' headless docs.

- **The same loop everywhere.** Claude Code (`claude -p`), Codex (`codex
  exec`), and Antigravity CLI (`agy -p`) all run one task headless, print a
  final message, give a session ID, and resume by it. They differ in how
  permissions are set, the shape of the output, where the session ID is,
  and how failure shows.
- **What only some have.** Cost in dollars (Claude Code), a turn limit
  (Claude Code), and per-tool allowlists on the command line (Claude Code;
  Antigravity in a settings file). A shared contract can't depend on them.
- **Antigravity's gaps.** No read-only flag; shell commands default to ask,
  which headless mode turns into a denial that still exits 0; the default
  `--print-timeout` is 5 minutes; and sign-in is cached credentials from an
  interactive run.
- **Codex's gaps.** It must run in a git repository unless told otherwise,
  has no turn limit, and only gives the session ID in its JSONL events.
- **Gemini CLI is on its way out.** Since 2026-06-18 it no longer serves
  individual Google AI Pro, Ultra, or free Code Assist users; enterprise
  licences get maintenance only, and new features go to Antigravity CLI.
  Antigravity reads `GEMINI.md` and `AGENTS.md`, and workspace skills move
  to `.agents/skills/`, where `hi skill` already writes.

These findings are the design above: hi depends only on what all three
share, computes what it can itself (changed files, duration, status), and
makes the permission tiers equal by running every agent in a box.

## Sources

- https://code.claude.com/docs/en/headless
- https://developers.openai.com/codex/noninteractive
- https://antigravity.google/docs/cli/headless/
- https://antigravity.google/docs/cli/gcli-migration/
- https://developers.googleblog.com/an-important-update-transitioning-gemini-cli-to-antigravity-cli

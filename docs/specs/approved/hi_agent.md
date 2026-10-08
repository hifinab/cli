# `hi agent` specification

Status: Approved (2026-10-07). Release 1 shipped in v0.28.0, tested on
2026-10-07 with Claude Code and Codex in rootless Podman.

Dependencies: `hi box` (boxes, worktrees, the proxy, `diff`), `hi install`
(Claude Code and Codex today; Antigravity CLI as a new tool), `hi skill`,
and for nested calls the box socket that the `hi box` and `hi ask` specs
assume but release 1 of `hi box` doesn't have. `hi server` adds audit,
the live view, and sign-ins kept on the server; it is not required.

## Goal

`hi agent` is how hi runs coding agents. It hands a task to an agent in an
isolated session, waits for it, and returns the same report whichever agent
did the work. A person or an agent calls it; hi deals with each agent's
flags, sign-in, output, session IDs, and failure modes, and with cleaning up
afterwards.

```sh
hi agent "make the backtest loader handle missing days"
hi agent codex "review the diff on branch fix-loader" --json
hi agent claude                      # an interactive session in a box
```

### `hi agent` and `hi box`

Until this spec, `hi box claude|codex` started agents, so `hi box` mixed
two ideas: **where** something runs, and **who** does the work. They are
now separate commands with one job each:

| | `hi box` | `hi agent` |
|---|---|---|
| What it is | An isolated environment for any code | An isolated agent session |
| Examples | `hi box shell`, `hi box run -- python train.py --gpu` | `hi agent claude`, `hi agent "fix the flaky test"` |
| Owns | The image, worktree, network presets, proxy, GPU, `ls`, `attach`, `diff`, `allow`, `stop`, `rm` | Which agent, its sign-in, the task, the report, resume, nesting, best-of runs |
| Uses | Nothing from agents | `hi box`, always |

Every agent run is a box, so `hi box ls`, `attach`, `diff`, `stop`, and
`rm` work on agent runs as on any other box. `hi agent` never runs an agent
on the host: running `claude` or `codex` there needs no wrapper, and keeping
`hi agent` boxed is what keeps the two commands apart.

`hi box claude`, `hi box codex`, and `hi box token claude` are removed. They
print one line pointing to the `hi agent` command for a release, then go.

### Why a call, and not a background box

`hi box claude "<prompt>"` started the agent in the background and left a
branch to look at later. An agent that delegates needs a call instead:
start, wait, and get back something it can act on: did it work, what did
it say, what changed, and how to continue. With only boxes it has to poll
`ls`, read logs, and parse free text that differs per agent.

The reasons to delegate at all:

- **A second vendor.** Codex reviews what Claude Code wrote, or the other
  way round. Different agents fail in different ways.
- **A sandbox for part of the work.** An interactive agent on the host hands
  a risky or long task to a boxed agent and carries on.
- **Running out of quota.** When one agent is rate-limited, hi uses the
  next one instead of the task stopping.
- **Running somewhere else.** On the workstation or a rented machine, with
  the laptop holding no agent and no sign-in (releases 3 and 4).
- **Visibility.** Every run is a box, so it shows in `hi box ls`, and with
  a server in the audit log and `hi server live`.

Fanning out inside one agent's session is not a reason: Claude Code and
Codex have their own subagents for that, and they share context that a
separate run doesn't. The skill says so.

## Commands

```text
hi agent [claude|codex] ["<task>"]     run a task and wait for the report;
                                       without a task, an interactive session
    [--detach]                         start and return; hi agent wait gets the report
    [--json]                           the report as JSON on stdout
    [box options]                      --name, --network, --allow, --gpu, --data,
                                       --here, --image, --memory, as for hi box
hi agent wait <name> [--json]          wait for a run and print its report
hi agent token claude                  store a long-lived token from claude setup-token
```

Later releases add:

```text
    [--tier read|edit|full]            what the run may change (release 2)
    [--max <duration>]                 wall-clock limit (release 2)
hi agent resume <name> "<follow-up>"   continue the same agent session (release 2)
hi agent best-of <n> "<task>"          the same task in n boxes (hi_agent_best_of.md)
```

- **Choosing the agent.** Without a name, hi takes the first agent that is
  installed and signed in, Claude Code before Codex. Release 2 makes the
  order configurable (`agents` in `~/.config/hi/config.json`) and moves on
  to the next agent when one fails before starting work because of its
  quota or sign-in. Choosing by what the task looks like is left out on
  purpose: the order is predictable, and whoever knows better names one.
- A task that starts with `wait` or `token` needs the agent named, as
  `hi agent claude "wait for …"`.
- **While it waits**, hi says which box the agent is in, and `hi box attach
  <name>` follows it from another terminal. Ctrl+C stops waiting and leaves
  the agent running; `hi agent wait <name>` picks it up again.
- **Interactive sessions** (no task) are unchanged from `hi box claude`: the
  agent in the terminal, without permission prompts, on a new worktree.

## The report

Every run with a task ends with one report, the same for every agent:

```json
{
  "name": "loader-3",
  "agent": "codex",
  "status": "done",
  "exit_code": 0,
  "session_id": "019a…",
  "text": "the agent's final message",
  "report": {"status": "complete", "summary": "…", "tests": "pass", "follow_ups": []},
  "branch": "hi-box/loader-3",
  "changed_files": ["loader.py", "tests/test_loader.py"],
  "tokens": {"input": 24763, "output": 1220},
  "warnings": []
}
```

- **`report`** comes from a short block hi adds to the end of every task and
  parses out of the agent's final message:

  ```text
  <<<REPORT
  {"status": "complete|partial|blocked", "summary": "…", "tests": "pass|fail|not_run", "follow_ups": []}
  REPORT>>>
  ```

  A prompt contract, not each agent's JSON-schema flag, because the agents
  take and return schemas differently and a new agent may have none. A
  missing or broken block is a warning, not a failure, and the block is cut
  out of `text`.
- **`changed_files`** comes from git in the worktree: committed,
  uncommitted, and new files since the box started. Never from the agent.
- **`status`** is `running`, `done`, or `failed` (release 2 adds `timeout`).
  A non-zero exit, Claude Code's `is_error`, and an empty final message all
  become `failed`.
- **`session_id`** and **`tokens`** are there when the agent reports them;
  otherwise they are left out.
- Without `--json`, the output is the final message, then the changed files
  and the branch, with the `hi box diff` and `git merge` commands to review
  and keep the work.

Exit statuses:

| Outcome | Exit |
|---|---|
| `done`, or started with `--detach` | 0 |
| `failed` | 1 |
| Usage errors | 2 |

## Adapters

One adapter per agent, each running it in its box and turning the run into
the report. What each has to handle:

| | Claude Code | Codex | Antigravity CLI |
|---|---|---|---|
| Run | `claude -p … --output-format json` | `codex exec -o <file> …` | `agy -p … --output-format json --print-timeout <max>` |
| Without prompts | `--dangerously-skip-permissions` | `--dangerously-bypass-approvals-and-sandbox` | `--dangerously-skip-permissions` |
| Final text | `.result` | the `-o` file | `.response` |
| Session ID | `.session_id` | `session id:` in its header on stderr | `.conversation_id` |
| Resume | `--resume <id>` | `codex exec resume <id>` | `--conversation <id>` |
| Tokens | `.usage` | `turn.completed` with `--json` | `.usage` |
| Failure | exit status, `is_error` | exit status | exit status, `status` |
| Its hosts | none: through the proxy's token listener | `chatgpt.com`, `ab.chatgpt.com`, `auth.openai.com`, `api.openai.com` | to be checked |

- The agent writes its result into a folder in the box's home folder, and
  prints its final message to the box's log, so `hi box attach` shows it.
  Codex's progress goes to the log as it works; Claude Code only prints at
  the end, as before.
- **An agent's hosts belong to the agent.** The box's `locked` preset now
  allows nothing; `hi agent` adds the hosts of the agent it starts. The
  `dev` and `open` presets are otherwise unchanged.
- **Sign-ins.** Claude Code's token stays at the proxy, as in `hi box`
  today. Codex's `auth.json` is copied into the box. Release 3 can keep both
  on the hi server instead.
- An adapter checks the agent's version against the range it was tested
  with and warns outside it (release 2); `hi doctor` reports the same.

### Hermes Agent (v0.32.0)

Added on 2026-10-08, outside the numbered releases. Hermes Agent (Nous
Research) is a Python program in `~/.hermes/hermes-agent` with its own
venv, signed in to a provider with a key in `~/.hermes/.env`.

| | Hermes Agent |
|---|---|
| Run | `hermes chat --query-file <task> -Q` |
| Without prompts | `--yolo` |
| Final text | stdout, after notices that start with `⚠` |
| Session ID | `session_id:` on stderr |
| Tokens, steps, model | the newest session in `~/.hermes/state.db` |
| Its hosts | `openrouter.ai` for its public model list; model calls go through the proxy's token listener |

- Its install and the Python its venv links to are mounted read-only at
  the same paths.
- Only OpenRouter: Hermes sends `OPENROUTER_API_KEY` only to
  `openrouter.ai`, so the box's config names the proxy as a `custom`
  endpoint with the placeholder as its `api_key`. The proxy mounts the
  host's `.env` read-only and takes only that key from it.
- With a custom endpoint Hermes doesn't price the calls, so the report has
  no cost.

## Tiers and limits (release 2)

`--tier` replaces each agent's permission flags:

| Tier   | Where it runs                                      | Afterwards                  |
|--------|----------------------------------------------------|-----------------------------|
| `read` | A box on a throwaway worktree, `dev` network        | The worktree is removed     |
| `edit` | A box on a new worktree and branch, `dev` network   | The branch and box are kept |
| `full` | As `edit`, with the `open` network                  | The branch and box are kept |

Inside a box every agent already runs without permission prompts, so the
tiers are the same for every agent: the box decides what a run can reach,
not the agent's own flags. That also covers Antigravity CLI, which has no
read-only flag and soft-denies shell commands with exit status 0.

`--max` is enforced by hi with a wall-clock timer that stops the box, for
every agent, rather than relying on each agent's own turn limit; a detached
run gets the same through a small watcher. Status `timeout`, exit 5.

`resume` restarts the same box on the same worktree with the agent's session
ID, so the agent's session files in the box's home folder are still there.

## Agents calling agents (release 2)

An agent can run `hi agent` itself. On the host that just works. Inside a
box there is no hi device key and no container engine, and boxes inside
boxes are not supported, so:

- `hi` in a box talks to hi on the host through a socket mounted into the
  box. The host starts the child as a sibling box and records the parent.
  This is the socket the `hi box` spec plans for compute requests from a
  box; `hi agent` would be its first user.
- **Depth.** Each run passes `HI_AGENT_DEPTH` to its children; past 2, `hi
  agent` fails with a clear error.
- **How many at once.** At most 4 runs per user at the same time on the
  workstation; more wait in a queue.
- **Time.** A child's `--max` can't be longer than what is left of its
  parent's, and stopping a parent stops its children.
- A child's branch starts from the parent's current commit, and its report
  goes back to the parent, not to the person.

## Sign-ins on the server (release 3)

With a connected hi server, the agents' sign-ins can live on the server, so
a device needs no Claude, Codex, or Antigravity sign-in at all:

- The box proxy already swaps a placeholder for the real token. Instead of
  reading the token on the host, it signs the request with the device key
  and sends it to the server, which adds the token and passes it on, as
  `hi server ai` does for `hi q`.
- The server needs pass-through routes in each agent's own protocol:
  Anthropic's Messages API for Claude Code and OpenAI's Responses API for
  Codex, both streamed. Antigravity works only if it accepts another base
  address; that is untested.
- What the server holds is the team's choice: API keys, OpenRouter, or a
  person's own subscription token used only by that person's devices and
  agents. Each provider's terms decide what is allowed; the docs say so.
- Agent traffic is much larger than `hi q`'s, so this release adds what `hi
  server ai` left out: spend limits per user, and a fair share of each
  upstream's rate limit. If the server is down, an agent falls back to a
  sign-in on the device, if there is one.

## Running somewhere else (release 4)

`hi agent --on <machine>` runs the box on another connected machine, such as
the workstation, and `--remote <hardware>` on a rented one through `hi
compute`, when `hi box` gains remote boxes. With sign-ins on the server, the
calling device needs neither the agent nor a sign-in.

## With `hi server`

- Every run, its agent, parent, duration, and token counts go to the audit
  log, without the task or the report text.
- Running agents appear in `hi server live`, as a tree when they have
  children.
- Requests from a run carry its name, as boxes' do: `iman via Claude Code,
  in loader-3`.

## Releases

1. **The split.** `hi agent claude|codex [task]`, choosing the first
   available agent, waiting with the report, `--json`, `--detach`, `wait`,
   and `token`; agents' hosts moved out of the `locked` preset;
   `hi box claude|codex|token` removed with a pointer.
2. **Control.** Tiers, `--max`, `resume`, the configured order and moving
   on, version checks, and nested calls through the box socket with depth,
   concurrency, and time limits.
3. **Sign-ins on the server**, audit, and the live view.
4. **Elsewhere and more agents.** `--on` and `--remote`; Antigravity CLI
   (`hi install agy`, its sign-in, its adapter); `hi agent best-of`.

## Risks

- **Flags change often.** All the agents change their headless flags and
  output between releases. Adapters need tests against recorded output.
- **Cost of nesting.** A loop that keeps delegating multiplies token spend.
  The depth and concurrency limits are the defence, and the report's token
  counts make it visible.
- **Agents delegate when they shouldn't.** Handing off a task loses the
  caller's context and costs a cold start. The skill limits it to the
  reasons in Goal.
- **The report block is a request, not a guarantee.** Agents sometimes
  leave it out or break its JSON; the status and changed files don't depend
  on it.
- **Renaming.** Scripts and notes that use `hi box claude` break; the
  pointer says what to use instead.

## Open questions

1. Whether to stream progress (each agent's events turned into one format)
   while waiting, or keep pointing to `hi box attach`.
2. Whether a parent should get a child's diff in the report, or only the
   branch and changed files.
3. Whether `hi box ls` should show a run's task, or `hi agent ls` should
   exist for that.

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
  has no turn limit, and only gives structured events with `--json`.
- **Gemini CLI is on its way out.** Since 2026-06-18 it no longer serves
  individual Google AI Pro, Ultra, or free Code Assist users; enterprise
  licences get maintenance only, and new features go to Antigravity CLI.
  Antigravity reads `GEMINI.md` and `AGENTS.md`, and workspace skills move
  to `.agents/skills/`, where `hi skill` already writes.

These findings are the design above: hi depends only on what the agents
share, computes what it can itself (changed files, status), and makes the
agents equal by running every one in a box.

## Sources

- https://code.claude.com/docs/en/headless
- https://developers.openai.com/codex/noninteractive
- https://antigravity.google/docs/cli/headless/
- https://antigravity.google/docs/cli/gcli-migration/
- https://developers.googleblog.com/an-important-update-transitioning-gemini-cli-to-antigravity-cli

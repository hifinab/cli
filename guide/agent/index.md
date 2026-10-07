---
title: Hand tasks to agents
description: Hand a task to Claude Code or Codex with hi agent. The agent works without permission prompts in a box, on its own branch, and you get back one report, the same for every agent.
---

`hi agent` hands a task to a coding agent and waits for it. The agent works
in a [box](/guide/box/): a rootless container with the project and nothing
else from your home folder, a network that reaches only what is allowed,
and its own git branch. When it is done, you get one report: what it said,
whether it worked, and which files changed. The report has the same shape
whichever agent did the work.

```sh
hi agent "make the flaky test in tests/test_sync.py reliable"
```

```text
Box myproject-1: claude in ~/.local/state/hi/box/myproject-1/work (branch hi-box/myproject-1), network dev.
Claude Code is working in myproject-1. hi box attach myproject-1 follows it; Ctrl+C stops waiting, not the agent.

The test waited a fixed 100 ms for the sync thread. It now waits on the
thread's event, and passes 50 times in a row. Committed as 3f2a91c.

Changed on hi-box/myproject-1:
  tests/test_sync.py
Review with hi box diff myproject-1, keep with git merge hi-box/myproject-1, then hi box rm myproject-1.
```

## Run a task

```sh
hi agent "<task>"              # the first agent installed and signed in
hi agent claude "<task>"       # Claude Code
hi agent codex "<task>"        # Codex
```

Without an agent's name, `hi agent` takes Claude Code if it is installed and
signed in on this machine, and Codex otherwise. The agent starts from your
last commit, on a new worktree and the branch `hi-box/<name>`; uncommitted
changes in the project aren't in it, and hi says so when there are some.

## In a folder without git

Not every task is code. In a folder that isn't a git repository, even an
empty one, the agent works in the folder itself:

```sh
mkdir gpu-prices && cd gpu-prices
hi agent "collect this year's GPU rental prices from RunPod and Shadeform into prices.csv"
```

```text
Box gpu-prices-1: claude in ~/gpu-prices (not a git repository: it works in place), network dev.
…
Changed in ~/gpu-prices:
  prices.csv
```

hi lists the folder's files before the run and again after, so the report
names every file added, changed, or removed, and `hi box diff` says which.
There's no branch to throw away: what the agent writes or deletes is in
the folder at once. hi warns when the folder has more than 1,000 files or
1 GB, and refuses to work in your home folder or above it, since the box
gets the whole folder. `--here` works in place in a git repository too.

## A task from a file

Write a long task, a brief, as a markdown file and give its name instead
of the task:

```sh
hi agent briefs/gpu-prices.md
hi agent codex review.md
./make-brief.sh | hi agent claude -     # - reads the task from stdin
hi agent --task-file brief.txt          # a file whose name doesn't end in .md
```

A single word ending in `.md` is a file: its contents are the task. Two or
more words are always the task itself, so
`hi agent "fix the typo in README.md"` works as before. hi reads the file
before it makes the box, so a brief you haven't committed works too. A name
that doesn't exist stops hi with an error, instead of being sent to the
agent as the task. A task file can be at most 1 MB, and the report's
`task_file` says which file it was.

## While it works

`hi agent` waits until the agent is done. While it works,
`hi box attach <name>` in another terminal follows it. Codex shows its
progress; Claude Code prints only its final answer. Ctrl+C stops waiting,
not the agent:

```sh
hi agent wait myproject-1      # wait again and print the report
```

The box options from `hi box` work here too: `--name`, `--network`,
`--allow`, `--gpu`, `--data`, `--here`, `--image`, and `--memory`. See
[Run code in a box](/guide/box/).

## In the background

```sh
hi agent codex --detach "upgrade FastAPI and fix what breaks"
# … later
hi agent wait myproject-2
```

`--detach` starts the agent and returns at once. `hi agent wait` waits for
it, if it's still working, and prints the report.

## The report as JSON

`--json` prints the report on stdout and everything else on stderr, for
scripts and for agents that hand work to other agents:

```sh
hi agent codex --json "review the change on this branch; don't edit files"
```

```json
{
  "name": "myproject-3",
  "agent": "codex",
  "status": "done",
  "exit_code": 0,
  "session_id": "019a…",
  "text": "The change looks right, but parse_date drops the time zone…",
  "report": {"status": "complete", "summary": "…", "tests": "pass", "follow_ups": ["…"]},
  "branch": "hi-box/myproject-3",
  "changed_files": [],
  "warnings": []
}
```

| Field           | What it is                                                                                 |
|-----------------|--------------------------------------------------------------------------------------------|
| `status`        | `done`, `failed`, or `running` (from `--detach`)                                           |
| `exit_code`     | The agent's exit status                                                                    |
| `text`          | The agent's final message                                                                  |
| `report`        | The agent's own summary: `status` (`complete`, `partial`, `blocked`), `summary`, `tests`, `follow_ups`; `null` if it didn't give one |
| `changed_files` | Every file changed since the start, committed or not, from git; outside git, from the folder's files before and after |
| `folder`        | The folder the agent worked in, when it worked in place                                    |
| `session_id`    | The agent's session, when it reports one                                                   |
| `task_file`     | The file the task came from, when it came from one                                         |
| `tokens`        | Input and output tokens, when the agent reports them (Claude Code)                         |
| `warnings`      | Anything hi noticed, such as a missing summary                                             |

hi asks every agent to end with a short summary block, and takes it out of
`text` into `report`. The changed files come from git, not from the agent,
so they are right even when the agent is wrong about what it did.

`hi agent` exits with 0 when the agent is done, and 1 when it failed: a
non-zero exit, an error from the agent, or no answer at all.

## Work with an agent yourself

```sh
hi agent claude
hi agent codex
```

Without a task, the agent opens in your terminal, in a box, without
permission prompts. When you quit, `hi box diff` shows what it did.

## Sign-ins

**Claude Code** never sees your token. The box gets a placeholder, and hi's
proxy outside the box swaps in your Claude sign-in for requests to
Anthropic. The proxy reads the sign-in from Claude Code on your machine, so
it stays fresh while you use Claude there. For long unattended runs, store a
token that lasts a year:

```sh
claude setup-token          # prints a token
hi agent token claude       # paste it; kept in ~/.config/hi, never in a box
```

**Codex** gets a copy of its sign-in (`~/.codex/auth.json`) in the box's
home folder for now, and its own hosts (`chatgpt.com` and OpenAI's) on top
of the box's network preset. A refresh token works only once, so when
Codex refreshes its sign-in in the box, hi copies the new one back to
`~/.codex/auth.json` when the run ends or the box is removed. Otherwise
Codex on your machine would stop working.

## Review and clean up

Every agent run is a box, so the `hi box` commands work on it:

```sh
hi box ls                    # every box, its agent, state, branch, and changes
hi box diff myproject-1      # commits and files; --full for the whole diff
git merge hi-box/myproject-1 # keep the work
hi box rm myproject-1        # keeps the branch if it has commits
```

`diff` marks files that run on your machine later, such as a `Makefile`,
`package.json`, `.envrc`, CI workflows, and editor tasks. Read those before
you run anything from the branch.

## Giving it a good task

The agent works unattended, so give it a task it can finish and check on
its own: "make `make check` pass", "add tests for `parse_config` and fix
what they find", or "upgrade FastAPI and fix what breaks". A vague task
such as "fix something" gives a vague result.

Hand a task to another agent when it helps: a second opinion from another
vendor (Codex reviewing what Claude Code wrote), a long or risky task that
should run in a box while you carry on, or one agent out of quota. Don't use
it to split one task into pieces: the agents' own subagents do that better,
because they share what they already know.

## How it works, step by step

```sh
cd ~/projects/myproject
hi agent claude "make the flaky test in tests/test_sync.py reliable"
```

1. **It checks the agent.** Claude Code must be installed and signed in on
   this machine; hi says what is missing before it makes anything.
2. **It makes a box**, as `hi box` does: it picks rootless Podman (or
   Docker, with a warning), reads `devcontainer.json`, names the box
   `myproject-1`, and checks out your last commit into a new worktree on
   the branch `hi-box/myproject-1`.
3. **It writes the allowlist** from the network preset, the agent's own
   hosts, `--allow`, and the project's domains if you allowed them.
4. **It starts the proxy**, the box's only way out, on a private network
   with no route out. It is the only container that can read your Claude
   sign-in.
5. **It starts the agent in the box**, in the background:
   - It sees the worktree, your repository's `.git` with `hooks` and
     `config` read-only, an empty home folder of its own, and the `claude`
     binary from your machine, read-only. Nothing else from your home
     folder.
   - The Claude token in the box is the word `hi-box-placeholder`, and
     `ANTHROPIC_BASE_URL` points at the proxy, which puts your real token in
     its place on the way to Anthropic.
   - The task and the summary request go in a file in the box's home
     folder, and from there to the agent on stdin, so a task of any length
     fits. The command is `claude -p --output-format json
     --dangerously-skip-permissions`; the result goes to a file in the
     box's home folder, and the final text to the box's log. Codex runs as
     `codex exec --dangerously-bypass-approvals-and-sandbox -o <file> -`.
6. **It waits** for the box to stop, then stops the proxy.
7. **It writes the report** from the agent's result file, the box's exit
   status, and `git diff` against the commit it started from.

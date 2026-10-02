# `hi ask` specification

Status: Draft

Dependencies: `hi server` (devices, users, the request flow and its exit
statuses, Slack over Socket Mode, linked Slack accounts, approvers, audit),
`hi skill`, and `hi box` (an agent in a box reaches hi on the host through
its socket).

## Goal

Let an agent ask a person a question in Slack and get the answer back as
output and an exit status, the same way it already asks for compute. It
turns the compute approval flow into one for any question.

Unattended agents are the reason. A `hi box claude "fix the flaky test"`
agent with no one at the terminal has two choices today when it is unsure:
guess, or stop. Claude Code's own question tool and Codex's approvals only
work when someone is watching the terminal. `hi ask` gives a third choice:
ask the right person where they already are, and carry on when they answer.

- **A question.** "Is 2019 data OK to include in the backtest?" goes to the
  research group; the first answer comes back as text.
- **A choice.** "Which of these three fixes should I keep?" gives buttons;
  the chosen one comes back.
- **An approval.** "Run the migration on staging?" gives Approve and Deny;
  the exit status is the decision. Other tools, such as `hi call`, can
  require one before a write.
- **A heads-up.** `--no-wait` posts and returns at once, for "I've pushed
  branch `fix-flaky`, have a look when you can."

People can use it too, in scripts: a nightly job that asks before
publishing, or `make release` that waits for a second pair of eyes.

## Commands

```text
hi ask "<question>"              ask, wait for the answer, print it
    [--to <user|@group|owner|approvers>]
    [--choices "a|b|c"] [--approve]
    [--context <file|->] [--wait <duration>] [--no-wait] [--json]
hi ask ls                        your questions: open, answered, expired
hi ask wait <id> [--timeout <d>] wait for an answer to an earlier question
hi ask cancel <id>               withdraw an open question
```

- `--to` defaults to the asker's **owner**: the person an agent works for
  (the device's user, or the `--owner` of an agent enrolled with
  `hi connect --agent`). A person asking defaults to `approvers`. `@group`
  is a group from `policy.json`; `approvers` is the approvers list.
- `--choices` gives one button per choice and prints the chosen text.
  `--approve` gives **Approve** and **Deny…** and prints nothing on approval.
  Without either, the person answers in a short form with free text.
- `--context` attaches up to 8 KB of text, such as a diff summary or the
  options in detail, shown folded under the question. `-` reads stdin.
- `--wait` defaults to 10 minutes, which fits an agent's command time limit;
  then the question stays open and `hi ask` exits with the pending status
  and the ID, as `hi compute up --no-wait` does.
- Questions expire after 24 hours unanswered (`ask_expiry` in the server's
  `config.json`).

### Output and exit statuses

| Outcome | Output | Exit |
|---|---|---|
| Answered, or a choice made | The answer or choice on stdout, who answered on stderr | 0 |
| Approved (`--approve`) | Who approved, on stderr | 0 |
| Still open after `--wait`, or `--no-wait` | The question's ID | 3 (pending) |
| Denied (`--approve`) | The reason given, on stderr | 4 (denied) |
| Expired or cancelled | A note | 5 |

3 and 4 are the statuses `hi compute` already uses. 5 is new; `hi compute
requests` can use it for expired requests too. `--json` prints
`{"id", "state", "answer", "by", "at"}`.

## Slack

One message per question, in a direct message to a single person or in
`#hi-asks` for a group (a channel the admin names in `hi server slack
setup`; `#compute-approvals` is the default so nothing new is needed):

```text
❓ fix-flaky-test (iman via Claude Code, in a box) asks:
   Is 2019 data OK to include in the backtest?
   ▸ context (12 lines)
   [ Answer… ]                                     expires in 24h
```

- The message is edited in place when the question is answered, expired, or
  cancelled, and keeps who answered: `✅ answered by sara: "No, start at
  2020; 2019 has the split errors."`
- A group question takes the first answer. Everyone in the group sees who
  answered.
- **Nobody answers their own question.** For a question from an agent, its
  owner may answer (that's the common case), but an approval (`--approve`)
  needs someone other than the person the agent works for when it goes to a
  group, as compute approvals do. Sent to `owner`, the owner approves their
  own agent's request; that is the point of asking.
- Answers are checked against the hi user linked to the Slack account when
  the button is pressed. A Slack user who isn't linked to a hi user can't
  answer.
- App Home gets a **Questions** section: open questions for you and your
  groups, and your agents' questions waiting on others.

People without Slack answer in the terminal: `hi ask inbox` lists open
questions for them, and `hi ask answer <id> "<text>"` replies. Admins have
`hi server ask answer` on the server box as the fallback when Slack is down.

## Agents

- `hi skill` teaches the rule: ask when a decision is the person's to make
  or the next step can't be undone; don't ask what the code, the docs, or
  `.hifin/q.md` already answer; ask once, wait, and never ask again to get a
  different answer.
- A question from a box carries the box's name and goes through the host's
  hi over the box socket from the hi box spec, so the box holds no device
  key. `hi box ls` shows a box waiting on a question as `waiting: asked
  sara 4m ago`.
- An agent that gets the pending status should tell the person what it
  asked, carry on with work that doesn't depend on the answer if there is
  any, and call `hi ask wait <id>` later.
- Rate limits stop a looping agent from flooding Slack: at most 10 open
  questions per user and 30 per hour (`ask_limits` in `config.json`); past
  that, `hi ask` fails with a clear error.

## Protocol

Three routes, signed with the device key like every other `/v1` route:

- `POST /v1/asks` creates a question: text, kind (`text`, `choice`,
  `approve`), choices, context, target, and the agent and box names.
- `GET /v1/asks/{id}` returns its state and answer; `hi ask wait` polls it
  every 3 seconds, as `hi compute requests --wait` does.
- `POST /v1/asks/{id}/cancel` withdraws it.

Questions live in the server's state as requests of kind `ask`, with the
same fields compute requests use for who, when, and the decision. The audit
log records each question as asked, answered, expired, or cancelled, with
who and when, but not the question or answer text: they can contain data or
code. The text is kept in the state file for 30 days so `hi ask ls` can show
it, then removed.

## Releases

1. Questions, choices, and approvals to the owner or approvers in Slack;
   `ls`, `wait`, `cancel`; exit statuses; the skill rule.
2. Groups, App Home's Questions section, `inbox` and `answer` in the
   terminal, and box names.
3. Other hi tools require an approval through `hi ask` for risky actions
   (`hi call` writes, `hi data` queries over a cost limit, publishing a
   `hi box url`).

## Risks

- Agents may ask too often and wear people out. The skill rule and the rate
  limits help; the weekly report can count questions per agent so a noisy
  one shows up.
- A question can carry project data or code to Slack. Slack already sees
  compute reasons; `--context` makes it more. The docs say so, and the
  8 KB limit keeps it small.
- An answer is advice, not enforcement: nothing stops an agent from ignoring
  it. Approvals that gate a hi action (release 3) are enforced by hi itself.
- A person who approves quickly without reading is still the weak point.

## Open questions

1. Whether an agent may send a question to someone other than its owner
   without the owner seeing it first. The proposal allows it, because
   waiting for the owner defeats the purpose, and the owner is always copied
   in App Home.
2. Whether answers should also go back into Claude Code directly, through
   its Channels or a hook, so an agent waiting in the terminal sees the
   answer without polling.
3. A separate `#hi-asks` channel or the existing approvals channel. Mixing
   them keeps setup at zero but makes compute approvals harder to find.

## Findings

Checked on 2026-10-02.

- **Claude Code Channels** (research preview) push events into a running
  session through a local MCP server; the official ones are Telegram,
  Discord, and iMessage, with no official Slack channel. With the
  `claude/channel/permission` capability, a session sends permission
  requests to the channel and takes `allow` or `deny` back, while the
  terminal prompt stays open and the first answer wins. Team and Enterprise
  admins must turn on `channelsEnabled`. A hi channel that relays these to
  Slack is a natural later step (open question 2).
- **Claude Code hooks**: `PermissionRequest` can allow, deny, or rewrite a
  tool call; `Notification` fires on `permission_prompt`, `idle_prompt`, and
  `agent_needs_input` but can't decide anything. Hooks time out after
  600 seconds by default. Answering Claude Code's own question tool from a
  hook is not documented.
- **Claude in Slack** starts a cloud session; it doesn't relay a local
  session's questions. Slack's own agent features are a UI for agents, with
  no "ask a person and wait".
- **MCP elicitation** is the protocol's way for a server to ask the user:
  a flat form schema, answered `accept`, `decline`, or `cancel`, with no
  timeout defined, and secrets must not be asked for in form mode. It reaches
  the person at the terminal, not someone else.
- **LangGraph** `interrupt()` saves state and waits indefinitely until
  resumed with a value, the same shape as `hi ask wait`.
- **HumanLayer**'s Slack approvals SDK is deprecated by its authors in
  favour of a rebuild; **gotoHuman** offers review forms through a web UI.
  Neither ties into a team's own approvers, policy, and audit.

## Sources

- https://code.claude.com/docs/en/channels,
  https://code.claude.com/docs/en/channels-reference,
  https://code.claude.com/docs/en/hooks
- https://modelcontextprotocol.io/specification/2025-11-25/client/elicitation
- https://docs.langchain.com/oss/python/langgraph/interrupts
- https://github.com/humanlayer/humanlayer,
  https://glama.ai/mcp/servers/@gotohuman/gotohuman-mcp-server
- https://learn.chatgpt.com/docs/developer-commands?surface=cli
- https://api.slack.com/docs/apps/ai

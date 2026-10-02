# `hi server ai` specification

Status: Approved (2026-10-02). Release 1 shipped in v0.21.0 and release 2
in v0.22.0, which also added `--no-key` for upstreams without a key.

Dependencies: `hi server` and `hi connect` (device keys, users, audit,
spend), and `hi q` v0.20.

## Goal

A team's hi server passes `hi q`'s model requests through to OpenRouter
with the team's key, so a connected device needs no key and no setup. The
key stays on the server, and each request is signed by a device the team
approved.

It is a plain pass-through: users choose any model the upstream offers,
and the server doesn't limit models, budgets, or request rates. The team
trusts its users, and `hi q`'s volume is small. The server still records
who used what and what it cost, so the bill can be explained.

What it gives a team:

- **No keys on laptops.** One OpenRouter key, stored once on the server.
  Removing a user or a device ends their access at once, with no key to
  rotate.
- **One bill**, with spend per user in `hi server spend`.
- **Nothing to set up** on a connected device: `hi q` uses the server by
  itself.
- **Agents too.** Agents enrolled with `hi connect --agent` use it like
  people, and their requests are recorded under the agent and its owner.

## Commands

On the server:

```text
hi server ai                     show the setup and this month's use per user
hi server ai set [--url URL] [--model M]
                                 store the upstream key (read without echo) and the default model
hi server ai off                 stop serving; keeps the key
hi server ai remove              stop serving and delete the key
hi server spend [--since 30d]    gains an AI column next to compute
```

`--url` defaults to OpenRouter (`https://openrouter.ai/api/v1`); any
OpenAI-compatible endpoint works. `--no-key` is for one that takes no key,
such as a model served on the team's own machine; then no Authorization
header is sent. `--model` is the model `hi q` suggests
and uses when the user hasn't chosen one; it defaults to
`anthropic/claude-haiku-4.5`. `set` makes one small request with the key
before saving it, as `hi q --setup` does. The key is kept with the other
provider keys in `keys.json`.

On a connected device, nothing new: `hi q` finds the server by itself, and
`hi q --setup` lists it.

## The menu in `hi q --setup`

When the device is connected and the server serves a model, it is the first
option and marked recommended:

```text
Which model should hi q use?
❯ Your team's hi server (vmhiserver): no key needed (recommended)
  Claude Code: your Claude sign-in, a few seconds per answer
  OpenRouter: one key for models from Anthropic, OpenAI, Google, DeepSeek, …
  An OpenAI-compatible endpoint: OpenAI, Ollama, vLLM, llama.cpp, …
  An Anthropic API key
  Cancel
```

Choosing it lists the upstream's models that can call tools, as the
OpenRouter option does today, with the server's default first, so Enter
takes the default. The choice is saved in `~/.config/hi/q.json` as provider
`server` and the model; `--model` changes it for one question.

When the device is connected but the server serves no model, the menu says
so in one line ("Your hi server doesn't serve a model yet; an admin can turn
it on with `hi server ai set`") and doesn't offer it.

## Choosing a provider

The order from the `hi q` spec gains one step:

1. `--provider`, or the provider saved by `hi q --setup`.
2. `HI_Q_BASE_URL` / `HI_Q_MODEL`.
3. **New:** the hi server this device is connected to, if it serves a
   model, with the server's default model.
4. `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`.
5. Claude Code.

Step 3 asks the server (`GET /v1/ai`, 1.5 second timeout) only when nothing
above it is set, and caches the answer for an hour in
`~/.local/state/hi/q/server.json`, so an unreachable server costs one short
wait, not one per question. `--provider server` forces it. `hi q --status`
shows "anthropic/claude-haiku-4.5 via vmhiserver".

The team's server comes before personal keys on purpose: a team member with
an old `OPENAI_API_KEY` in their shell uses the team's key unless they
choose otherwise in `hi q --setup`.

### When the server can't be used

When the server can't be reached, answers with a 5xx, or no longer serves a
model, `hi q` falls back to the next personal provider in the order above:
`HI_Q_BASE_URL`, then `OPENAI_API_KEY`, `OPENROUTER_API_KEY`,
`ANTHROPIC_API_KEY`, then Claude Code. It says so in one dim line, such as
"vmhiserver can't be reached; using OPENROUTER_API_KEY", and remembers the
failure for five minutes so later questions don't wait for the server
again. This also applies when the server was saved by `hi q --setup`. With
no personal provider, `hi q` fails with the server's error and suggests
`hi q --setup`.

Errors from the upstream that the server passes on, such as an unknown
model or no credit, don't cause a fallback: they would happen with a
personal key too, and falling back would hide them.

## Protocol

Three device routes, signed with the device key like every other `/v1`
route:

- `GET /v1/ai` returns whether a model is served and the default model.
- `GET /v1/ai/models` returns the upstream's `/models` list as it is, for
  the menu.
- `POST /v1/ai/chat/completions` takes an OpenAI chat-completions body,
  which is what `hi q` already sends.

For a request, the server:

1. checks that AI is on; an empty model becomes the default;
2. forwards the body to `<url>/chat/completions` with the upstream key,
   with `stream` forced off and nothing else changed;
3. returns the upstream's answer and status as they are, so the upstream's
   own errors (an unknown model, no credit) reach `hi q` unchanged;
4. records the user, device, model, tokens, and cost from the answer's
   `usage`. OpenRouter reports the cost there; for other upstreams the cost
   is left empty.

The signed body limit rises from 1 MB to 4 MB on this route: a long chat
with tool results can pass 1 MB. Requests time out after 90 seconds, as in
`hi q`.

## Privacy

Prompts carry the user's shell history, folder names, and command output.
The server passes them on and keeps none of it:

- Usage records and the audit log hold the user, device, model, token
  counts, cost, and status. Never the messages or the answer.
- The server writes no request bodies to disk or to its log, also not on
  errors.
- The docs say plainly that the team's server, and whoever runs it, can see
  prompts in transit, as the upstream provider can. Someone who doesn't
  want that keeps their own key or uses Claude Code.

## Records

Usage goes to `ai_usage.jsonl` in the server's state folder, one line per
request: time, user, device, agent (if any), model, prompt and completion
tokens, cost, and status. `hi server ai` sums this month's per user; `hi server spend` adds
it next to compute. The audit log gets `ai set`, `ai off`, and `ai remove`,
but not each request.

## Releases

1. Server: `hi server ai`, `set`, `off`, `remove`, the three routes, usage
   records, and the AI column in `hi server spend`. Device: provider
   `server`, discovery, the first option in the menu with the model list,
   and `--status`.
2. AI spend in `hi server live` and the Slack App Home.

## Risks

- A runaway script or a mistake in `hi q` can send many requests. Nothing
  stops it but the upstream's own credit limit; set one on the OpenRouter
  key (OpenRouter keys can have a spending limit) as the backstop.
- An expensive model chosen by mistake costs more than expected. The cost
  is visible per user in `hi server spend`.
- If the server is down, `hi q` falls back to a personal key or Claude Code
  when there is one, which changes who pays and where prompts go; the dim
  line says so each time. Without one, it stops working until the server is
  back.
- A compromised device, or an agent that misbehaves, can use the key until
  it is removed. The usage records show which device or agent made each
  request.
- Prompts pass through the team's server (see Privacy).

## Decisions

1. Agents enrolled with `hi connect --agent` may use the endpoint, recorded
   under the agent and its owner.
2. When the server can't be used, `hi q` falls back to a personal provider,
   and fails only when there is none.

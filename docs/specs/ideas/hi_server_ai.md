# `hi server ai` specification

Status: Draft (2026-10-02)

Dependencies: `hi server` and `hi connect` (device keys, users, groups,
audit, policy, budgets, Slack), and `hi q` v0.20.

## Goal

A team's hi server gives its connected devices a language model, the way it
already gives them GPUs: the team's key stays on the server, each request
is signed by a device the team approved, and policy, budgets, and the audit
log apply. The first user is `hi q`; after `hi connect`, it works with no
key and no setup.

What it gives a team:

- **No keys on laptops.** One upstream key, for example the team's
  OpenRouter key, stored once on the server. Removing a user or a device
  ends their access at once, with no key to rotate.
- **One bill and a default model.** The admin chooses which models each
  group may use and which one `hi q` gets by default.
- **Limits.** Monthly budgets per user and group and a rate limit, so a
  loop in a script can't spend the month's money in an hour.
- **The team's own GPUs later.** The upstream can be a model on the
  workstation or one rented with `hi compute serve`, behind the same
  endpoint and rules.

## Commands

On the server:

```text
hi server ai                               show the setup, today's and this month's use
hi server ai set [--url URL] [--model M]   store the upstream key (read without echo) and settings
hi server ai models <m>...                 the models devices may ask for; the first is the default
hi server ai off                           stop serving; keeps the key
hi server ai remove                        stop serving and delete the key
hi server spend [--since 30d]              gains an AI column next to compute
```

`--url` defaults to OpenRouter (`https://openrouter.ai/api/v1`); any
OpenAI-compatible endpoint works, including a local llama.cpp or vLLM. `set`
makes one small request with the key before saving it, as `hi q --setup`
does.

On a connected device, nothing new: `hi q` finds the server by itself, and
`hi q --setup` lists it.

## The menu in `hi q --setup`

When the device is connected and the server serves a model, it is the first
option and marked recommended:

```text
Which model should hi q use?
❯ Your team's hi server (vmhiserver): claude-haiku-4.5, no key needed (recommended)
  Claude Code: your Claude sign-in, a few seconds per answer
  OpenRouter: one key for models from Anthropic, OpenAI, Google, DeepSeek, …
  An OpenAI-compatible endpoint: OpenAI, Ollama, vLLM, llama.cpp, …
  An Anthropic API key
  Cancel
```

When the server allows more than one model for the user's group, choosing
it lists them, with the default first. When the device is connected but the
server serves no model, the menu says so in one line ("Your hi server
doesn't serve a model yet; an admin can turn it on with `hi server ai
set`") and doesn't offer it.

## Choosing a provider

The order from the `hi q` spec gains one step:

1. `--provider`, or the provider saved by `hi q --setup`.
2. `HI_Q_BASE_URL` / `HI_Q_MODEL`.
3. **New:** the hi server this device is connected to, if it serves a
   model.
4. `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`.
5. Claude Code.

Step 3 asks the server once (`GET /v1/ai`, 1.5 second timeout) only when
nothing above it is set, and the answer is cached for an hour in
`~/.local/state/hi/q/server.json`, so an unreachable server costs one
short wait, not one per question. `--provider server` forces it.
`hi q --status` shows "team model via vmhiserver".

The team's server comes before personal keys on purpose: a team member
with an old `OPENAI_API_KEY` in their shell should use the team's model
and budget unless they choose otherwise in `hi q --setup`.

## Protocol

Two device routes, both signed with the device key like every other
`/v1` route:

- `GET /v1/ai` returns whether a model is served, the models this device's
  group may use, the default, and this month's spend and budget for the
  user.
- `POST /v1/ai/chat/completions` takes an OpenAI chat-completions body,
  which is what `hi q` already sends.

For a request, the server:

1. checks that AI is on, the user's group may use the requested model (an
   empty model means the default), the user and group are within budget,
   and the device is within its rate limit;
2. forwards the body unchanged, apart from the model, to
   `<url>/chat/completions` with the upstream key, and `stream` forced off;
3. returns the upstream's answer and status as they are;
4. records the user, device, model, tokens, and cost from the answer's
   `usage` (OpenRouter reports cost there; for other upstreams, cost comes
   from per-model prices in the settings, or is unknown).

Errors come back in OpenAI's error shape, so `hi q` shows them as it does
an upstream's: `403 your group may not use anthropic/claude-opus-5.5; ask
for it or use claude-haiku-4.5`, `429 you have used $10.00 of your $10.00
AI budget this month`.

The signed body limit rises from 1 MB to 4 MB on this route: a long chat
with tool results can pass 1 MB.

## Policy

`policy.json` gains an `ai` block per group, next to compute:

```json
"groups": {
  "quant": {
    "ai": {
      "models": ["anthropic/claude-haiku-4.5", "google/gemini-2.5-flash"],
      "user_monthly_budget_usd": 20,
      "group_monthly_budget_usd": 200,
      "requests_per_minute": 30
    }
  }
}
```

Without a block, a group may use the default model only, with the server's
defaults: $10 per user per month and 20 requests a minute. Unlike compute
budgets, which only warn because a stopped job wastes the money already
spent, AI budgets block: each request is small, and refusing one loses
nothing. At 80% the reply carries a warning header that `hi q` shows once a
day.

## Privacy

Prompts carry the user's shell history, folder names, and command output.
The server forwards them and keeps none of it:

- The audit log and spend records hold the user, device, model, token
  counts, cost, and status. Never the messages or the answer.
- The server writes no request bodies to disk or to its log, also not on
  errors.
- The docs say plainly that the team's server, and whoever runs it, can see
  prompts in transit, as the upstream provider can. Someone who doesn't
  want that keeps their own key or uses Claude Code.

## Slack and the dashboard

- A user going over 80% or 100% of their AI budget gets a Slack message
  if they linked Slack, as for compute.
- `hi server live` and the App Home show AI spend this month per user,
  next to compute.
- No message per request.

## Releases

1. Server: `hi server ai set|models|off|remove`, the two routes, the
   default model for everyone, rate limit, per-user budget with the default
   of $10, and usage in `hi server spend`. Device: `--provider server`,
   discovery, the first option in the menu, and `--status`.
2. Policy per group, group budgets, the 80% warning, Slack messages, and
   the dashboard.
3. Upstreams beyond OpenRouter with per-model prices, and the team's own
   served models as upstreams.

## Risks

- The server becomes a shared spending point. Budgets block and the rate
  limit is on from the first release for that reason.
- If the server is down, `hi q` stops working for people relying on it. A
  personal key or Claude Code in `hi q --setup` is the fallback, and the
  error says so.
- A compromised device can spend up to its user's budget until it is
  removed. The audit log shows which device made each request.
- Prompts pass through the team's server (see Privacy).

## Open questions

1. Should agents enrolled with `hi connect --agent` get the endpoint too,
   with their own budget, or only people at first?
2. Should other tools (scripts, IDEs, Claude Code) be able to use it? They
   can't sign requests with a device key, so the server would issue
   per-user tokens. Left out of this spec until someone needs it.
3. Default budget: $10 per user per month, or no default and the admin
   must choose one in `hi server ai set`?
4. Should `hi q` fall back to a personal key automatically when the
   server is unreachable, or fail and say so? Falling back is convenient
   but silently changes who pays and where prompts go; the draft says fail.

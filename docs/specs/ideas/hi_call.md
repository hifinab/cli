# `hi call` specification

Status: Draft

Dependencies: `hi server` (keys kept on the server, devices, groups,
`policy.json`, audit, Slack), `hi ask` (approvals for writes), and `hi box`
(its proxy, which already adds Claude Code's token outside the box).

## Goal

Let agents use the team's API accounts without ever holding their keys.
The server keeps the key, adds it to each request, checks the request
against policy, and records it. This is the rule `hi server` was built on,
"broker, don't distribute", applied to every API and not only to GPU
providers.

What agents ask for today, and what happens:

- **GitHub.** An agent wants to read another private repository's code, an
  issue, or CI logs. Today it uses the person's `gh` token, which can do
  anything the person can, including pushing and deleting. Inside a box it
  has no token at all.
- **Hugging Face.** A gated model needs a token to download. The token can
  also write to the organization's repositories.
- **Data vendors and internal services.** A market data REST API, the
  team's own services. Their keys end up in `.env` files that agents can
  read, and there is no record of what was called.

`hi call` gives agents one command for all of these, with keys that never
leave the server.

## Commands

On a device:

```text
hi call <service> <METHOD> <path>   make a request through the server
    [-q key=value]... [-d @file | -d - | -d '<json>']
    [-H 'Name: value'] [--reason "<why>"] [--json] [--out <file>]
hi call ls                           services this device may use, and what they allow
hi call log [--since 1d]             your recent calls
```

On the server box:

```text
hi server call add <service> --url <base> --auth <kind>    store a key (read without echo)
hi server call remove|list [<service>]
hi server call test <service>                              one harmless request
```

Examples:

```sh
hi call github GET /repos/hifinab/templates/contents/README.md
hi call github GET /repos/hifinab/cli/actions/runs -q status=failure
hi call hf GET /api/models/unsloth/Qwen3-8B-GGUF
hi call vendor GET /v2/prices -q symbol=ERIC-B -q from=2026-09-01 --out prices.json
```

The response body goes to stdout (or `--out`), the status to stderr, and a
non-2xx status gives exit status 1 with the body still printed, so the
agent sees the API's error.

## Authentication kinds

| `--auth` | What the server adds |
|---|---|
| `bearer` | `Authorization: Bearer <key>` |
| `header:<Name>` | `<Name>: <key>`, for APIs such as `X-Api-Key` |
| `basic` | HTTP basic with a stored user and password |
| `query:<name>` | `?<name>=<key>`, for old APIs; such URLs never go to the log |
| `github-app` | A GitHub App installation token minted per request (below) |

**GitHub** gets special care, because it is the most common and the most
dangerous. The recommended setup is a GitHub App installed on the
organization with read-only permissions (contents, issues, pull requests,
actions). The server keeps the App's private key and mints an installation
token on demand, which GitHub limits to one hour and to the App's
permissions and repositories; the server can narrow each token further to
the one repository a call names. A fine-grained personal token with
read-only access, as the template sources already use, is the simpler
alternative.

## Policy

`policy.json` gains a `calls` section per group. Without it, a group may
use no service.

```json
"staff": {
  "calls": {
    "github": { "allow": ["GET *"], "approve": ["POST /repos/*/issues", "POST /repos/*/issues/*/comments"] },
    "hf":     { "allow": ["GET *"] },
    "vendor": { "allow": ["GET /v2/prices", "GET /v2/symbols"], "per_hour": 600 }
  }
}
```

- `allow` lists method and path patterns that go through at once; `*`
  matches one or more path segments.
- `approve` lists patterns that go through after a person approves through
  `hi ask --approve`, sent to the caller's owner, showing the method, path,
  and body. Anything matching neither is refused with the reason.
- `per_hour` limits calls per user per service. Over it, calls are refused
  until the hour turns, and the channel gets one alert.
- As elsewhere, hi checks the file before saving and rejects unknown fields.

## Inside a box

A box has no device key; it reaches hi on the host through its socket, so
`hi call` works the same inside. The box's proxy can also do it without
`hi call` for tools that already speak an API: with `customizations.hi`
asking for `github`, the proxy gives the box a placeholder `GH_TOKEN`, and
for `api.github.com` only it swaps the placeholder for a fresh token from
the server, exactly as it does for Claude Code's token. `gh` and `git` over
HTTPS then work for what policy allows, and nothing else. Over plain HTTPS
the proxy can't see paths, so this mode is limited to services where the
token itself is narrow (a read-only App token for named repositories);
path rules need `hi call`, or `ANTHROPIC_BASE_URL`-style plain HTTP to the
proxy where the tool supports a base URL.

## Records

Each call is audited with the user, device, agent, service, method, path
(without query values for `query:` auth), status, bytes, and duration.
Bodies are never stored. `hi server spend` doesn't apply; the weekly report
gets a line with calls per service and user.

Responses pass through unchanged, except that the server replaces the key
with `[redacted]` if an API echoes it back. Responses over 20 MB are cut,
with an error, so a mistaken call can't move a whole dataset through the
server at once.

## Protocol

`POST /v1/call` with `{service, method, path, query, headers, body,
reason}`, signed like every `/v1` route, with the signed body limit raised
to 4 MB as for AI. The server answers with the upstream's status, headers
(a safe subset: content type, pagination links, rate limit headers), and
body. `GET /v1/call` lists the services and rules for this device's group.

## Releases

1. `hi call`, `ls`, `log`; `hi server call add|remove|list|test`; bearer,
   header, and basic auth; `allow` rules and `per_hour`; audit.
2. GitHub App tokens narrowed per call; `approve` rules through `hi ask`.
3. The box proxy adds tokens for `github` and `hf` transparently.

## Risks

- **An allowed call can still leak.** An agent allowed to read the private
  repository can copy it anywhere it can reach. Policy decides what is
  readable; it can't decide what happens next.
- **Path patterns are coarse.** `GET *` on GitHub includes every private
  repository the token reaches. Narrow tokens matter more than rules.
- **The server becomes a target** with more keys on it. It already holds
  provider keys on an encrypted disk, reachable only over NetBird; the same
  protections apply, and each key should be as narrow as the API allows.
- **Prompt injection through responses.** An issue body or a web page in an
  API response can carry instructions. Responses are data, and the skill
  says so; rules that need approval for writes limit what a misled agent can
  do.

## Open questions

1. Whether to offer OAuth for services where each person has their own
   account (Google Drive, Notion), with the server holding per-user refresh
   tokens. That's a much larger feature; the proposal starts with team keys.
2. Whether `hi call` should speak MCP for services that offer an MCP server,
   with the server holding the credentials, instead of raw HTTP.
3. Whether `per_hour` should also cap bytes, for data vendors that bill by
   volume.

## Findings

Checked on 2026-10-02.

| Product | How the credential is added | What the agent sees | Rules |
|---|---|---|---|
| Docker Sandboxes | Host proxy matches service and domain and overwrites the auth header | A placeholder such as `proxy-managed` | Per service |
| Fly Connectors (Sprites) | Encrypted in Fly's control plane, added in transit; OAuth refresh handled | Nothing | Default deny, by label; paths allowed or blocked |
| E2B | `Secret.fill` per-host HTTPS transforms; nothing on plain HTTP | Nothing; an unresolved reference drops the header | Per host |
| BoxLite | Host keychain | A placeholder `<BOXLITE_SECRET:name>` | Per secret |
| Arcade, Composio | Their service calls the API or returns an authorized client | Never the token | Per tool |

- **MCP authorization** (2025-11-25) requires tokens bound to one resource
  (RFC 8707) and forbids an MCP server from passing a client's token on
  ("MUST NOT accept or transit any other tokens"). A broker holding its
  own credentials, as here, fits that rule.
- **GitHub App installation tokens** expire after one hour and can be
  narrowed per token to some permissions (such as `contents: read`) and up
  to 500 repositories, never beyond the installation's own. Fine-grained
  personal tokens can be limited to repositories and read-only contents,
  and organizations can require approval and cap their lifetime.

What hi adds to these: the rules live in the team's `policy.json`, writes
can wait for a person in Slack, and every call lands in the same audit log
as compute, for agents in a box and outside one.

## Sources

- https://docs.docker.com/ai/sandboxes/security/credentials/
- https://fly.io/connectors
- https://docs.e2b.dev/secrets/inject
- https://docs.boxlite.ai/manage-sandbox/secrets-and-security
- https://docs.arcade.dev/home/auth/how-arcade-helps,
  https://docs.composio.dev/docs/authentication
- https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization
- https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app,
  https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens

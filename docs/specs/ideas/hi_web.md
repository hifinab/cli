# `hi web` specification

Status: Draft

Dependencies: `hi install` (Lightpanda as a new tool), `hi box` (its egress
proxy), `hi server` (keys on the server, policy, audit, Slack) for signed-in
browsing, `hi llm` for crawls that feed a model, and `hi skill`.

## Goal

Give agents a browser that is fast and cheap enough to use for everything,
safe by default, and governed by the team, without building one.

Agents read the web constantly: library docs, changelogs, vendor API
references, the app they just built. Today they use `curl`, which sees no
page that needs JavaScript, or their own fetch tools, which run somewhere
else and can't reach a page on NetBird or `localhost`. When they need a real
browser, someone installs Playwright and Chrome, at 2 GB of memory per
page.

[Lightpanda](https://lightpanda.io) changes the trade-off. It is a headless
browser written for machines: it runs JavaScript, speaks the Chrome
DevTools Protocol, and draws no pixels, which makes it about 9 times faster
than headless Chrome with 16 times less memory in its own crawl benchmark
(100 pages: 5 s and 123 MB against 46 s and 2 GB). Version 1.0 shipped on
2026-10-02. It already turns pages into Markdown or an accessibility-style
outline and has its own MCP server for agents.

hi's part is not another browser or another set of browser tools. It is:

- **one install** with safe defaults: telemetry off, private networks
  blocked, the box proxy honoured;
- **short commands** for the things agents do most: read a page, check an
  app, crawl a site;
- **signed-in browsing without credentials**: the team's sessions for
  internal dashboards and vendor portals stay on the server, and agents get
  the content;
- **the team layer**: egress rules, approvals for new domains, and an audit
  of what agents read.

## Commands

```text
hi web read <url>                 the page as Markdown, after its JavaScript ran
    [--format markdown|text|tree|links|html] [--select <css>]
    [--wait load|networkidle|<css>] [--max <bytes>] [--as <session>]
hi web check <url>                problems on a page or a small site, for the app you just built
    [--crawl <depth>] [--json]
hi web crawl <url>                pages of a site as JSON Lines, for hi llm
    [--max-pages <n>] [--depth <n>] [--match <glob>] [--rate <per-second>]
hi web mcp                        Lightpanda's MCP server, with hi's settings, for interactive browsing
hi web shot <url>                 a real screenshot through headless Chromium (later)
hi web login <name> <url>         sign in once, by a person, for --as (later)
```

### `read`

`hi web read` runs `lightpanda fetch` with `--dump markdown` and
`--strip-mode clutter`, so navigation, cookie banners, and scripts are left
out, and prints the page under one header line:

```text
# https://docs.vendor.com/v2/orders · "Orders API" · 14 KB · untrusted page content
…
```

- `--format tree` gives Lightpanda's semantic outline (roles, names, and
  node IDs), which is what an agent needs to understand a form or a
  dashboard rather than to read prose. `links` lists links with their text.
- `--select` keeps one part of the page (`--dump-selector`); `--max`
  (default 100 KB) truncates with a note saying how much was cut, so a huge
  page can't flood an agent's context.
- `--wait` maps to `--wait-until` or `--wait-selector`, for pages that load
  their content late.

### `check`

For the loop "build, open, look, fix". `hi web check http://localhost:5173`
loads the page and reports, in a short list an agent can act on:

- JavaScript errors and console errors, with source and line;
- requests that failed or returned 4xx or 5xx, with their URLs;
- broken links on the page (same-origin ones are followed, others checked
  with a `HEAD`);
- elements in the semantic tree with no accessible name (buttons, links,
  form fields), and a missing title or `lang`.

It exits 0 when it found nothing, 1 otherwise. `--crawl 2` follows
same-origin links two levels deep. `--json` gives the same for scripts. A
`web` template project gains a `make smoke` target that runs it against the
dev server, and `hi box url` previews can be checked by anyone on NetBird.

What `check` can't see: layout, overlap, colours, or anything that needs
pixels. That's what `shot` is for.

### `crawl`

`hi web crawl https://docs.vendor.com --max-pages 300` writes one JSON line
per page, `{"url", "title", "markdown", "status"}`, which `hi llm map` reads
directly. It stays on the starting host unless `--match` widens it, obeys
`robots.txt` (Lightpanda's `--obey-robots`, which is off by default there
and on in hi), sends at most 2 requests per second per host by default,
and caches responses (`--http-cache-dir`) so a rerun is cheap.

Because Lightpanda is light, the Strix Halo workstation could run hundreds
of pages at once. Crawls running on the team's machine through the server,
for devices that are only laptops, are a later step.

### `mcp`

For an agent that needs to click, fill, and navigate, Lightpanda already
has the right tools in its MCP server (`goto`, `markdown`, `tree`,
`interactiveElements`, `click`, `fill`, `extract`, `consoleLogs`, sessions,
and more). hi doesn't rebuild them. `hi web mcp` starts `lightpanda mcp` with
hi's settings (below) and is what `hi skill` tells agents to add:

```sh
claude mcp add hi-web -- hi web mcp
```

## Defaults

Every Lightpanda process hi starts gets:

- `LIGHTPANDA_DISABLE_TELEMETRY=true`. Lightpanda sends telemetry by
  default; hi never does.
- `--block-private-networks`, with one exception: the host of the URL the
  agent asked for, so `hi web check http://localhost:5173` works but a page
  can't make the browser fetch from `10.0.0.5` or the cloud metadata address.
- The box's proxy, inside a box. Lightpanda honours `HTTPS_PROXY` (tested
  with 1.0.0: pointing it at a closed port made the fetch fail rather than
  go direct), so the box's allowlist applies, and a blocked domain becomes a
  question in Slack as for any other tool in the box.
- No cookies kept between runs, unless `--as` gives a session.

## Signed-in browsing (`--as`)

Agents often need pages behind a sign-in: the team's Grafana, a ticket, a
data vendor's portal, a broker's report page. Giving an agent the password
or a cookie file defeats the point of keeping credentials out of the box.

With a server, signed-in pages are read **on the server**, and only the
content comes back:

1. A person runs `hi web login vendor https://portal.vendor.com` on their
   own machine. It opens a temporary Chrome profile, the person signs in as
   they normally would (two-factor included), and hi takes the session's
   cookies from that profile through the DevTools Protocol, sends them to
   the server, and deletes the profile. The server keeps them with the
   other keys in `keys.json`.
2. An agent runs `hi web read --as vendor https://portal.vendor.com/reports/q3`.
   The device asks the server, which checks the URL against the session's
   allowed patterns and the caller's group, runs Lightpanda with the
   cookies (`--cookie`, and `--cookie-jar` to keep refreshed ones), and
   returns the Markdown. The cookie never leaves the server.
3. When the session has expired (a sign-in page comes back), the server
   tells the person who created it in Slack, with the command to sign in
   again.

`policy.json` lists the sessions each group may use and, per session, the
URL patterns (`"vendor": {"allow": ["https://portal.vendor.com/reports/*"]}`).
`--as` is read-only in the first version: `read`, `check`, and `crawl`, no
clicking or form posts. Every read is audited with the user, agent, session,
and URL, never the content.

Lightpanda's MCP server has its own answer for passwords: `$LP_*`
placeholders in tool arguments, filled from the environment, so a value is
typed without the model seeing it. That fits sites where a username and
password is enough and the server signs in itself; open question 2.

## Screenshots (`shot`)

Some checks need pixels: does the layout break on a phone width, did the
chart render, does the page look right to a person. Lightpanda's own
screenshots are text-only layouts. `hi web shot` uses Chrome's
`chrome-headless-shell` from Chrome for Testing (linux64 and linux-arm64
builds, installed with `hi install chrome-headless-shell`) for that one
job, with `--width`, `--full-page`, and an output PNG an agent can look at.
With Slack, `--to <user>` posts it in a thread for a person to judge.

## Install

`hi install lightpanda` downloads a pinned release from GitHub (amd64 and
arm64 Linux builds), checks its checksum, and installs it per user in
`~/.local/bin`, like the other single-binary tools. The 1.0.0 binary is
about 188 MB because it ships with debug information; hi may strip it.
Lightpanda is AGPL-3.0, so hi runs it as a separate program and never
embeds or links it. `hi update` doesn't update it; `hi install lightpanda`
with "Update everything" does, to the version hi has tested.

## Implementation

hi talks to Lightpanda in two ways, both without a new Go dependency:

- `lightpanda fetch` with its flags, for `read` and `crawl`.
- `lightpanda mcp` over stdio, for `check` (console logs, links, the tree)
  and for `hi web mcp` itself, which passes JSON-RPC through and adds the
  defaults.

If `check` needs network events MCP doesn't expose, hi can speak the
DevTools Protocol directly to `lightpanda serve` over the WebSocket library
it already has; `chromedp` is documented by Lightpanda as compatible if
that grows.

## Releases

1. `hi install lightpanda`; `read` with formats, selection, and truncation;
   `check` on one page; the defaults; the skill rule.
2. `crawl`; `check --crawl`; `hi web mcp`; `make smoke` in the `web`
   template.
3. Signed-in browsing on the server: `login`, `--as`, policy, audit, and
   expiry messages.
4. `shot` through `chrome-headless-shell`, and crawls on the team's machine.

## Risks

- **Prompt injection.** Pages are written by strangers and can carry
  instructions, also in text people can't see. Anthropic reports an attack
  success rate of 11.2% for its own browsing agent even with safeguards;
  Brave and OpenAI describe it as a lasting, unsolved problem. hi labels
  every page as untrusted content, keeps credentials out of reach, blocks
  private networks, and keeps `--as` read-only. It can't make an agent
  ignore what it reads, and the skill says to treat page content as data.
- **Sites break.** Lightpanda passes a large part of the Web Platform Tests
  but not all of it, and has open issues with some Playwright flows. A page
  that doesn't work is reported as such, and `shot` (Chromium) is the
  fallback for pages that need a full browser.
- **Signed-in sessions are powerful.** A team session reaches whatever that
  account can see. URL patterns, read-only use, and the audit limit it; the
  account used should be the narrowest the site offers.
- **Being a good citizen.** Crawls can hammer small sites. `robots.txt`, a
  per-host rate, and a page limit are on by default.

## Open questions

1. Whether `read` should replace the agents' own fetch tools in the skill,
   or only be used for pages those tools can't read (JavaScript, NetBird,
   `localhost`).
2. Whether the server should sign in itself with stored credentials and
   Lightpanda's `$LP_*` placeholders, for sites without two-factor, so
   sessions renew without a person.
3. Whether `check` should become part of `hi agent best-of`'s check for web
   projects.
4. Whether to strip the 188 MB binary, and whether that is allowed without
   rebuilding (it is AGPL, so redistribution of a modified binary needs the
   source offer; downloading it on the user's machine and stripping there
   avoids redistribution).

## Findings

Checked on 2026-10-02 against primary sources, and by running the Lightpanda
1.0.0 Linux binary.

### Lightpanda

- **Version.** 1.0.0 released on 2026-10-02, after 0.3.6 to 0.4.1 between
  July and September; nightly builds on a rolling tag.
- **Commands.** `fetch`, `serve` (CDP; WebDriver BiDi and, since 1.0,
  classic WebDriver), `mcp` (stdio, or HTTP with one session per
  `Mcp-Session-Id`), `agent`, `run`, `version`.
- **Dump formats.** `html`, `markdown`, `semantic_tree` (JSON),
  `semantic_tree_text`, and text-only `png` and `pdf`. Options include
  `--strip-mode clutter|shell|js|css|ui|invisible`, `--dump-selector`,
  `--dump-max-bytes`, `--json`, `--wait-until load|networkidle|done`,
  `--wait-selector`, and `--terminate-ms`.
- **Network.** `--http-proxy`, `--proxy-bearer-token`, and (undocumented,
  tested) `HTTP_PROXY`/`HTTPS_PROXY`; `--block-private-networks`,
  `--block-cidrs`, `--block-urls`, `--adblock-lists`, `--ca-cert`,
  `--obey-robots` (off by default). CORS is enforced since 1.0.
- **State.** `--cookie` loads CDP JSON or Netscape cookie files and
  `--cookie-jar` saves them on exit; no flag found for localStorage or
  IndexedDB. `--http-cache-dir` caches responses.
- **Telemetry.** On by default; `LIGHTPANDA_DISABLE_TELEMETRY=true` turns it
  off.
- **Compatibility.** Puppeteer, Playwright, and chromedp are documented.
  Open issues: Playwright Test over CDP needs workarounds (#3076), and
  `scrollIntoViewIfNeeded` is a no-op, so some clicks fail (#3656).
  ServiceWorker is experimental.
- **License and builds.** AGPL-3.0; Linux and macOS on x86_64 and aarch64,
  `.deb` packages, Homebrew, Docker; glibc only.

### Other browsers for agents

| Tool | What the model sees | License | Where |
|---|---|---|---|
| Playwright MCP | Accessibility snapshot; `--secrets` redaction | Apache-2.0 | Local |
| Vercel agent-browser | Accessibility snapshot with refs, screenshots, Markdown; experimental Lightpanda engine; encrypted auth vault | Apache-2.0 | Local |
| browser-use | DOM extraction, optional screenshots; `sensitive_data` placeholders | MIT | Local or cloud |
| Stagehand | Trimmed accessibility tree | MIT | Local Chrome or Browserbase |
| chrome-headless-shell | Whatever the driver asks for, pixels included | BSD | Local |
| Obscura | DOM, Markdown, CDP; Rust and V8 | Apache-2.0 | Local |
| Cloudflare Browser Rendering `/markdown` | Markdown | Proprietary | Hosted |
| Jina Reader | Markdown, text, screenshot | Apache-2.0 | Hosted or self-hosted |

Credentials elsewhere: Browserbase Contexts persist encrypted browser state
per session; 1Password's agentic autofill delivers credentials just in time
with a person's approval and never to the model; browser-use and
Lightpanda use placeholders the model sees instead of values. None of them
keep the session itself on a team server the way `--as` does.

Go libraries: chromedp works against Lightpanda and is documented by it;
rod needed fixes and is less maintained.

## Sources

- Lightpanda: https://github.com/lightpanda-io/browser,
  https://github.com/lightpanda-io/browser/releases/tag/1.0.0,
  https://lightpanda.io/docs/usage/mcp, https://lightpanda.io/docs/usage/agent,
  https://lightpanda.io/docs/usage/cdp/chromedp,
  https://github.com/lightpanda-io/demo/blob/main/BENCHMARKS.md#crawler-benchmark,
  https://perf.lightpanda.io/wpt,
  https://github.com/lightpanda-io/browser/issues/3076,
  https://github.com/lightpanda-io/browser/issues/3656
- Alternatives: https://github.com/microsoft/playwright-mcp,
  https://github.com/vercel-labs/agent-browser,
  https://github.com/browser-use/browser-use,
  https://github.com/browserbase/stagehand,
  https://developer.chrome.com/blog/chrome-headless-shell,
  https://github.com/h4ckf0r0day/obscura,
  https://developers.cloudflare.com/browser-rendering/rest-api/markdown-endpoint/,
  https://github.com/jina-ai/reader
- Credentials: https://docs.browserbase.com/features/contexts,
  https://1password.com/blog/closing-the-credential-risk-gap-for-browser-use-ai-agents,
  https://docs.browser-use.com/customize/sensitive-data
- Prompt injection: https://www.anthropic.com/research/prompt-injection-defenses,
  https://openai.com/index/hardening-atlas-against-prompt-injection,
  https://brave.com/blog/unseeable-prompt-injections/

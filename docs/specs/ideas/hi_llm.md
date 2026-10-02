# `hi llm` specification

Status: Draft

Dependencies: `hi server ai` (the signed pass-through, usage records, spend),
a model served on the team's own machine (`halogen-qwen3.8-flash-next` on
aiw11 since v0.22.0, or `hi compute serve`), and `hi q`'s provider order for
devices without a server.

## Goal

Give agents a cheap worker for bulk text work, so the expensive coding agent
keeps the judgement and hands the volume to a model on the team's own
hardware.

A coding agent that has to read 500 scraped pages, classify 3,000 log
lines, or pull fields out of 200 PDFs today either pushes all of it through
its own context, which is slow, costly, and crowds out the real task, or
writes a one-off script against some API with a key it shouldn't hold.
`hi llm` is a Unix filter instead:

```sh
hi web crawl docs.vendor.com --max-pages 300 \
  | hi llm map "extract: endpoint, method, params, rate limit" --fields endpoint,method,params,rate_limit \
  > api.jsonl
```

With the team's local model, the marginal cost is electricity, the data
never leaves the team's machines, and the agent reads 300 small JSON lines
instead of 300 pages.

## Commands

```text
hi llm "<prompt>"                      one answer, for quick questions in scripts
hi llm map "<instruction>"             one answer per input item, in order
hi llm filter "<question>"             keep the items the model answers yes for
hi llm classify "<question>" --choices "a|b|c"
                                       add a label to each item

Input and output:
    [--in lines|jsonl|files|paragraphs]  what an item is (default: lines; jsonl if it parses)
    [--fields a,b,c | --schema <file>]  structured output: a JSON object per item
    [--text]                            plain text output, one line per item
    [--resume <file>]                   skip items already in this output file
Model and pace:
    [--model local|team|<id>]           default: local when the server has one, else team
    [-j <n>]                            requests at once (default: what the upstream allows)
    [--limit <n>] [--dry-run] [--yes]
```

- `files` reads a list of paths from stdin and treats each file as an
  item, with its path kept in the output; PDFs and HTML are turned into text
  first (PDF through `pdftotext` when installed, HTML through the same
  reader as `hi web read`).
- Each output line is the input item's ID (its line number or path) plus the
  result: `{"id": 17, "endpoint": "/v2/orders", …}`. Order follows input
  order, so `paste` and `join` work.
- An item the model fails on, after one retry, is written with an `error`
  field, never dropped, so counts always match.
- `--dry-run` shows the number of items, an estimate of tokens and cost,
  the model, and the first item's prompt, then stops.

## Where it runs

`hi server ai` serves one upstream today. It gains **named upstreams**, so
one server can offer OpenRouter for `hi q` and the team's machine for bulk
work at once:

```text
hi server ai add local --url http://aiw11:8080/v1 --no-key
hi server ai add team --url https://openrouter.ai/api/v1      # today's upstream, renamed
hi server ai default team                                     # what hi q uses
hi server ai bulk local                                       # what hi llm uses
```

Models from a named upstream are listed as `local/<model>`, and the routes
stay the same: `POST /v1/ai/chat/completions` with `model: "local/…"`.
Existing setups keep working: the one upstream becomes `team`.

Without a server, `hi llm` uses `hi q`'s provider order: `HI_Q_BASE_URL`, a
local server on a known port (llama.cpp, Ollama, `hi compute serve`'s
tunnel), then the API keys. Claude Code is not used for `map`: one process
per item is far too slow.

## Speed

A local model's throughput comes from running requests in parallel: a
llama.cpp server started with several slots batches them on the GPU, so
four or eight requests at once take little longer than one.

- `-j` defaults to the upstream's slot count when the server can read it
  (llama.cpp's `/props` reports it), else 4. The server passes the slot
  count in `GET /v1/ai` for the bulk upstream.
- `hi llm` shows progress on stderr: items done, items per minute, errors,
  and time left.
- The server's 90-second timeout per request still applies; long items are
  cut to the model's context, with a warning, before sending.

For paid upstreams, `hi llm` keeps `-j` at 4 and never uses a provider's
batch API in the first version: batch APIs return within hours, which
doesn't fit an agent that is waiting.

## Structured output

`--fields a,b,c` is shorthand for a schema where each field is a string.
`--schema` takes a JSON Schema file. hi sends it as `response_format` with
`json_schema`, which llama.cpp turns into a grammar so the model can only
produce valid JSON, and OpenAI-compatible APIs enforce natively. hi checks
every answer against the schema anyway and retries once on a mismatch.

## Cost and safety

- With the local upstream, there is no price; usage is still recorded per
  user and device in `ai_usage.jsonl`, marked `tool: llm`, so heavy use is
  visible in `hi server spend`.
- With a paid upstream, `hi llm` estimates the cost before starting and asks
  when it is over $1 (`--yes` skips it, after the person agreed, as the
  skill says). The server's existing records show the actual spend.
- Items are data. The instruction is the only instruction: hi wraps each
  item in delimiters and tells the model to treat it as content, which
  reduces, but does not prevent, instructions hidden in scraped pages from
  steering the answers. The output is data for the agent to check, and the
  docs say so.
- Items can carry sensitive data. With a paid upstream it goes to the
  provider, as `hi q`'s prompts do; with `--model local` it stays on the
  team's machines. `hi llm --dry-run` names where the data will go.

## Caching

Each result is cached on the device under the instruction, the schema, the
model, and the item's hash, in `~/.cache/hi/llm/`, for 7 days, so rerunning
a pipeline after fixing one step doesn't redo the rest. `--no-cache` skips
it. `--resume` is the same idea for one interrupted run.

## Releases

1. `hi llm`, `map`, `filter`, `classify` against `hi q`'s providers and the
   server's single upstream; `--fields`, `--schema`, `-j`, `--resume`,
   `--dry-run`, progress, and caching.
2. Named upstreams on the server, `bulk`, the slot count, and `tool: llm` in
   the usage records.
3. `files` input with PDF and HTML to text, and `hi web` piping straight in.

## Risks

- **Small models are wrong more often.** Extraction from clear pages works
  well; judgement doesn't. The skill says to use `hi llm` for extraction,
  classification, and summaries, spot-check a sample, and keep decisions for
  the coding agent.
- **The local machine is shared.** A large `map` keeps aiw11's GPU busy and
  slows `hi q` for everyone using the same upstream. Separate slots for
  `hi q` or a per-user limit on the bulk upstream may be needed.
- **Hidden instructions in items.** See Cost and safety.

## Open questions

1. How many parallel slots the aiw11 llama.cpp server runs today, and how
   throughput scales with more, for this model on the Strix Halo.
2. Whether `hi llm` belongs as its own command, or as `hi q --map`. The
   proposal keeps `hi q` for people at a terminal and `hi llm` for pipes.
3. Whether to add embeddings (`hi llm embed`) for search over large sets,
   which `hi notes` might also use.

## Findings

Checked on 2026-10-02.

- **llama.cpp server.** `-np` sets the number of parallel slots (automatic
  by default), continuous batching is on by default, and `--kv-unified`
  shares one KV cache across slots. `/v1/chat/completions` accepts
  `response_format` with a JSON schema, and `/completion` accepts
  `json_schema` or a GBNF `grammar`, so output can be forced to valid JSON.
- **Batch APIs.** Anthropic's Message Batches cost 50% less, take up to
  100,000 requests or 256 MB, mostly finish within an hour, and expire
  unbilled after 24 hours. OpenAI's Batch API also costs 50% less, with a
  24-hour window and 50,000 requests or 200 MB. Both suit overnight jobs,
  not an agent waiting on a pipe; a `--batch` option for paid upstreams
  could come later.
- **Existing CLIs.** Simon Willison's `llm` (0.36, 2026-09-22) has schemas
  (`--schema`, `--schema-multi`) and async models, but no map or batch
  command over many prompts; its batching is only for embeddings. Nothing
  found combines a Unix filter, structured output, parallel requests to a
  team's own model, and the team's usage records.

## Sources

- https://github.com/ggml-org/llama.cpp/blob/master/tools/server/README.md
- https://platform.claude.com/docs/en/build-with-claude/batch-processing
- https://developers.openai.com/api/docs/guides/batch
- https://llm.datasette.io/en/stable/changelog.html

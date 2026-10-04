# `hi data` specification

Status: Approved (2026-10-03). Datasets and models shipped in v0.24.0,
buckets in v0.24.2, `hi data run`, `env`, `.hifin/data.json`, and
`hi box --data` in v0.25.0, and `hi compute run --data` in v0.25.1.
Decided: groups with no `data` policy read everything, models are
included, compute instances get signed links, uploads wait.

Dependencies: `hi server` (keys kept on the server, devices, groups,
`policy.json`, audit, Slack, signed client API inside NetBird), and the
official `hf` CLI on the device (huggingface_hub 1.5.0 or later, for
buckets).

## Goal

A device connected to a hi server can list and download the private
Hugging Face datasets, models, and buckets of the team's organizations,
with the official `hf` tools, without a Hugging Face token on the device.
Downloading is what people need most of the time; uploading is left for
later (see [Later: uploads](#later-uploads)). The design choice below follows
[hi_server.md](hi_server.md#design-choice-broker-dont-distribute).

```text
$ hi data
  hifinab
  › dataset  hifinab/daily-bars        12.4 GB  updated 2 days ago
    dataset  hifinab/fills-2026         3.1 GB  updated today
    model    hifinab/ranker-v3          1.8 GB  updated 5 days ago
    bucket   hifinab/scratch           80.2 GB  412 files
  name2
    dataset  name2/reference-data      620 MB   updated 3 weeks ago
```

Pick one, confirm where it goes, and it downloads. Today each person needs
their own Hugging Face account in each organization, and a token in `.env`
or `~/.cache/huggingface/token` that an agent can read, copy, and use for
anything the person can, including writing.

## Design choice: broker, don't distribute

The server holds the Hugging Face tokens, one per organization, and never
sends them to a device. It runs a small Hugging Face proxy instead:

```text
 device                                hi server                          huggingface.co
 ──────                                ─────────                          ──────────────
 hi data get hifinab/daily-bars
   │ signed POST /v1/data/token ─────▶ checks group policy
   │ ◀── hi data token (24 h, this device)
   │
   │ runs: hf download --repo-type dataset hifinab/daily-bars
   │       HF_ENDPOINT=http://<server>/hf   HF_TOKEN=<hi data token>
   │
   │ GET /hf/api/datasets/... ───────▶ checks token, method, path, policy
   │                                   swaps in the org's real token ──▶ API, resolve
   │ ◀────────────────────────────────── answer, or 302 to a signed CDN URL
   │
   │ file bytes, straight from the CDN or Xet storage ◀──────────────────── no token needed
```

- `huggingface_hub`, the `hf` CLI, and `datasets` send every Hub call to
  `HF_ENDPOINT`, including the link for refreshing Xet tokens. So the
  official tools work unchanged, and so does `load_dataset("hifinab/...")`
  in a script run through `hi data run`.
- The bytes don't go through the server. Downloads are answered with a 302
  to a signed CDN URL (valid about an hour, no token needed), or with a Xet
  read token (about 15 minutes, read-only, one repository) for Hugging
  Face's storage service. The library drops the device's `Authorization`
  header when it follows a redirect off the Hub, so the hi data token
  doesn't leak to the CDN either.
- The proxy only passes the read calls listed under [Protocol](#protocol).
  Even if the stored token can write, a device can't.

### Sending the token to the device, considered and rejected

The first version of this idea had the server send the organization's
token to the device and `hi` run `hf download` with it, hidden from the
user. It is simpler, but the hiding isn't real:

- The token is in the child process's environment, and anything running as
  the same user can read `/proc/<pid>/environ`. An agent that can run
  `hi data` can get it.
- It is a long-lived organization token. Taking access away from one person
  means rotating it for everyone, the same reason
  [hi_server.md](hi_server.md#design-choice-broker-dont-distribute)
  keeps provider keys on the server.
- A token on a laptop works outside `hi`, with no policy and no record.

What does reach the device is limited: a hi data token that works only
against this server, from this device, for 24 hours, and Xet read tokens
that last 15 minutes and cover one repository. A day is long because `hf`
uses the same hi data token for the whole download, asking for a new Xet
token every 15 minutes; with one hour, a large download stopped partway.
Removing the device or the user ends its tokens at once, and policy is
checked on every call.

## Commands

On a device:

```text
hi data                                  search, pick one, and download it
hi data ls [<org>] [--json]              datasets, models, and buckets this device may read
    [--kind dataset|model|bucket]
hi data info <org>/<name>                size, files, last update, description
hi data get <org>/<name> [--to <dir>]    download a dataset, model, or bucket
    [--revision <rev>] [--include <glob>]... [--exclude <glob>]... [--no-record]
hi data get                              in a project: everything recorded in .hifin/data.json
hi data run -- <command> [args]          run a command that can read the team's data
hi data env                              print HF_ENDPOINT and HF_TOKEN for a shell
hi box claude|codex|shell|run --data     a box whose hf and huggingface_hub reach the data
```

On the server box:

```text
hi server data                           interactive: add, test, and remove organizations
hi server data add <org> [<org>...]      prompt for a token (no echo), check it, store it
    [--token-file <path>]                or read it from a file, for scripted setup
hi server data list                      organizations, token owner, what each can read
hi server data test [<org>]              check each token still reads its organization
hi server data remove <org>
```

`<org>/<name>` is the same as on the Hub. When a dataset, a model, or a
bucket share a name, `dataset:`, `model:`, or `bucket:` in front picks one.

### `hi data`

With a terminal, it opens a list with a search field: typing filters at
once, every word must match (`gemma 27b`), and at most 15 rows show, with
the number of matches. Each entry shows its kind, size, and last update.
Without a styled terminal it asks for search words first and numbers only
the matches.
Enter shows the destination (default `./data/<name>`, or `--to`) and the
size, and starts the download after a confirmation. Without a terminal,
for example when an agent runs it, it prints the same as `hi data ls`.

### `hi data get`

1. Asks the server for a hi data token. The server checks the group policy
   first, so a refusal comes with a clear reason instead of a 403 from the
   proxy.
2. Runs, with `HF_ENDPOINT` and `HF_TOKEN` set for this command only:
   - a dataset: `hf download --repo-type dataset <org>/<name> --local-dir <dir>
     [--revision] [--include] [--exclude]`
   - a model: the same without `--repo-type`
   - a bucket: `hf buckets sync hf://buckets/<org>/<name> <dir>
     [--include] [--exclude]` (`hf download` doesn't do buckets)
3. Before downloading a dataset or model, resolves the branch or tag (the
   default branch when none is given) to a commit and downloads that
   commit, so what is recorded is exactly what was fetched.
4. In a project (the nearest folder upwards with `.hifin/`), records the
   repository, the commit, the folder relative to the project, and the
   filters in `.hifin/data.json`, replacing an earlier entry for the same
   repository and folder. `--no-record` skips it, and a folder outside the
   project isn't recorded. `hi data get` with no arguments fetches
   everything recorded, at the recorded commits, without changing the
   record, so a teammate gets the same files. Buckets have no history, so
   their entries record the time of the download and a hash of the file
   list (path, size, and content hash of each file); a later `get` warns
   when the bucket has changed and fetches the current files.

```json
{
  "data": [
    { "kind": "dataset", "id": "hifinab/bars-1d", "revision": "9981174…",
      "to": "data/bars-1d", "include": ["2026/*"], "fetched": "2026-10-03T21:03:50Z" },
    { "kind": "bucket", "id": "hifinab/fdb", "to": "data/fdb",
      "files_hash": "5c1e…", "files": 412, "fetched": "2026-10-03T21:04:10Z" }
  ]
}
```

If `hf` isn't installed, `hi data get` says so and how to install it. `hi`
doesn't bundle it.

### `hi data run` and `hi data env`

`hi data run -- python backtest.py` runs the command with `HF_ENDPOINT` and
a fresh hi data token for every repository the group may read (scope
`*`), so `load_dataset`, `hf_hub_download`, and `hf://datasets/...` paths
in pandas or Polars read the team's private data directly, and
`from_pretrained("hifinab/...")` loads the team's models. The command's
exit status is passed on. `hi data env` prints the two variables for
`eval "$(hi data env)"` in a shell; the token lasts a day.

### `hi box --data`

A box has no hi device key, so it can't ask the server itself. With
`--data` (or `"data": true` under `customizations.hi` in
`devcontainer.json`), hi asks for a hi data token on the host before the
box starts and keeps it in the box's state folder, mounted only into hi's
box proxy. The box gets `HF_ENDPOINT` pointing at a third listener on that
proxy and `HF_TOKEN` set to a placeholder; the listener passes the calls on
to the server's `/hf` with the real token, the way Claude Code's token is
added. The box's allowlist gains `cdn.hf.co` and `xethub.hf.co` for the
file contents, and the server's name is resolved on the host, since it may
resolve only through NetBird. A box running longer than a day needs a
restart for a new token.

### `hi compute run --data`

A rented instance can't reach the hi server, which listens only inside
NetBird, so it can't use the proxy. Decided on 2026-10-04: signed links.
Joining instances to NetBird, a public HTTPS endpoint for `/hf`, and
copying from the laptop over SSH were considered; the first puts a network
credential on rented machines and doesn't work on Hugging Face Jobs or
Colab, the second exposes the server, and the third is slow.

1. When the run starts, after the confirmation, the device calls
   `POST /v1/data/links` with `{scope, revision, include}` for each
   `--data`. The server checks policy, pins the commit, lists the files
   (at most 2,000), and resolves each with the organization's token,
   eight at a time. The Hub answers a file in Hugging Face's storage with a
   signed CDN link that needs no token and lasts about an hour; a small
   file kept in git has no such link, so the server sends its contents
   (10 MB each, 32 MB in all at most). The grant is audited.
2. `hi` replaces the script with a wrapper: the links and contents,
   compressed, then a downloader that fetches eight files at a time into
   `data/<name>` with retries, skips files already there with the right
   size, and stops with a clear message when a link has expired; then the
   user's script runs as `__main__` with its arguments. The script's
   inline metadata (PEP 723) is copied to the top, so `uv` installs its
   dependencies as before.
3. Hugging Face Jobs carry the script in one environment variable, which
   Linux limits to 128 KiB, so the wrapper may be at most 120 KB there.
   Links take about 1 KB each before compression. When files kept in git
   make it too large, the error names them. Colab uploads the script as a
   file and has no such limit.

Only script runs take `--data`: an image may have no Python. `hi compute
up` instances (RunPod, Shadeform) can use `hi data get` over SSH only if
they reach the server; that is left for later.

## Policy

`policy.json` gains `data` per group: patterns over `<org>/<name>` for the
datasets, models, and buckets the group may read.

```json
"staff":    { "data": ["hifinab/*", "name2/*"] },
"students": { "data": ["hifinab/daily-bars", "hifinab/public-*"] },
"agents":   { "data": [] }
```

- With no `data` field, a group can read everything the server's tokens
  can, like `template_sources`. An empty list allows nothing. Decided on
  2026-10-03: most groups need everything, and limiting is the exception.
- The pattern is checked twice: when a hi data token is issued, and on every
  proxied call, using the `<org>/<name>` in the path.
- `hi data ls` shows only what the group may read.

## Server

### Adding organizations

```text
$ hi server data add hifinab name2
Hugging Face token for hifinab, name2: ****************
✓ hifinab: 14 datasets, 4 models, 3 buckets (token of service-bot, read-only)
✓ name2:   2 datasets, 0 models, 0 buckets
```

- The token is asked for with no echo, the same as `hi server provider add`,
  and stored in `keys.json`. A flag like `--hf_key=...` would put it in the
  shell history and process list, so the only non-interactive way in is
  `--token-file`.
- One token can serve several organizations. Different organizations can
  have different tokens. The proxy picks the token from the `<org>` in the
  path.
- Before saving, the server calls `whoami-v2` and lists the organization's
  datasets, models, and buckets to show what the token can read. It warns when the
  token can also write, and recommends a fine-grained, read-only token
  limited to these organizations, or an organization service account on
  Enterprise.
- `hi server data`, with no arguments, is a menu for the same steps: add an
  organization and its token, test, remove.

### Listing

The server lists each organization's datasets (`GET /api/datasets?author=`),
models (`GET /api/models?author=`), and buckets (`GET /api/buckets/<org>`) and keeps the result for five
minutes, so devices don't spend the token's rate limit. `hi server data list`
and the `/v1/data` answer show the same.

### Records

Each hi data token issued is written to the audit log with the user,
device, and repository. Downloads go to `data_usage.jsonl`, one line per
call, with the user, device, repository, file, and size from the Hub's
headers:

- Files served by `resolve` (small files, and Xet files for older clients)
  are recorded by name.
- Recent `hf` versions fetch Xet files without a `resolve` call: they list
  the repository and ask for a Xet read token, then read from Hugging Face's
  storage directly. The server records the token grant (repository and
  revision), not which files were read.

The server never sees the bytes downloaded. The weekly report gets a line per organization: downloads, data
size, and the most used repositories.

## Protocol

Signed client API, for enrolled user devices:

- `GET /v1/data`: the organizations, datasets, models, and buckets this
  device's group may read, with kind, size, file count, last update, and
  for datasets and models the current commit.
- `POST /v1/data/token` with `{scope}` (one `<org>/<name>`, or `*` for
  `hi data run`): a hi data token bound to this device and the group's
  policy, valid for 24 hours.
- `POST /v1/data/links` with `{scope, revision, include}`: for a compute
  run, a signed download link per file (or its contents, for small files
  kept in git) and the pinned commit; see
  [`hi compute run --data`](#hi-compute-run---data). Laptops older than v0.24.2 don't get buckets
  in the list, since they would download them as models.

The proxy, under `/hf/`, takes a hi data token as `Authorization: Bearer`.
It passes on, with the organization's real token:

| Kind | Calls |
|---|---|
| Dataset | `GET /api/datasets/<org>/<name>[/revision/<rev>]`, `GET .../tree/<rev>[/<path>]`, `POST .../paths-info/<rev>`, `GET .../xet-read-token/<rev>`, `GET`/`HEAD /datasets/<org>/<name>/resolve/<rev>/<path>` |
| Model | `GET /api/models/<org>/<name>[/revision/<rev>]`, `GET .../tree/<rev>[/<path>]`, `POST .../paths-info/<rev>`, `GET .../xet-read-token/<rev>`, `GET`/`HEAD /<org>/<name>/resolve/<rev>/<path>` |
| Bucket | `GET /api/buckets/<org>/<name>`, `GET .../tree[/<prefix>]`, `POST .../paths-info`, `GET .../xet-read-token`, `GET`/`HEAD /buckets/<org>/<name>/resolve/<path>` |

Everything else gets a 403 with a message naming `hi data`. Responses pass
through unchanged, except that a `Link` header pointing to `huggingface.co`
is rewritten to the proxy.

The client API is plain HTTP inside NetBird, so `HF_ENDPOINT` is
`http://<server>:<port>/hf`, encrypted by the NetBird tunnel.

## Releases

1. Datasets and models (v0.24.0), then buckets (v0.24.2): `hi data`, `ls`,
   `info`, `get`; `hi server data add|list|test|remove`; the proxy; `data`
   in policy; audit. Day-long tokens in v0.24.3.
2. v0.25.0: `hi data run` and `env`; `.hifin/data.json` and `hi data get`
   with no arguments; `hi box --data`.
3. v0.25.1: `hi compute run --data <org>/<name>[/<pattern>]`, so a job
   downloads on the instance instead of the laptop (see below).

## Risks

- **Data leaves through the agent.** Anything that can read a dataset can
  send it anywhere it can reach. Inside a box the egress allowlist limits
  where; outside one, nothing does. Sensitive datasets should be limited to
  groups whose agents run in boxes.
- **One token's rate limit for everyone.** Hugging Face counts limits per
  token's user, in five-minute windows (resolver calls: 5,000 on Free,
  20,000 on Team). Many devices downloading many small files can hit it.
  Caching the listings and using a service account on Enterprise help; the
  CDN and Xet traffic don't seem to count.
- **Short-lived tokens do reach the device.** A Xet read token is usable for
  15 minutes against one repository, and a signed CDN URL for about an hour
  for one file. That's acceptable, and much less than a long-lived
  organization token.
- **The proxy has to follow the Hub.** A new endpoint used by a future `hf`
  release fails with a 403 until the proxy allows it. `hi server data test`
  runs a real small download, so a change shows up there first.

## Checked in a prototype

On 2026-10-03, with `hf` 1.32.0 against the real Hub through a local hi
server:

1. `hf download` accepts a hi data token (`hidata_…`); there's no format
   check on the device.
2. `HF_ENDPOINT=http://<server>/hf` works for every call, including Xet
   downloads (a 548 MB `model.safetensors` came from Xet storage directly).
3. The Hub answers `resolve` for small files with a relative redirect to
   `/api/resolve-cache/<kind>s/<org>/<name>/<commit>/<file>`, with nested
   names encoded as one segment (`onnx%2Fconfig.json`). The proxy allows
   it and rewrites relative redirects to stay on the proxy.
4. Recent `hf` downloads Xet files with `tree` and `xet-read-token` only,
   with no `resolve` call per file (see [Records](#records)).
5. `hf` also calls `GET /api/agent-harnesses` without a token; the proxy
   refuses it and `hf` carries on.
6. `hf download` takes `--include` once per pattern; with several patterns
   after one flag, the rest are read as file names.
7. `hf buckets sync hf://buckets/<org>/<name> <dir>` makes these calls: the
   bucket's info, its `tree?recursive=true`, a `HEAD` on one file's
   `resolve`, and a Xet read token; the files come from Xet storage. A
   19-file public bucket synced through the proxy on 2026-10-03, and a
   private 36 GB bucket (`hifinab/fdb`) from the team's server.
8. In `hi data run`, `datasets.load_dataset` (datasets 5.0.1), pandas
   `read_parquet("hf://datasets/...")`, and `hf_hub_download` all read
   through the proxy with no other Hub calls needed.
9. In `hi box run --data` (rootless Podman), `hf_hub_download` of a 548 MB
   Xet file worked with only the placeholder token in the box; the box
   reached `cas-server.xethub.hf.co` and `us.aws.cdn.hf.co`.
10. `hi compute run --data`'s wrapper, run as the instance would, fetched
    a dataset's parquet files and a 548 MB Xet file from signed links with
    no token (6 KB wrapper), and ran the script with its arguments. With
    an old model's 1.4 MB in-git `tokenizer.json`, the Hugging Face size
    check refused it and named the file.

## Later: uploads

Devices can't upload through `hi data` for now; the proxy refuses every
write. Users mostly need to download. A `hi data upload` will be needed at
some point (results, cleaned datasets, trained models), and how it is
limited (which repositories or buckets, approval through `hi ask`, review of
what's uploaded) is to be discussed and decided then.

## Open questions

1. Whether to support the S3-compatible gateway for buckets (`s3.hf.co`),
   for tools that only speak S3. Its keys carry a user token's permissions,
   so the same broker approach would be needed.

## Earlier draft: databases

The first draft of this spec was a read-only SQL gateway over the team's
databases (Postgres, ClickHouse, BigQuery, Snowflake) with cost estimates
and approvals (commit 128a1e2). It is set aside until the team decides
which databases it uses. It would fit under the same command as `hi data
sql`, with the same broker design.

## Findings

Checked on 2026-10-03.

- **Buckets** launched on 2026-03-10 (huggingface_hub 1.5.0). They are
  mutable, Xet-backed storage with no history, private or public under a
  user or organization. CLI: `hf buckets create|list|cp|sync|rm|settings`.
  `hf download` refuses them and points to `hf sync`.
- **`HF_ENDPOINT`** is used by huggingface_hub and `datasets` for API,
  resolve, and bucket calls. The Xet token refresh link is rewritten to it.
  Xet storage and CDN addresses come from Hub responses and are reached
  directly.
- **Short-lived access**: `xet-read-token` returns `{accessToken, exp,
  casUrl}`, read-only, one repository and revision (a whole bucket), about
  15 minutes, measured. `resolve/` on Xet files answers 302 to a signed CDN
  URL that works without a token for about an hour, measured. The library
  drops `Authorization` on redirects off the Hub.
- **Minting tokens**: there's no public API to create fine-grained tokens.
  Enterprise has OAuth token exchange (tokens of a member, up to 30 days)
  and service accounts. Neither gives per-device, per-repository tokens, so
  the proxy is still needed.
- **Fine-grained tokens** can be limited to read of chosen organizations or
  repositories. Team and Enterprise admins can list and deny tokens with
  access to the organization; anyone can revoke a leaked token with
  `POST /api/credentials/revoke`.
- **`hf download`** takes `--token`, then `HF_TOKEN`, then the token file.
  A token passed by flag or variable isn't written to disk.

## Sources

- https://huggingface.co/blog/storage-buckets
- https://huggingface.co/docs/hub/storage-buckets,
  https://huggingface.co/docs/hub/storage-buckets-s3
- https://huggingface.co/docs/huggingface_hub/guides/buckets
- https://huggingface.co/docs/xet/auth
- https://huggingface.co/docs/hub/security-tokens,
  https://huggingface.co/docs/hub/enterprise-tokens-management,
  https://huggingface.co/docs/hub/oauth
- https://huggingface.co/docs/hub/rate-limits
- huggingface_hub source at commit d711944 (`constants.py`, `utils/_xet.py`)

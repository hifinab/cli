# `hi server expose` specification

Status: Draft (2026-10-04). Release 1 (the team's data on cloud machines)
shipped in v0.26.0, tested end to end on a real Hugging Face job. Not yet built from the spec: `hi server expose revoke`, and the
`on|off` command (`expose_listen: "off"` in `config.json` does it).

Dependencies: `hi server` (signed client API inside NetBird, server key,
policy, audit), `hi data` ([hi_data.md](../approved/hi_data.md)), NetBird's
reverse proxy (`netbird expose`, beta, on NetBird's cloud), and `hi compute
run` and `up`.

## Goal

Let cloud machines started by the team (Hugging Face Jobs, Colab, RunPod,
Shadeform) use a narrow part of the hi server while they run. The first part
is the team's data, so code on the machine can call `load_dataset` and
`from_pretrained`, and `hi data get` works there.

Today the server listens only inside NetBird, which rented machines can't
join. `hi compute run --data` works around that with signed links, but:

- the links last about an hour and must be made when the run starts;
- on Hugging Face Jobs, everything travels in one environment variable of
  120 KB, so large repositories need a pattern;
- the script can't read anything it wasn't given up front;
- `hi compute up` machines (RunPod, Shadeform) get no data at all.

## Design choice: a narrow listener, exposed only while needed

The server opens a **second listener** with only the routes a cloud machine
may use, and publishes it with `netbird expose` while a run that needs it is
active. The existing client API (port 7373) stays inside NetBird.

```text
 cloud machine                    NetBird reverse proxy               hi server
 ─────────────                    ─────────────────────               ─────────
 HF_ENDPOINT=https://hi-…        https://hi-<name>-<id>.eu1.          instance listener
   .netbird.services/hf          netbird.services                     (NetBird IP, :7374)
 HF_TOKEN=<run token>   ───TLS──▶ terminates TLS ──WireGuard──▶       /hf/* only
                                                                      checks the run token
 file contents  ◀────────────────── signed CDN links ◀──────────────  swaps in the org token
```

Considered and rejected:

- **Exposing the whole client API.** Enrollment, compute requests, Slack
  linking, and the dashboard feed would be on the internet for as long as a
  run lasts. They were designed with NetBird as the outer wall.
- **Cloudflare Quick Tunnels.** For testing only, with a new random name
  every time, no uptime promise, a 200-request limit, and a new third party.
  Their email protection needs a browser.
- **Joining machines to NetBird.** Puts a network credential on rented
  machines, needs extra container rights, and doesn't work on Hugging Face
  Jobs or Colab.

NetBird's password and PIN protection are login pages for browsers; a script
can't pass them (checked, below). The lock is hi's own **run token**.

## Run tokens

A run token is a hi data token (signed by the server key, see
[hi_data.md](../approved/hi_data.md#hi-data-tokens)) with three differences:

- **Marked for cloud use** (`"x": true`). The instance listener accepts only
  these, and the client API refuses them, so a laptop's token is useless on
  the internet and a run token is useless inside the VPN.
- **Several scopes**, the repositories named with `--data`, never `*`.
- **The run's lifetime** plus 15 minutes, at most 7 days, instead of a day.

It names the device that asked for it, and stops working at once when that
device or its user is removed, or when policy no longer allows a
repository. `hi server expose revoke <run>` ends one early; the server keeps
a short list of revoked run IDs until they would have expired.

## The exposure's lifetime

`netbird expose` keeps a service only while the command runs, so the server
runs it as a child process:

1. When a device asks for a run token and no exposure is up, the server
   starts `netbird expose <port> --with-name-prefix hi-<server name>` and
   reads the `URL:` line. It answers once the public URL responds.
2. While any run token is still valid, the exposure stays up. If
   `netbird expose` exits, the server starts it again and logs the new URL;
   runs already started keep the old URL, so they fail until they retry
   with a new token. (NetBird gives a new name each time.)
3. When the last run token has expired and nothing has called for 10
   minutes, the server stops it.

Every start and stop is audited, and `hi server live` shows the URL while
it is up. With NetBird's dashboard setting **Peer Expose** off, run tokens
are refused with a message naming the setting, and `hi compute run --data`
uses signed links instead.

The listener binds to the server's NetBird address (NetBird's proxy reaches
the server over WireGuard, not `localhost`) on port 7374 unless
`expose_listen` in `config.json` says otherwise.

## Routes on the instance listener

| Route | For |
|---|---|
| `/hf/*` | the `hi data` proxy, with the same allowed calls; links point at the public URL |
| `/health` | an empty 200, for the start check |

Everything else is a 404. Later releases may add the team model for `hi q`
and an idle signal, each as its own route and its own scope on the token.

## Commands

On the server box:

```text
hi server expose                  the current URL, runs using it, and since when
hi server expose on|off           allow or forbid exposure (on by default when Peer Expose works)
hi server expose revoke <run>     end one run's token early
```

On a device, nothing new to type: `hi compute run --data <org>/<name>`
asks for a run token first, and falls back to signed links when the server
can't expose.

## Cloud machines

**`hi compute run --data` (Hugging Face Jobs).** The device asks
`POST /v1/data/run-access` with the run's scopes and lifetime and gets
`{url, token, expires}`. The job gets:

- `HF_ENDPOINT=<url>/hf` as an environment variable;
- the run token as the encrypted job secret `HF_TOKEN`, never as a plain
  environment variable, which shows in the job's settings;
- a wrapper that downloads each `--data` repository into `data/<name>`
  through the proxy (listing the tree, then following each file's redirect
  to the CDN without the token), and then runs the script.

With these, the script can also call `load_dataset("hifinab/...")` for any
repository named with `--data`. There is no size limit and no one-hour
expiry.

**Colab** has no secret store, so it keeps using signed links.

**`hi compute up` (RunPod, Shadeform), later release.** `--data` on `up`
would put `HF_ENDPOINT` and the run token in the machine's environment, so
`hf download` and `hi data get` work over SSH.

## Risks

- **The public URL is on the internet** while runs use it. Only `/hf` and
  `/health` answer, and `/hf` needs a valid, signed, unexpired run token
  whose device, user, and policy still check out.
- **NetBird sees the traffic in plain text**, since its proxy terminates
  TLS: run tokens and repository names, not the files, which come from the
  CDN.
- **A run token sits on a rented machine.** It reads only the named
  repositories, only through this server, and only for the run's lifetime;
  it can be revoked.
- **Beta.** NetBird's reverse proxy is marked beta; if it fails, signed
  links still work.
- **The server becomes part of every run** that uses data this way. A
  restart of the server ends the exposure; runs fail until they retry.

## Releases

1. The team's data: the instance listener with `/hf`, run tokens, the
   exposure's lifetime, `hi server expose`, and `hi compute run --data` on
   Hugging Face Jobs through it, with signed links as the fallback.
2. `hi compute up --data` for RunPod and Shadeform.
3. Other routes, each its own scope: the team model for `hi q` on cloud
   machines, and an idle signal so a machine can stop itself.

## Open questions

1. Whether one exposure should serve all runs (as above) or each run gets
   its own, which gives each run its own URL at the cost of a NetBird
   service per run.
2. Whether to use a custom domain (`--with-custom-domain`) so the URL is
   stable across restarts of `netbird expose`.
3. Whether NetBird's dashboard-only static header check is worth setting
   up as a second lock for a permanent service.

## Findings

Checked on 2026-10-04 on vmhiserver (NetBird 0.79.0, NetBird cloud):

- With **Peer Expose** off, `netbird expose` fails with "peer expose is not
  enabled for this account"; an admin turns it on in Settings > Clients.
- With it on, `netbird expose 18099 --with-name-prefix hi-test` printed
  `URL: https://hi-test-04is.eu1.netbird.services` within 10 s, without
  sudo.
- A service bound to `127.0.0.1` gave 502 "connection refused"; bound to
  the server's NetBird address, it answered from outside in 0.2 s. 404s
  passed through, and an `Authorization` header reached the service
  unchanged.
- `--with-password` serves a browser login page: no password, basic auth,
  and a form post all failed with 401 or 404, so scripts can't use it.
- Stopping `netbird expose` ended the service; the URL answered 404 within
  seconds.

Prototype, checked the same day with a throwaway hi server on vmhiserver
(ports 7399 and 7400, only the hifinab data token):

- The first `POST /v1/data/run-access` started `netbird expose 7400
  --with-name-prefix hi-vmhiserver` and answered with
  `https://hi-vmhiserver-y4v6.eu1.netbird.services/hf` once `/health`
  responded; the start and the run token were audited.
- A real Hugging Face job (`cpu-basic`) got the run token as the encrypted
  secret `HI_DATA_TOKEN` and downloaded `hifinab/fintabarena-results`
  through it: 150 files, 35.6 MB, in 13 s (2 s with signed links; each
  file's call goes through NetBird's proxy first). The script then called
  `hf_hub_download` for a file itself, and a repository the token doesn't
  name was refused.
- Stopping the server with SIGTERM stopped `netbird expose`; the URL
  answered 404 seconds later.

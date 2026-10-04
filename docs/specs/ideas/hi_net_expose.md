# `hi net expose` specification

Status: Draft (2026-10-04).

Dependencies: NetBird's reverse proxy (`netbird expose`, beta, on NetBird's
cloud, with **Peer Expose** turned on in Settings > Clients), `hi net`, and
optionally a hi server for the audit log and live feed.

## Goal

Let a person or an agent put a service running on their machine on a
temporary public HTTPS address in one command, safely by default: a web app
to show a colleague, a webhook receiver for a test, a notebook, an API for
a phone. It ends on its own.

```text
$ hi net expose 3000 --max 2h
Exposing localhost:3000 at https://hi-aiw9-3000-k2x7.eu1.netbird.services
  password: plum-otter-42 (a login page asks for it)
  until 16:30; Ctrl+C stops it sooner
GET  /            200  12 ms
POST /api/order   201  48 ms
```

`netbird expose` does the publishing. `hi` adds what it lacks:

- **Services on `localhost` work.** NetBird's proxy reaches the machine
  over WireGuard at its NetBird address, so a service bound to `127.0.0.1`
  gives 502 "connection refused" (checked on 2026-10-04). Most dev servers
  bind there. `hi` runs a small relay on the NetBird address that passes
  requests to `localhost:<port>`.
- **A time limit.** Exposure ends after `--max` (default 1 hour), even if
  the person or agent forgets.
- **Protected by default.** No protection is an explicit choice.
- **A request log** on the terminal, which also helps debug webhooks.
- **Background use for agents.** `--detach` returns once the address works,
  with `--json` output; `ls` and `stop` manage what is running.
- **A record.** On a machine connected to a hi server, the start and end go
  to the server's audit log and live feed.

## Commands

```text
hi net expose <port> [options]     expose localhost:<port> until --max or Ctrl+C
    --max <duration>               how long (default 1h; at most 24h)
    --password <text>              a password page in front (default: one is generated)
    --pin <6 digits>               a PIN page instead
    --groups <g1,g2>               NetBird SSO, for users in these groups, instead
    --public                       no protection: anyone with the address
    --name <prefix>                the start of the address (default hi-<host>-<port>)
    --host <addr>                  where the service listens (default localhost)
    --detach                       run in the background; print the address and return
    --json                         print {name, url, port, protection, password, expires}
hi net expose ls [--json]          what this machine is exposing
hi net expose stop <name> | --all  stop it now
```

## Protection

NetBird's command line offers three locks; hi picks one by default:

| Option | Who gets in | Good for |
|---|---|---|
| (default) generated password | anyone told the password; a login page asks for it | showing a web app to people |
| `--pin 123456` | anyone told the PIN | the same, easier to say aloud |
| `--groups devops` | NetBird users in those groups, through SSO | colleagues |
| `--public` | anyone with the address | webhooks, API clients, phones |

Password, PIN, and SSO are login pages for browsers: scripts and webhook
senders can't pass them (checked: basic auth and form posts fail). So a
service for machine clients needs `--public`, and should check its own
token or signature, as webhook receivers normally do. The addresses are
random but not secret enough to be the only lock.

The generated password is three words and two digits, printed once, and
kept in the background state file (mode 0600) so `ls` can show it again.

## How it works

1. Find the NetBird address (interface `wt0`). Without NetBird connected,
   stop with "connect with `hi net` first".
2. Check that something answers on `<host>:<port>`, and say so if not;
   an exposed port with nothing behind it gives visitors a 502.
3. Start the relay: an HTTP reverse proxy on `<NetBird address>:<free port>`
   to `http://<host>:<port>`, which keeps the `Host` header the browser
   sent, passes WebSockets through, and logs one line per request (method,
   path without the query, status, time).
4. Run `netbird expose <relay port> --with-name-prefix <name>` with the
   protection flags, and read the `URL:` line. If it reports "peer expose
   is not enabled", say that a NetBird admin turns on Peer Expose in
   Settings > Clients.
5. Wait until the URL answers (any status but NetBird's own 502), then
   print it.
6. At `--max`, Ctrl+C, `stop`, or if the service stops answering for a
   while, stop `netbird expose` and the relay. The address answers 404
   seconds later.

With `--detach`, `hi` starts itself in the background for steps 3–6 and
records `{name, url, port, host, protection, password, pid, expires}` in
`~/.local/state/hi/net/expose/<name>.json`, which `ls` and `stop` read.

## On a hi server

If the machine is connected to a hi server, `hi` sends an `expose` activity
when the address starts and stops: the address, the local port, the
protection, and the end time. The server writes it to the audit log and the
live feed ("alice exposed https://… (port 3000, password) until 16:30").

This is a record, not a control. NetBird's Peer Expose setting is the only
real switch: anyone on the account can run `netbird expose` directly. A
group policy (`"net_expose": false`) could make `hi` refuse, as a guard
against agents, not people.

## Agents

The skill tells agents to:

- ask the user before exposing anything, and say what will be reachable;
- never use `--public` without the user's yes, and say why it's needed;
- use `--detach --json`, give the user the address, and `stop` it when the
  task is done;
- keep `--max` short.

## Risks

- **A service on the internet.** Whatever the service does, anyone who
  passes the lock can do. Dev servers often have debug pages and no
  authentication. The default password and the time limit narrow this;
  `--public` removes the lock.
- **NetBird sees the traffic in plain text**, since its proxy terminates
  TLS.
- **Beta.** NetBird's reverse proxy is marked beta.
- **The relay listens on the NetBird address**, so other peers in the
  network can reach the service through it for as long as it runs. That
  is usually already true for services bound to `0.0.0.0`.

## Releases

1. `hi net expose <port>` with the relay, the time limit, the default
   password, `--pin`, `--groups`, `--public`, `--name`, `--host`, the
   request log, `--detach`, `--json`, `ls`, `stop`, the activity on a hi
   server, and the skill's rules.
2. TCP services (`--protocol tcp`, for a database or SSH) without the
   relay's request log; `net_expose` in policy.

## Open questions

1. Whether the default should be a password (as above) or SSO for the
   person's own NetBird groups, which needs no shared secret but only works
   for colleagues.
2. Whether `hi compute serve` should offer `--expose`, giving a model on a
   rented GPU a public address through the laptop's tunnel.

---
title: Share a local service
description: Put a web app, webhook receiver, or API running on your machine on a temporary public HTTPS address with hi net expose, protected by a password and ending on its own.
---

`hi net expose` puts a service running on your machine on a public HTTPS
address for a while: a web app to show a colleague, a webhook receiver for
a test, a notebook, or an API for your phone. It ends on its own.

```sh
hi net expose 3000
```

```text
Exposing localhost:3000 at https://hi-aiw9-3000-k2x7.eu1.netbird.services
  password: plum-otter-raven-42 (a login page asks for it)
  until 16:30; Ctrl+C stops it sooner
GET    /                                        200  12 ms
POST   /api/order                               201  48 ms
```

It uses NetBird's reverse proxy, so the machine must be on
[NetBird](/guide/workstation/netbird/), and a NetBird admin must have turned
on **Peer Expose** once (Settings > Clients in the NetBird dashboard).

## Who gets in

By default, a password page sits in front, with a password hi makes up and
prints. Choose another lock, or none:

```sh
hi net expose 3000                      # a generated password
hi net expose 3000 --password <text>    # your own password
hi net expose 3000 --pin 482913         # a 6-digit PIN
hi net expose 3000 --groups devops      # NetBird users in these groups, through SSO
hi net expose 8080 --public             # anyone with the address
```

Password, PIN, and SSO are login pages for people in a browser. Webhook
senders, API clients, and scripts can't pass them, so they need `--public`;
the service should then check its own token or signature, as webhook
receivers normally do.

## How long

```sh
hi net expose 3000 --max 30m    # default 1h, at most 24h
```

At `--max`, or on Ctrl+C, the address stops working within seconds.

## In the background

```sh
hi net expose 3000 --detach          # prints the address and returns
hi net expose ls                     # what is exposed, with passwords and end times
hi net expose stop hi-aiw9-3000-k2x7
hi net expose stop --all
```

The request log of a background exposure goes to a file under
`~/.local/state/hi/net/expose/`, which `--detach` names. `--json` prints
the address, protection, and end time for scripts and agents.

## Services on localhost

Most development servers listen only on `localhost`, which NetBird's proxy
can't reach: it comes in over the VPN, at the machine's NetBird address.
`hi net expose` runs a small relay on that address that passes requests to
`localhost:<port>`, keeping the original `Host` header and WebSockets.
`--host` points the relay elsewhere, such as a container's address.

## What to keep in mind

- Whatever the service lets people do, anyone past the lock can do.
  Development servers often have debug pages and no sign-in.
- NetBird's proxy sees the traffic, since it handles the HTTPS.
- Other machines in the NetBird network can reach the service through the
  relay while it runs.
- On a machine connected to a [hi server](/guide/compute/managed/), each
  exposure is recorded in the server's audit log and live feed.
- NetBird's reverse proxy is in beta.

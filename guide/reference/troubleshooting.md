---
title: Troubleshooting
description: Error messages from hi and what to do about them, grouped by area.
---

Search this page for the words in your error message. Most messages also say
what to do next.

## Starting machines

| Message                                                     | What to do                                                   |
|-------------------------------------------------------------|--------------------------------------------------------------|
| `confirmation needed; rerun with --yes`                     | No terminal to ask in, such as a script or an agent. Check the price, then add `--yes`. |
| `several providers are ready (colab, hf)`                   | Add `--on colab` or `--on hf`, use a hardware name only one has, or set `HI_COMPUTE_PROVIDER`. |
| `unknown colab hardware "X"` / `unknown hf hardware "X"`    | Use a name from `hi compute hardware`. Names differ: `T4` on Colab, `t4-small` on Hugging Face. |
| `--max … exceeds the colab limit of 24h`                    | Colab sessions last at most 24 hours.                        |
| `an instance named "X" already exists`                      | Choose another `--name`, or stop the old one.                |
| `no provider is ready`                                      | `hi compute providers` shows what is missing.                |

## Colab

| Message                                                     | What to do                                                   |
|-------------------------------------------------------------|--------------------------------------------------------------|
| `the Colab CLI is not installed`                            | `hi install`, or `uv tool install google-colab-cli`          |
| `colab    not signed in`                                    | `hi login colab`                                             |
| `asked for X but Colab started Y`                           | Your plan or Colab's capacity did not allow X. Stop it and try other hardware. |
| `port 8080 on colab is used by Colab's own proxy`           | Serve and tunnel another port, such as 8000.                 |
| `colab has no secret store` / `colab runs cannot detach`    | Use Hugging Face for secrets and detached runs, or an instance. |
| `colab … did not answer within …`                           | The Colab CLI stalled. Try again.                            |

## Hugging Face

| Message                                                     | What to do                                                   |
|-------------------------------------------------------------|--------------------------------------------------------------|
| `Hugging Face Jobs need pre-paid credits` (HTTP 402)        | Add credits, or bill an account that can pay: see `hi compute billing`. |
| `Hugging Face rejected the token`                           | `hi login hf`                                                |
| `Permission denied (publickey)`                             | [Add your SSH key to the Hub](/guide/compute/hugging-face/#add-your-ssh-key-to-the-hub). |
| `Hugging Face refused: …`                                   | No permission on that account; check `--namespace` and `hi compute billing`. |
| `secret X is not set in this shell`                         | `export X=…` first.                                          |
| `… is larger than 96 KB`                                    | Put the code in an image, or in a repository the script clones. |
| `401` from an `…hf.jobs` URL                                | Send `Authorization: Bearer <your Hugging Face token>`.      |

## RunPod

| Message                                                     | What to do                                                   |
|-------------------------------------------------------------|--------------------------------------------------------------|
| `RunPod needs more account balance`                         | Add funds at console.runpod.io/user/billing.                 |
| `none of that hardware is free right now`                   | Choose another `--gpu`.                                      |
| `RunPod refused … read-only`                                | Create an API key with read and write access.                |
| `runpod has no run-to-completion jobs yet`                  | Use `hi compute up --on runpod` and `hi compute ssh`.        |

## Shells and tunnels

| Message                                                     | What to do                                                   |
|-------------------------------------------------------------|--------------------------------------------------------------|
| `no SSH key found`                                          | `ssh-keygen -t ed25519`                                      |
| `bind … Address already in use`                             | The local port is taken; use `<remote>:<other-local>`, such as `8888:18888`. |
| `Tunnel closed: X is no longer running.`                    | The machine was stopped, by you or its `--max`. Start it again. |
| `no instance named "X"`                                     | Check `hi compute ls`; names are case-sensitive and lowercase. |

## Serving

| Message                                                     | What to do                                                   |
|-------------------------------------------------------------|--------------------------------------------------------------|
| `failed: download`                                          | Check the repository name and `--quant`; gated models need access on the Hub. |
| `failed: llama-server exited`                               | Usually out of GPU memory. Choose a smaller quant or bigger GPU. `hi compute logs <name>` shows why. |
| `failed: llama.cpp build`                                   | Colab only; see `hi compute logs <name>`. Retry the same command. |
| `serving needs a GPU`                                       | Choose GPU hardware with `--gpu`.                            |
| An empty answer from a reasoning model                      | Raise `max_tokens`; the thinking uses tokens first.          |

## Projects

| Message                                                     | What to do                                                   |
|-------------------------------------------------------------|--------------------------------------------------------------|
| `file(s) already exist with other content; nothing was written` | `hi init` never overwrites. Move those files, or choose another directory. |
| `give a directory or --name`                                | `hi init python pricing-tools`, or `--name` with `.`.        |
| `the … template needs hi v… or newer`                       | `hi update`, then run `hi init` again.                       |
| Private templates are missing from `hi init --list`           | Run `hi connect status`: the device must be connected, and the server's policy may hide them from your group. |
| `warning: no server templates from …`, then `cached`        | The server is unreachable; the last templates this device fetched still work. |
| `… are not signed by this server's key`                     | Nothing from that bundle was used. Tell an admin; if the server was replaced on purpose, `hi disconnect` and `hi connect` again. |
| `the server … has a different key than when this device connected` | Same as above.                                       |
| `make check` fails right after `hi init`                    | Run the setup first: `uv sync`, `npm ci`, or `make sync` in `ml`. `hi init --no-setup` skips it. |
| `… has no .hifin/template.json`                             | `hi init --update` works only on projects `hi init` made. Use `hi init --adopt <template>` first. |
| `… were edited by hand and left alone`                      | Make the change in the template, or rerun `hi init --update --force` to overwrite them. |
| `… conflict with the template; nothing was written`         | `--adopt` found files `hi` would own, or `Makefile` targets it would add. Move them aside, or pass `--force`. |
| `… uses layers from …, which this device can't reach`       | Run `hi connect status`: private layers need the server. In CI, `--check` without `--strict` skips them. |
| `… made with hi …, newer than this hi`                      | `hi update`, then run it again.                              |

For admins, `hi server templates list` shows each source's commit, last fetch,
and any problem. `git fetch: no answer within 2m0s` means GitHub was slow or
unreachable; devices keep the last good commit, and the next sync retries.
`Authentication failed` usually means the source's token expired: run
`hi server templates remove <name>`, then `add` with a new token.

## Team data

| Message                                                     | What to do                                                   |
|-------------------------------------------------------------|--------------------------------------------------------------|
| `hi data needs a hi server`                                 | Connect with `hi connect <server>` first.                    |
| `This server serves no Hugging Face organizations yet`      | An admin runs `hi server data add <org>` on the server box.  |
| `hf isn't installed`                                        | `pip install -U huggingface_hub`, or `uv tool install huggingface_hub`. |
| `… is more than one kind; say dataset:… or model:…`         | Put `dataset:`, `model:`, or `bucket:` in front of the name. |
| `… is not among what you may download`                      | Check the name with `hi data ls`; your group may not see it. |
| `your group … may not read …`                               | The team's policy. Ask an admin; don't look for another way in. |
| `this hi data token is for bucket:…, not …`                 | That `hi` is older than v0.24.2 and doesn't know buckets: `hi update`. |
| `the hi data token has expired`                             | Run the command through `hi data` again; tokens last a day.  |
| `the hi server is older than v0.25.1`                       | An admin runs `hi update` on the server box.                 |
| `the server can't expose the data proxy (…); using signed links instead` | The run still works. To use the proxy, an admin turns on Peer Expose in NetBird (Settings > Clients); `hi server expose` shows the last problem. |
| `the data is too much to send with one Hugging Face job`    | Name a folder or pattern after the repository (`--data hifinab/fdb/runs/*`), or use Colab. Happens only with signed links. |
| `the download link for … has expired`                       | The job started more than about an hour after the links were made. Run it again. |

## Sharing a local service

| Message                                                     | What to do                                                   |
|-------------------------------------------------------------|--------------------------------------------------------------|
| `NetBird isn't connected on this machine`                   | Connect with `hi net` (or `hi net reconnect`) first.         |
| `peer expose is not enabled … turns on Peer Expose`         | A NetBird admin turns on Peer Expose in Settings > Clients, once. |
| `nothing answers on localhost:<port> yet`                   | Start the service, or check the port. Visitors get an error page until it answers. |
| `hi net expose: nothing answers on … on the exposing machine` (in the browser) | The service stopped or listens elsewhere; `--host` points at another address. |
| A webhook or script gets 401                                | Password, PIN, and SSO are login pages for browsers. Use `--public`, and have the service check its own token or signature. |
| `doesn't answer yet; NetBird may be slow`                   | NetBird's proxy took too long to publish the name. Try again. |
| `--max is at most 24h`                                      | Exposures always end; start a new one when it does.          |
| `choose one of --password, --pin, --groups, and --public`   | Pass at most one lock.                                       |
| `nothing named "…" is exposed`                              | `hi net expose ls` shows the names.                          |

## Installing and signing in

| Message                                                     | What to do                                                   |
|-------------------------------------------------------------|--------------------------------------------------------------|
| `hi: command not found` right after installing              | Open a new shell, or run `~/.local/bin/hi`.                  |
| `the hf CLI is not installed`                               | `hi install`, or `curl -LsSf https://hf.co/cli/install.sh \| bash` |
| `netbird is not installed; run hi install first`            | `hi install`                                                 |

Still stuck? Open an issue at [github.com/hifinab/cli](https://github.com/hifinab/cli/issues)
with the command and its output.

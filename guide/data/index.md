---
title: Download the team's data
description: Download the team's private Hugging Face datasets, models, and buckets with hi data, through the hi server, without a Hugging Face token on your machine.
---

`hi data` downloads the private Hugging Face datasets, models, and buckets
of your team's organizations. The hi server keeps the Hugging Face tokens;
your machine never gets one. It runs the official `hf` tool, pointed at
the server.

```sh
hi data                          # search, pick one, download it
hi data ls                       # everything you may download
hi data get hifinab/bars-1d      # straight to ./data/bars-1d
```

You need a machine connected to the team's server (`hi connect`) and `hf`:

```sh
pip install -U huggingface_hub   # or: uv tool install huggingface_hub
```

## Find what you need

`hi data` in a terminal shows a list with a search field. Type words in any
order, such as `gemma 27b` or `dataset fin`. Every word must match. Arrows
move, Enter downloads, and Esc clears the search, then leaves.

```sh
hi data ls                       # kind, name, size, and last update
hi data ls hifinab --kind bucket # one organization, one kind
hi data ls --json                # for scripts and agents
hi data info hifinab/finset2     # size, file count, and the largest files
```

When a dataset, a model, or a bucket share a name, put `dataset:`,
`model:`, or `bucket:` in front, as in `hi data get bucket:hifinab/fdb`.

## Download

```sh
hi data get hifinab/finset2                          # into ./data/finset2
hi data get hifinab/ranker --to models/ranker        # another folder
hi data get hifinab/bars-1d --revision v2            # a branch, tag, or commit
hi data get hifinab/fdb --include 'runs/2026-09/*'   # only some files; repeat for more
hi data get hifinab/fdb --exclude '*.tmp'
```

Datasets and models download with `hf download`, buckets with
`hf buckets sync`. Buckets have no revisions. Running `hi data get` again
fetches only what is missing or changed, so an interrupted download
continues where it stopped.

## What the server does

```text
your machine                       hi server                         Hugging Face
hi data get ──── asks for a ─────▶ checks your group
                 hi data token     
hf download ──── with the token ─▶ swaps in the team's token ──────▶ the Hub's API
            ◀──────────────────── answers and redirects
            ◀──────────────────────── file contents, straight from the CDN
```

- The server passes on only calls that read one repository or bucket:
  nothing that uploads, deletes, or changes settings.
- The hi data token works only against your server, from your machine, for
  one repository, for a day. Removing your machine or user ends it at once.
- File contents come straight from Hugging Face's storage, so large
  downloads don't go through the server.
- The server records who downloaded what. It never sees the files.

## For admins

On the server box, connect an organization with a token that can read it.
A fine-grained, read-only token limited to the organization is best:

```sh
hi server data add hifinab           # asks for the token; it shows as *
hi server data add hifinab name2     # one token for several organizations
hi server data                       # a menu: add, test, remove
hi server data list                  # organizations and what they hold
hi server data test                  # check every token now
hi server data remove name2
```

Every group can read everything by default. To limit a group, list
patterns in `policy.json` (`hi server policy edit`); an empty list allows
nothing:

```json
"students": { "data": ["hifinab/bars-1d", "hifinab/public-*"] }
```

Token grants are in the audit log (`hi server audit`), and every download
is in `data_usage.jsonl` in the server's state folder.

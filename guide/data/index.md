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

## Keep track in a project

In a project made with `hi init` (any folder with a `.hifin` folder), `hi
data get` records what it downloaded in `.hifin/data.json`: the repository,
the exact commit, the folder, and the filters. Commit the file, and a
teammate gets the same files with:

```sh
hi data get        # no name: everything .hifin/data.json records, at the recorded commits
```

A branch or tag is pinned to the commit it pointed to at the time, so a
backtest can always be traced to its data. Buckets keep no history; their
entry records the file list, and `hi data get` warns when the bucket has
changed since. `--no-record` downloads without recording.

## Use the data in code

`hi data run` runs a command that reads the team's data directly, without
a download step first:

```sh
hi data run -- python backtest.py
hi data run -- jupyter lab
```

In it, the usual Hugging Face code works with private repositories:

```python
from datasets import load_dataset
bars = load_dataset("hifinab/bars-1d", split="train")

import pandas as pd
fills = pd.read_parquet("hf://datasets/hifinab/fills/2026-09.parquet")

from transformers import AutoModel
model = AutoModel.from_pretrained("hifinab/ranker")
```

For a whole shell session:

```sh
eval "$(hi data env)"   # sets HF_ENDPOINT and HF_TOKEN; the token lasts a day
```

## In a box

Boxes from [`hi box`](/guide/box/) and [`hi agent`](/guide/agent/) hold no
credentials. With `--data`, the box's own `hf` and Python code can still
read the team's data:

```sh
hi box run --data -- python train.py
hi agent claude --data "train the ranker on hifinab/bars-1d"
```

The box gets a placeholder token; hi's proxy outside the box adds the real
one on the way to the server, and allows Hugging Face's download hosts.

## On a rented GPU

`hi compute run --data` downloads the team's data on the instance before
the script starts, through signed links that need no token:

```sh
hi compute run --gpu a10g-small --data hifinab/bars-1d train.py
```

On Hugging Face Jobs, the server opens a narrow public address with
NetBird for the run, so the script can also call `load_dataset` for the
repositories you named. See [Run a script](/guide/compute/run/#the-teams-data).

## On a machine you SSH into

`hi compute up --data` gives a RunPod or Shadeform machine the repositories
you name, through the same public address, until its `--max`:

```sh
hi compute up --on runpod --gpu rtx-4090 --name train --max 4h --data hifinab/bars-1d --data hifinab/ranker
hi compute ssh train -- hf download --repo-type dataset hifinab/bars-1d --local-dir data/bars-1d
```

Before the machine starts, hi asks the server for a token that reads only
those repositories, so a refusal costs nothing. Once the machine is up, hi
writes `HF_ENDPOINT` and the token as `HF_TOKEN` into `~/.hi/data.env` on
it (readable only by you), over SSH, and its shells load that file. The token
never appears in the provider's console. `hf download`, `load_dataset`,
and `from_pretrained` then work there for those repositories. Install `hf`
with `pip install -U huggingface_hub` if the image lacks it.

- Name whole repositories; download parts of them on the machine with
  `hf download --include`.
- Not on RunPod's Community Cloud: those are third-party hosts, so hi
  refuses.
- Through a team server that manages the provider, the start still needs
  its approval, and `--no-wait` can't be used: hi hands the machine its
  token once it is up.
- With `--max none`, the token lasts 24 hours.

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
- The hi data token works only against your server and from your machine,
  for a day; `hi data get` asks for one limited to a single repository.
  Removing your machine or user ends it at once.
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

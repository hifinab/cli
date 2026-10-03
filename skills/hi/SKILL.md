---
name: hi
description: Use the hi CLI to run work on rented remote machines (Google Colab, Hugging Face Jobs, RunPod, Shadeform) - run a Python script or container to completion on a GPU, start an SSH-able GPU box, forward its ports to localhost, serve a GGUF model with an OpenAI-compatible API, check what is running, and stop it. Also covers starting a new project from a template (hi init), Hifin workstation setup (hi install, hi verify strix), NetBird (hi net), and downloading the team's private Hugging Face datasets, models, and buckets through a hi server (hi data). Use when the user wants to train, evaluate, or test something on a GPU they do not have locally, try or serve an LLM remotely, see or stop running remote compute, get the team's data or models, or set up a Hifin machine. Not for local model serving or cloud infrastructure management.
---

# hi

`hi` is the Hifin command-line tool. Its `hi compute` commands rent remote
machines from Colab, Hugging Face, RunPod, or Shadeform with the same commands
on all of them, and give them back. `hi init` starts new projects from
templates. Run `hi compute help` for the full reference; the user-facing guide
is at https://hifin.sh/guide/ if the user needs step-by-step instructions.

## Rules

Remote compute costs money (US dollars on Hugging Face, prepaid compute units
on Colab). Follow these rules every time:

1. **Ask before spending.** Before any `hi compute run`, `up`, or `serve` on
   paid hardware, tell the user the provider, hardware, price per hour, and
   maximum lifetime, and get their explicit yes. Show them the plan with
   `--dry-run` first. Only then add `--yes`, which you need because you have no
   terminal for hi's own confirmation prompt. Never add `--yes` on your own
   judgement.
2. **Always set `--max`.** Choose the shortest lifetime that fits the task
   (for example `--max 30m` for a quick test; a bare number is hours). Every
   machine stops itself at `--max`; the defaults are 4h for instances and 1h
   for runs. Never use `--max none` (no limit) unless the user asks for it
   explicitly.
3. **Clean up.** When you are done with a machine you started, stop it with
   `hi compute stop <name>`, then check `hi compute ls` shows nothing left
   running that you started. Never stop machines you did not start without
   asking.
4. **Keep secrets secret.** Pass credentials with `--secret NAME` (Hugging Face
   encrypts them), never with `--env`, and never print tokens or put them in
   commands, files, or logs. **Never put any token, password, key, or
   sensitive data on a RunPod Community Cloud machine** (hardware ending in
   `@community`): it is a third-party host. Use Community only for public
   code and data, and only when the user chose it.
5. **Ask before system changes.** `hi install`, `hi adduser`, and `hi net` need
   sudo and change the machine. Only run them when the user asks.
6. **Managed compute waits for a person.** When `hi compute providers` says a
   provider is `managed by` a hi server, starting there sends a request that
   someone else approves. Rules 1 and 2 still apply. Never run
   `hi server approve` or `hi server deny`, and never retry a denied request
   with other hardware or another name to get around the decision.

## Choosing a provider and hardware

```sh
hi compute providers                 # which providers are signed in
hi compute hardware                  # names, memory, and prices for each provider
hi compute hardware --on hf
```

Hardware names are the provider's own: Colab uses `cpu`, `T4`, `L4`, `G4`
(96 GB), `A100`, `H100`; Hugging Face uses flavors such as `cpu-basic`,
`t4-small`, `a10g-small`, `l40sx1`, `a100-large`, `rtx-pro-6000` (96 GB);
RunPod uses short GPU names such as `rtx-4090` and `a100-pcie` (see
`hi compute hardware --on runpod`). RunPod supports `up`, `ssh`, `tunnel`,
and `stop`, but not `run`. Shadeform rents virtual machines from many clouds:
`h100` is the cheapest free H100 on any of them, `h100@lambdalabs` pins a
cloud, and like RunPod it supports `up`, `ssh`, `tunnel`, and `stop` but not
`run`. Its machines boot in 3-8 minutes.
When both providers are signed in, the hardware name picks the provider;
otherwise pass `--on colab`, `--on hf`, or `--on runpod`.

`hi compute billing` shows which Hugging Face account pays (the user's own if
it can pay, otherwise their only organization that can) and the Colab
compute-unit balance. The start line of every paid command names the payer,
as in `($1.00/h, billed to hifinab)`; include it when you ask the user.
Changing the saved payer with `hi compute billing ACCOUNT` is the user's
decision.

If a provider is not ready, tell the user the fix rather than working around
it: `hi login hf`, `hi login colab`, or `hi install`.

## Run something to completion

Best for training, evaluation, batch jobs, and quick experiments. Logs stream
until the run ends, and `hi` exits with the run's own exit code.

```sh
# A uv script (PEP 723 inline dependencies are installed remotely)
hi compute run --gpu a10g-small --max 1h --dry-run train.py -- --epochs 3
hi compute run --gpu a10g-small --max 1h --yes train.py -- --epochs 3

# A container image and command (Hugging Face)
hi compute run --on hf --max 10m --yes python:3.12 -- python -c 'print(1)'

# Long runs: detach, then follow or wait (Hugging Face)
hi compute run --gpu a10g-small --max 4h --detach --name sweep --yes train.py
hi compute logs sweep --follow
hi compute wait sweep
```

Colab runs Python scripts only, cannot detach, and has no secret store.
Hugging Face scripts are limited to 96 KB; larger code belongs in an image or
a URL.

## Use an interactive machine

```sh
hi compute up --gpu T4 --name box --max 2h --yes
hi compute ssh box -- nvidia-smi           # run one command; omit it for a shell
hi compute tunnel box 8000                 # remote port 8000 on 127.0.0.1:8000
hi compute status box
hi compute stop box
```

`tunnel` runs in the foreground until interrupted, so start it in the
background if you need to keep working, and stop it when done. On Colab, port
8080 belongs to Colab and cannot be tunnelled. Hugging Face SSH needs the
user's public key registered at https://huggingface.co/settings/keys.

## Serve a model

`serve` starts a machine, runs llama.cpp with a GGUF model, and waits until it
answers.

```sh
hi compute serve --on colab --max 1h --yes qwen3.8-flash-next    # tested recipe, Colab G4
hi compute serve --on hf --gpu t4-small --quant Q4_K_M --max 30m --yes unsloth/Qwen3-0.6B-GGUF
hi compute logs <name> --follow                                  # build, download, server
```

- On Colab, `serve` then tunnels the API to `http://127.0.0.1:8080/v1` and
  stays in the foreground.
- On Hugging Face, it prints an HTTPS URL such as
  `https://<job>--8000.hf.jobs/v1` and returns. Requests need the user's
  Hugging Face token as the API key; read it from `$HF_TOKEN` or the token
  file inside the program that calls the API, and never print it.

Use any OpenAI-compatible client with the printed base URL and model name.
Stop the machine when finished.

## Managed compute (a team's hi server)

If the user's machine has joined a hi server (`hi connect status` says so),
the providers it manages need an approval, a reason, and a time limit. Colab
and other providers still use the user's own sign-in.

```sh
hi compute up --on runpod --gpu rtx-4090 --max 1h --reason "eval of the new tokenizer" --yes --no-wait
# prints the request ID, such as r-9b41e0, and exits with status 3 (pending)
hi compute requests r-9b41e0 --wait --timeout 10m
```

1. Write `--reason` as one line the approvers will understand.
2. Use `--no-wait`, tell the user the request ID and that it waits for
   approval, then wait with `hi compute requests <id> --wait --timeout 10m`.
3. Exit status 0 means it is running; carry on as with any instance. Status 3
   means it is still pending: tell the user and check again later. Status 4
   means it was denied: tell the user the approver's reason and stop there.
4. `--json` on `hi compute requests` gives the request in a form you can
   read.
5. If a machine needs longer, ask with
   `hi compute extend <name> 1h --reason "..." --no-wait`; it needs approval
   like a start. Never start a second machine to get around a limit.
6. A `Warning: This month: … Over … budget` line means the user is over
   budget. Tell the user; the request still goes to a person.

Stopping never needs approval, and you can stop only the user's own
machines.

## Team data (hi data)

On a machine connected to a hi server, the team's private Hugging Face
datasets, models, and buckets come through `hi data`. Never look for, ask
for, or use a Hugging Face token for them: the server keeps it, and `hf`
runs without one.

```sh
hi data ls --json                    # what the user may download: kind, id, size, updated
hi data ls hifinab --kind dataset    # one organization, one kind
hi data info hifinab/bars-1d         # size, file count, and the largest files
hi data get hifinab/bars-1d          # into ./data/bars-1d with hf
hi data get hifinab/fdb --include 'runs/*' --to data/fdb-runs
```

1. Check the size with `hi data info` before a download, and tell the user
   before anything over a few GB. Use `--include` to fetch only what the
   task needs.
2. Put `dataset:`, `model:`, or `bucket:` in front when a name is more than
   one kind; the error says which.
3. A "may not read" error is the team's policy: tell the user, and don't
   try another way to reach the data.
4. If `hf` is missing, `pip install -U huggingface_hub` installs it.
5. In a project, `hi data get` records what it fetched, at which commit, in
   `.hifin/data.json`; keep that file in the commit that uses the data.
   `hi data get` with no name fetches everything recorded.
6. For code that reads the data itself (`load_dataset`, `from_pretrained`,
   `hf://` paths in pandas), run it with `hi data run -- <command>`
   instead of downloading first. Don't set `HF_TOKEN` yourself.
7. Inside a hi box (`$HI_BOX` is set), `HF_ENDPOINT` and a placeholder
   `HF_TOKEN` are already set when the box was started with `--data`; use
   `hf` or Python directly. Without them, ask the user to start the box
   with `--data`.

## Check and clean up

```sh
hi compute ls           # everything running, when it stops, and the Colab balance
hi compute stop <name>
```

Always pass the name to `stop`: without one it opens an interactive picker,
which needs a terminal. `ls` also stops instances past their limit. Names can be given as `name` or
`provider/name`, such as `hf/6ab97c...`.

## Troubleshooting

| Message                                       | Meaning and fix                                                    |
|-----------------------------------------------|--------------------------------------------------------------------|
| `confirmation needed; rerun with --yes`       | Paid hardware: get the user's yes, then add `--yes`.               |
| `Hugging Face Jobs need pre-paid credits`     | Tell the user; `hi compute billing` shows which account can pay.   |
| `several providers are ready`                 | Add `--on colab` or `--on hf`, or a provider-specific `--gpu`.     |
| `Permission denied (publickey)` on Hugging Face | The SSH key is not registered on the Hub account.                |
| `unknown ... hardware`                        | Use a name from `hi compute hardware`.                             |
| A `serve` step failed                         | `hi compute logs <name>` shows the build, download, and server logs. |
| `approvers need a reason`                     | Managed provider: add `--reason "..."`.                            |
| `... is waiting for approval` (exit 3)        | Tell the user; check with `hi compute requests <id> --wait`.       |
| `... was denied by ...` (exit 4)              | Tell the user the reason; do not retry around it.                  |
| `can't reach the hi server`                   | Managed providers are unavailable; tell the user. Do not use another key. |

## Start a project

`hi init` creates a repository from a template: `make check`, `AGENTS.md`
rules, Claude Code settings, this skill, and CI.

```sh
hi init --list                               # templates and where they come from
hi init python pricing-tools --dry-run       # every file and command, nothing written
hi init python pricing-tools --yes           # after the user agreed to the plan
```

Other templates: `web`, `service`, `pipeline`, `ml`, and the team's private
ones on a connected device. `hi init --update --dry-run` shows how a project
differs from the current templates, and `hi init --update` applies it; if it
writes `docs/upgrades/<version>.md`, apply what fits from it, run
`make check`, and delete the file. `hi init --adopt <template>` brings an
existing repository under a template without touching its code.

Only create, update, or adopt a project when the user asks for it, and show
them the `--dry-run` plan first. Never use `--force` unless they ask: it
overwrites their hand edits. `hi init` never overwrites a file with other content;
if it lists conflicts, tell the user instead of moving their files. In a
project made by `hi init`, run `make check` before saying a change is done,
and follow its `AGENTS.md`.

## Workstation and network

These change the machine and need sudo; run them only when asked.

- `hi install`: a checkbox menu of the Hifin workstation software (and AMD
  ROCm for Strix Halo); unchecking an installed tool removes it. Without a
  terminal, use `hi install <tool>...` or `hi install --all`, and
  `hi uninstall <tool>...`; `hi install --list` is read-only and safe to run.
  `hi verify strix` checks a Strix Halo machine afterwards.
- `hi net`: enroll the machine in NetBird (it prompts for the setup key).
  `hi net status` is read-only and safe to run.
- `hi version`: the installed version. `hi update --check` says whether a
  newer release exists; `hi update` installs it (ask first).

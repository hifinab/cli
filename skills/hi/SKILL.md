---
name: hi
description: Use the hi CLI to run work on rented remote machines (Google Colab, Hugging Face Jobs) - run a Python script or container to completion on a GPU, start an SSH-able GPU box, forward its ports to localhost, serve a GGUF model with an OpenAI-compatible API, check what is running, and stop it. Also covers Hifin workstation setup (hi install, hi verify strix) and NetBird (hi net). Use when the user wants to train, evaluate, or test something on a GPU they do not have locally, try or serve an LLM remotely, see or stop running remote compute, or set up a Hifin machine. Not for local model serving or cloud infrastructure management.
---

# hi

`hi` is the Hifin command-line tool. Its `hi compute` commands rent remote
machines from Colab or Hugging Face with the same commands on both, and give
them back. Run `hi compute help` for the full reference.

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
   (for example `--max 30m` for a quick test). Every machine stops itself at
   `--max`; the defaults are 4h for instances and 1h for runs.
3. **Clean up.** When you are done with a machine you started, stop it with
   `hi compute stop <name>`, then check `hi compute ls` shows nothing left
   running that you started. Never stop machines you did not start without
   asking.
4. **Keep secrets secret.** Pass credentials with `--secret NAME` (Hugging Face
   encrypts them), never with `--env`, and never print tokens or put them in
   commands, files, or logs.
5. **Ask before system changes.** `hi install`, `hi adduser`, and `hi net` need
   sudo and change the machine. Only run them when the user asks.

## Choosing a provider and hardware

```sh
hi compute providers                 # which providers are signed in
hi compute hardware                  # names, memory, and prices for each provider
hi compute hardware --on hf
```

Hardware names are the provider's own: Colab uses `cpu`, `T4`, `L4`, `G4`
(96 GB), `A100`, `H100`; Hugging Face uses flavors such as `cpu-basic`,
`t4-small`, `a10g-small`, `l40sx1`, `a100-large`, `rtx-pro-6000` (96 GB).
When both providers are signed in, the hardware name picks the provider;
otherwise pass `--on colab` or `--on hf`.

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

## Check and clean up

```sh
hi compute ls           # everything running, when it stops, and the Colab balance
hi compute stop <name>
```

`ls` also stops instances past their limit. Names can be given as `name` or
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

## Workstation and network

These change the machine and need sudo; run them only when asked.

- `hi install` / `hi install strix`: install the Hifin workstation software
  (and AMD ROCm for Strix Halo). `hi verify strix` checks it afterwards.
- `hi net`: enroll the machine in NetBird (it prompts for the setup key).
  `hi net status` is read-only and safe to run.
- `hi version`: the installed version.

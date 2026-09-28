---
title: Costs, limits, and billing
description: What remote compute costs on each provider, who pays, how hi asks before spending, and how every machine is guaranteed to stop.
---

## What things cost

| Provider     | You pay with                                  | See prices with               |
|--------------|-----------------------------------------------|-------------------------------|
| Colab        | Prepaid compute units from your Colab plan    | `hi compute hardware --on colab`, `hi compute billing` |
| Hugging Face | US dollars, billed per minute while a job runs | `hi compute hardware --on hf` |
| RunPod       | US dollars from your account balance, while a pod exists | `hi compute hardware --on runpod` |

Some real numbers from testing `hi`:

| What                                                   | Cost                 |
|--------------------------------------------------------|----------------------|
| Colab CPU / T4 / G4                                    | ~0.08 / ~1.07 / ~8.9 units per hour |
| Serving Qwen3.8-Flash-Next on a Colab G4 for a test     | 0.93 units in total  |
| Hugging Face `cpu-basic` / `t4-small` / `a10g-small` / `rtx-pro-6000` | $0.01 / $0.40 / $1.00 / $2.75 per hour |
| A day of Hugging Face test jobs, including a `t4-small` model server | under $0.05 |

Colab's rates for other GPUs are shown as `unmeasured`; `hi compute billing`
shows your balance and the rate you are using right now.

## hi asks before spending

Starting paid hardware prints the price, the limit, and on Hugging Face the
account that pays, then asks:

```text
Run train.py on hf a10g-small ($1.00/h, billed to hifinab), stopping after 1h.
Start it? [y/N]
```

Colab CPU and Hugging Face hardware under $0.10 per hour start without asking.
In scripts, where there is no terminal to ask in, `hi` stops with
`confirmation needed; rerun with --yes` until you add `--yes`.

## Every machine stops by itself

Every run and instance has a maximum lifetime set with `--max`. Give it in
hours (`2`, `1.5`) or with a unit (`30m`, `1h30m`, `2d`):

| Kind      | Default | Upper limit        |
|-----------|---------|--------------------|
| Instance  | 4h      | 24h on Colab       |
| Run       | 1h      | 24h on Colab       |

How the limit is enforced differs by provider:

- **Hugging Face** stops the job itself: `--max` becomes the job's timeout.
  It stops even if your laptop is off.
- **Colab** cannot stop a runtime from inside it, so `hi compute up` starts a
  small background process on your laptop that stops the machine at `--max`.
  It survives closed terminals and sleep. It does not survive a reboot, so
  every `hi compute ls` also stops machines past their limit, and Colab ends
  sessions after 24 hours regardless.
- **Colab runs** pass `--max` to the Colab CLI as the run's timeout, and the
  runtime is released when the run ends.
- **RunPod** has no built-in limit either. `hi` installs a watchdog on the pod
  that terminates it at `--max`, and runs the local watcher as well.

Choose the shortest `--max` that fits the work. You can always start another.

### No time limit

`--max none` (or an empty answer to the menu's lifetime question) starts a
machine without a limit. It keeps running, and costing money, until you stop
it, so `hi` shows a warning and asks you to confirm; in scripts, add `--yes`.

- On Hugging Face, the job gets a one-year timeout.
- On Colab, no watcher runs, and Colab still ends the session after 24 hours.
- `hi compute ls` shows `no limit` in the STOPS IN column.

## Check what is running

```sh
hi compute ls
```

```text
NAME   PROVIDER  HARDWARE    UP   STOPS IN
box    colab     G4          42m  3h18m
sweep  hf        a10g-small  5m   55m

Colab balance: 1795.89 compute units, currently using 8.90 units/h
```

Stop everything you started with `hi compute stop --all`.

## Who pays on Hugging Face

Hugging Face Jobs need pre-paid credits on the account that pays. You may
belong to organizations; `hi` picks the payer in this order:

1. `--namespace <account>` on the command.
2. The `HI_HF_NAMESPACE` environment variable.
3. The account saved with `hi compute billing <account>`.
4. Your own account, if it can pay.
5. Your only organization that can pay.

`hi compute billing` shows your accounts and which one `hi` bills:

```sh
hi compute billing
```

```text
Hugging Face:
ACCOUNT    TYPE  CAN PAY  PLAN  BILLED
quantbert  user  no
hifinab    org   yes      team  <- hi bills this

Chosen by: automatic: your own account if it can pay, else your only organization that can
```

Choose one explicitly, or go back to automatic:

```sh
hi compute billing hifinab
hi compute billing --clear
```

Hugging Face reports whether an account can pay, but not its credit balance;
check that at [huggingface.co/settings/billing](https://huggingface.co/settings/billing).
If a job is refused with `Hugging Face Jobs need pre-paid credits`, add credits
or choose an account that can pay.

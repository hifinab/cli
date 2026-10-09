---
title: Run a parameter sweep
description: Start several detached training runs with different settings on Hugging Face, wait for all of them, and collect their results.
---

Detached runs make sweeps simple: start one run per setting, then wait for
all of them. This uses the `train.py` from
[Train with uv scripts](/guide/tutorials/train/) and Hugging Face, which
supports `--detach`.

> Every run bills separately. Five `a10g-small` runs cost five times $1.00 per
> hour while they run. Start with a short `--max` and a CPU flavor.
{: .warning}

## Start the runs

```sh
for epochs in 50 100 200 400; do
  hi compute run --gpu a10g-small --max 30m --detach --yes \
    --name "sweep-e$epochs" train.py -- --epochs "$epochs"
done
```

`--yes` confirms each start, because the loop has no terminal to ask in;
decide on the cost before running it. Each run prints its job URL.

## Watch them

```sh
hi compute ls
```

```text
NAME         PROVIDER  HARDWARE    UP  STOPS IN
sweep-e100   hf        a10g-small  1m  29m
sweep-e200   hf        a10g-small  1m  29m
sweep-e400   hf        a10g-small  1m  29m
sweep-e50    hf        a10g-small  1m  29m
```

```sh
hi compute logs sweep-e400 --follow
```

## Wait for all of them

`hi compute wait` takes several names and exits non-zero if any run failed:

```sh
hi compute wait sweep-e50 sweep-e100 sweep-e200 sweep-e400 && echo "all succeeded"
```

## Collect the results

Logs stay available after the runs end:

```sh
for epochs in 50 100 200 400; do
  printf '%s\t' "$epochs"
  hi compute logs "sweep-e$epochs" -n 5 | grep 'loss' | tail -1
done
```

## Stop early

```sh
hi compute stop sweep-e400    # one
hi compute stop --all         # everything, after asking
```

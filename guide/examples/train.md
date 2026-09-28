---
title: Train with uv scripts
description: Train a model on a rented GPU from a single Python file with inline dependencies, pass secrets safely, follow progress, and keep the results.
---

A training script with its dependencies declared inline runs on any provider
without an image or requirements file. This example trains a small network on
a GPU and uploads the result to the Hugging Face Hub, because the machine and
its disk disappear when the run ends.

## The script

Save this as `train.py`:

```python
# /// script
# requires-python = ">=3.10"
# dependencies = ["torch", "numpy", "huggingface_hub"]
# ///
import argparse
import os

import torch
from huggingface_hub import HfApi

parser = argparse.ArgumentParser()
parser.add_argument("--epochs", type=int, default=5)
parser.add_argument("--repo", help="Hub repository to upload the model to")
args = parser.parse_args()

device = "cuda" if torch.cuda.is_available() else "cpu"
print(f"training on {device}", flush=True)

# Learn y = 3x + 1 from noisy samples.
x = torch.randn(10_000, 1, device=device)
y = 3 * x + 1 + 0.1 * torch.randn_like(x)
model = torch.nn.Linear(1, 1).to(device)
optimizer = torch.optim.SGD(model.parameters(), lr=0.1)

for epoch in range(args.epochs):
    optimizer.zero_grad()
    loss = torch.nn.functional.mse_loss(model(x), y)
    loss.backward()
    optimizer.step()
    print(f"epoch {epoch + 1}: loss {loss.item():.4f}", flush=True)

torch.save(model.state_dict(), "model.pt")
if args.repo:
    api = HfApi(token=os.environ["HF_TOKEN"])
    api.create_repo(args.repo, exist_ok=True, private=True)
    api.upload_file(path_or_fileobj="model.pt", path_in_repo="model.pt", repo_id=args.repo)
    print(f"uploaded to https://huggingface.co/{args.repo}")
```

`flush=True` makes each line appear in the streamed logs as it happens.

## Try it cheaply first

Run it on a CPU with a few epochs to check it works:

```sh
hi compute run --on hf --max 10m train.py -- --epochs 3
```

## Train on a GPU

```sh
export HF_TOKEN=$(hf auth token)
hi compute run --gpu a10g-small --max 1h --name train \
  --secret HF_TOKEN \
  train.py -- --epochs 200 --repo <you>/tiny-linear
```

- `--secret HF_TOKEN` sends your token encrypted, so the script can upload.
  Never pass tokens with `--env`.
- The output streams until the run ends, and `hi` exits with the script's
  exit code.

On Colab, drop `--secret` (Colab has no secret store) and use `--gpu T4`; save
results by printing them or by uploading with a token you type into an
instance instead.

## Long training: detach

For runs that take hours, detach and check in later:

```sh
hi compute run --gpu a10g-small --max 8h --detach --name train \
  --secret HF_TOKEN train.py -- --epochs 100000 --repo <you>/tiny-linear

hi compute logs train --follow     # watch; Ctrl+C stops watching, not the run
hi compute wait train && echo done # block until it ends
```

## Log to Weights & Biases or similar

Add the package to the script's dependencies and pass its key as a secret:

```sh
export WANDB_API_KEY=…
hi compute run --gpu a10g-small --secret WANDB_API_KEY --secret HF_TOKEN train.py
```

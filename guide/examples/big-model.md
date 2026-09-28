---
title: Try a 125B model on a 96 GB GPU
description: Serve Qwen3.8-Flash-Next, a 125B mixture-of-experts model, on a single 96 GB GPU and chat with it from your laptop - about seven minutes from nothing to answers.
---

Qwen3.8-Flash-Next has 125 billion parameters, but only about 6 billion are
active for each token, plus a 51B n-gram table that can live in host RAM.
That makes it usable on one 96 GB GPU. The `qwen3.8-flash-next` recipe has the
settings that make it fit.

**You need:** Colab Pro+ with a `G4`, or Hugging Face credits for an
`rtx-pro-6000`. **Cost:** about 9 compute units or $2.75 per hour.

## 1. Preview

```sh
hi compute serve qwen3.8-flash-next --on colab --name qwen --max 1h --dry-run
```

```text
Recipe qwen3.8-flash-next, tested on Colab G4, 2026-09-27: ~84 tokens/s through hi compute serve (~103 in colab-runner), 62.9 GB VRAM at 131k context, ready in ~7 min.
Start colab/qwen on G4 (~8.9 units/h), stopping after 1h at 15:10.
Would run: colab new -s qwen --gpu G4
Would serve unsloth/Qwen3.8-Flash-Next-GGUF:UD-Q3_K_XL on port 8000.
```

## 2. Serve

```sh
hi compute serve qwen3.8-flash-next --on colab --name qwen --max 1h
```

The machine builds llama.cpp and downloads the 90 GB model at the same time:

| Step                              | Time on a Colab G4 |
|-----------------------------------|--------------------|
| Start the machine                 | ~20 s              |
| Build llama.cpp (in parallel)     | ~2 min             |
| Download 90 GB from the Hub       | ~4.5 min           |
| Load the model                    | < 1 min            |
| **Total**                         | **~7 min**         |

When it is ready, `hi` tunnels the API to your laptop:

```text
OpenAI-compatible API: http://127.0.0.1:8080/v1   model: qwen3.8-flash-next
Forwarding http://127.0.0.1:8080 to qwen port 8000. Press Ctrl+C to close.
```

Leave this terminal open and use another one.

## 3. Chat with it

```sh
curl -s http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model": "qwen3.8-flash-next", "max_tokens": 2000,
       "messages": [{"role": "user", "content": "What is a compute unit on Google Colab?"}]}' \
  | python3 -c 'import json, sys; print(json.load(sys.stdin)["choices"][0]["message"]["content"])'
```

It generates about 84 tokens per second through `hi`. It thinks before
answering; the thinking arrives separately in `reasoning_content`, so give it
a generous `max_tokens`.

## 4. Stop

Press Ctrl+C in the serving terminal and answer `y` to stop the machine, or
from anywhere:

```sh
hi compute stop qwen
```

## Why the recipe works

The recipe passes these `llama-server` flags:

- `-ot 'per_layer_token_embd\.weight=CPU'` keeps the 28.8 GB n-gram table in
  host RAM and everything else on the GPU, using about 63 GB of the 96 GB of
  VRAM at a 131k context.
- `--lazy-mode off` loads that table fully into RAM instead of streaming it
  from disk, which is slow on Colab's network storage.

## On Hugging Face

The same recipe runs on an `rtx-pro-6000`, which also has 96 GB:

```sh
hi compute serve qwen3.8-flash-next --on hf --name qwen --max 1h
```

There, `llama-server` downloads the model itself, and `hi` prints an HTTPS URL
instead of tunnelling; use your Hugging Face token as the API key. See
[Serve a model](/guide/compute/serve/#hugging-face).

> The recipe was measured on a Colab G4. The Hugging Face flow was tested with
> a smaller model; expect similar speed on the same GPU.
{: .note}

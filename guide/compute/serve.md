---
title: Serve a model
description: Start a GPU, run a GGUF model with llama.cpp, and use it through an OpenAI-compatible API from curl, Python, or any OpenAI-compatible tool.
---

`hi compute serve` rents a GPU, starts [llama.cpp](https://github.com/ggml-org/llama.cpp)'s
server with a GGUF model, waits until it answers, and gives you an
OpenAI-compatible URL. It takes a tested recipe or any GGUF repository on the
Hugging Face Hub.

## With a recipe

Recipes pick the model file, hardware, context length, and server flags that
have been tested together:

```sh
hi compute serve qwen3.8-flash-next --on colab --max 1h
```

| Recipe               | Model                                      | Hardware                             | Measured                     |
|----------------------|--------------------------------------------|--------------------------------------|------------------------------|
| `qwen3.8-flash-next` | `unsloth/Qwen3.8-Flash-Next-GGUF`, `UD-Q3_K_XL` (90 GB), 131k context | Colab `G4`, Hugging Face `rtx-pro-6000` (96 GB) | Colab G4: ready in ~7 min, ~84 tokens/s |

[Try a 125B model on a 96 GB GPU](/guide/tutorials/big-model/) walks through
this one.

## With any GGUF model

Name a Hugging Face repository that contains GGUF files, a quantization that
exists in it, and hardware with enough memory:

```sh
hi compute serve unsloth/Qwen3-8B-GGUF --quant Q4_K_M --gpu L4 --max 1h
hi compute serve unsloth/Qwen3-0.6B-GGUF --quant Q4_K_M --gpu t4-small --max 30m
```

Choose hardware whose GPU memory is at least the model file's size plus a few
GB for the context. A 5 GB `Q4_K_M` file fits a 16 GB T4 comfortably; a 90 GB
file needs a 96 GB GPU.

| Option            | Meaning                                                     |
|-------------------|-------------------------------------------------------------|
| `--quant <quant>` | The quantization in the file names, such as `Q4_K_M` or `UD-Q3_K_XL` |
| `--gpu <hardware>`| GPU hardware; recipes choose their own                       |
| `--ctx <tokens>`  | Context length; default 32768 (recipes set their own)        |
| `--alias <id>`    | Model name in the API; default from the repository name      |
| `--args "<args>"` | Extra `llama-server` arguments                               |
| `--port <port>`   | Local port for the Colab tunnel; default 8080                |
| `--max`, `--name`, `--on`, `--yes`, `--dry-run` | As for `hi compute up`         |

## What happens on each provider

### Colab

1. `hi` starts the machine, uploads a setup script over SSH, and runs it in the
   background, so a dropped connection never stops setup.
2. The script builds llama.cpp with CUDA and downloads the model in parallel.
   `hi` prints each step as it changes:
   ```text
     [19:57:04] building llama.cpp and downloading unsloth/Qwen3.8-Flash-Next-GGUF:UD-Q3_K_XL  (model on disk: 4.5G)
     [19:58:58] llama.cpp built, downloading …  (model on disk: 39G)
     [20:01:24] loading model  (model on disk: 84G)
     [20:03:57] ready
   OpenAI-compatible API: http://127.0.0.1:8080/v1   model: qwen3.8-flash-next
   ```
3. It then tunnels the API to `http://127.0.0.1:8080/v1` and stays in the
   foreground. Press Ctrl+C to close the tunnel; `hi` asks whether to stop the
   machine.

If setup fails or you close the terminal, run the same `serve` command with
the same `--name` again: the script skips what is already done.

### Hugging Face

1. `hi` starts one job that is the server, using llama.cpp's official CUDA
   image. `llama-server` downloads the model itself.
2. When the server answers, `hi` prints an HTTPS URL and returns:
   ```text
     [20:34:55] downloading and loading unsloth/Qwen3-0.6B-GGUF:Q4_K_M
     [20:35:36] ready
   OpenAI-compatible API: https://6ab97de2…--8000.hf.jobs/v1   model: qwen3-0.6b-gguf
   It needs your hf token as the API key, and stops after 30m.
   ```
3. The URL works from anywhere, but only with a Hugging Face token that can
   read the account that pays; without one, it answers `401`. Use your token
   as the API key.

To use it only from your laptop instead, tunnel it:
`hi compute tunnel <name> 8000:8080`.

## Use the API

The server speaks the OpenAI chat completions API.

```sh
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model": "qwen3.8-flash-next", "messages": [{"role": "user", "content": "Hello"}]}'
```

For a Hugging Face URL, add the token as a header:

```sh
export HF_TOKEN=$(hf auth token)
curl https://<job>--8000.hf.jobs/v1/models -H "Authorization: Bearer $HF_TOKEN"
```

From Python with the `openai` package:

```python
import os
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:8080/v1",  # or the https://…hf.jobs/v1 URL
    api_key=os.environ.get("HF_TOKEN", "unused"),
)
reply = client.chat.completions.create(
    model="qwen3.8-flash-next",
    messages=[{"role": "user", "content": "Explain compute units in one sentence."}],
)
print(reply.choices[0].message.content)
```

Most OpenAI-compatible tools accept the same two settings, often as
environment variables:

```sh
export OPENAI_BASE_URL=http://127.0.0.1:8080/v1
export OPENAI_API_KEY=unused      # or your Hugging Face token for an hf.jobs URL
```

> Reasoning models such as Qwen3 think before answering. The thinking arrives
> in `reasoning_content` and counts toward `max_tokens`, so give them room
> (for example `"max_tokens": 2000`) or the answer may be empty.
{: .tip}

## Watch and troubleshoot

```sh
hi compute logs <name>            # setup, download, and server logs
hi compute logs <name> --follow
hi compute status <name>
```

| Symptom                                    | Likely cause                                                  |
|--------------------------------------------|---------------------------------------------------------------|
| `failed: download`                         | The repository or `--quant` does not exist, or the model is gated and needs a token |
| `failed: llama-server exited`              | The model does not fit in GPU memory; choose a smaller quant or larger GPU |
| `serving needs a GPU`                      | `--gpu` names CPU hardware                                    |
| `Address already in use` (Colab)           | Local port 8080 is taken; add `--port 8090`                   |
| `401` from an `hf.jobs` URL                | Missing or wrong token in the `Authorization` header          |

## Stop

A model server keeps costing money until it stops:

```sh
hi compute stop <name>
```

---
name: hi-data
description: Read the team's private Hugging Face datasets and models through the hi server, in a hi box or on a machine started with --data. Use when a task needs the team's data, such as hifinab/bars-1d, and before asking anyone for a Hugging Face token.
---

# The team's data through hi

The team's datasets and models live on Hugging Face and are read through
the hi server, never with a personal token. In a box started with
`hi agent --data` (or `hi box --data`), the environment is ready:

```sh
echo "$HF_ENDPOINT"     # the hi server's data proxy
```

`HF_TOKEN` is a placeholder; hi's proxy outside the box adds the real
token, so it never enters the box. Use Hugging Face's own tools:

```sh
hf download --repo-type dataset hifinab/bars-1d --local-dir data/bars-1d
hf download --repo-type dataset hifinab/bars-1d --include "data/2025-*" --local-dir data/bars-1d
```

```python
from datasets import load_dataset
bars = load_dataset("hifinab/bars-1d", split="train")
df = bars.to_pandas()
```

- If `HF_ENDPOINT` isn't set, the box was started without `--data`. Stop
  and say so in your report: the person can run the task again with
  `--data`. Don't look for a token anywhere else.
- A repository the person may not read answers 401 or 403. Name it in your
  report rather than try other names.
- Download only what the task needs: use `--include` with a pattern, or
  read with DuckDB straight from the downloaded Parquet files.
- Keep the data in the working folder's `data/` and don't commit it to
  git; mention its size in your report.

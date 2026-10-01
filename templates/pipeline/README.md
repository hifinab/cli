# Hifin Template Name

A data pipeline: fetch one date, land the raw response in `data/raw/`, parse
it, and load it into `data/warehouse.sqlite`.

```sh
uv sync
make check
uv run hifin-template-name run --date 2026-01-02
```

Schedule it with the team's scheduler; rerunning a date is always safe.

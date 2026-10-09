"""The results of a hi agent best-of run: every attempt, re-run, in results/<run>/.

    make results              the newest run in this project
    make results ID=b-2       another run
    make results HOLDOUT=none without the holdout

make loop runs it by itself when the rounds end (hi agent best-of --then);
run it by hand after a run that stopped early or failed. It covers every
round that finished. Run it on this machine, not in a box: it reads the
holdout, and boxes must never see what it writes (results/ is ignored by git).

results/<run>/
  report.html          the report: open it in a browser, no server needed;
                       it reads the Parquet tables below, embedded in it
  run.parquet          one row: the run, its limits, and how it ended
  attempts.parquet     one row per attempt (the baseline is attempt 0): who,
                       what, the score hi recorded, and every metric re-run
                       in-sample and on the holdout
  rounds.parquet       one row per round
  returns.parquet      each attempt's net monthly returns, and the benchmark's
                       (attempt -1), from the first scored month on
  weights.parquet      the weights each attempt held each month
  strategies/<name>.py strategy.py as each attempt left it
  run.json             hi agent best-of show <run> --json, as it was

Every attempt is scored on the holdout. That spends it: a version picked
because it looks good there has been fitted to the holdout, which then
tests nothing. Read the holdout columns as a check on the loop.
"""

import argparse
import base64
import hashlib
import json
import math
import os
import subprocess
import sys
import types
from collections.abc import Callable
from concurrent.futures import ProcessPoolExecutor
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

import pandas as pd

import evaluate
from prepare import EVAL, HOLDOUT, SPLIT, TICKERS

HERE = Path(__file__).resolve().parent
REPORT = HERE / "report"
BENCHMARK = evaluate.BENCHMARK
SCORE_KEY = "sharpe"  # the key of evaluate.score() that make score prints last
ROUND_COLUMNS = ["round", "result", "started", "ended", "spend_usd", "best_before", "score"]
ROUND_COLUMNS += ["winner", "agent", "idea", "commit"]
METRICS = ["cagr_pct", "ann_vol_pct", "max_drawdown_pct", "turnover_per_year", "sharpe"]

Run = dict[str, Any]


def hi_json(*args: str) -> Any:
    try:
        output = subprocess.run(
            ["hi", "agent", "best-of", *args, "--json"], capture_output=True, text=True, check=True
        ).stdout
    except FileNotFoundError:
        raise SystemExit("make results needs hi, on this machine (not in a box)") from None
    except subprocess.CalledProcessError as error:
        raise SystemExit(f"hi agent best-of {' '.join(args)}: {error.stderr.strip()}") from None
    return json.loads(output)


def find_run(runs: list[Run], root: Path, wanted: str = "") -> Run:
    """The run named wanted, or the newest run with rounds in this project."""
    if wanted:
        for run in runs:
            if run["id"] == wanted:
                return run
        raise SystemExit(f"there is no run {wanted}; hi agent best-of ls lists them")
    mine = [r for r in runs if r.get("loop") and Path(r["root"]).resolve() == root.resolve()]
    if not mine:
        raise SystemExit(f"no hi agent best-of run with rounds in {root}; make loop starts one")
    return max(mine, key=lambda r: r["created"])


def box_result(box: Run, round_: Run) -> str:
    if round_.get("winner") == box["name"]:
        return "kept" if round_["result"] == "kept" else "discarded"
    if box.get("outside"):
        return "disqualified"
    if box.get("score") is None:
        return "failed"
    if not box.get("files"):
        return "unchanged"
    return "not kept"


def attempts_of(run: Run) -> list[Run]:
    """The baseline, then every box of every finished round, in order."""
    loop = run["loop"]
    attempts = [
        {
            "attempt": 0,
            "round": 0,
            "name": "baseline",
            "agent": "",
            "model": "",
            "result": "baseline",
            "files": 0,
            "added": 0,
            "removed": 0,
            "idea": "your last commit when the run started",
            "rev": run["base"],
            "commit": run["base"],
            "cost_usd": 0.0,
            "input_tokens": 0,
            "output_tokens": 0,
            "agent_seconds": 0,
            "score_recorded": loop.get("baseline"),
        }
    ]
    for round_ in loop.get("history") or []:
        for box in sorted(round_.get("boxes") or [], key=lambda b: b["name"]):
            result = box_result(box, round_)
            tokens = box.get("tokens") or {}
            branch = box.get("branch", "")
            attempts.append(
                {
                    "attempt": len(attempts),
                    "round": round_["round"],
                    "name": box["name"],
                    "agent": box.get("agent", ""),
                    "model": box.get("model", ""),
                    "result": result,
                    "files": box.get("files", 0),
                    "added": box.get("added", 0),
                    "removed": box.get("removed", 0),
                    "idea": box.get("idea", ""),
                    # hi keeps each attempt under refs/<branch>; older runs
                    # have only the snapshot, which git may prune in time.
                    "rev": [f"refs/{branch}", box.get("snapshot", "")],
                    "commit": round_.get("commit", "") if result == "kept" else "",
                    "cost_usd": box.get("cost_usd", 0.0),
                    "input_tokens": tokens.get("input", 0),
                    "output_tokens": tokens.get("output", 0),
                    "agent_seconds": box.get("agent_seconds", 0),
                    "score_recorded": box.get("score"),
                }
            )
    return attempts


def git_source(rev: str | list[str]) -> str | None:
    for candidate in [rev] if isinstance(rev, str) else rev:
        if not candidate:
            continue
        shown = subprocess.run(
            ["git", "-C", str(HERE), "show", f"{candidate}:strategy.py"],
            capture_output=True,
            text=True,
        )
        if shown.returncode == 0:
            return shown.stdout
    return None


# The worker processes get the prices once, not with every attempt.
_PRICES: dict[str, pd.DataFrame | None] = {}


def _init(in_sample: pd.DataFrame, full: pd.DataFrame | None) -> None:
    _PRICES["in"], _PRICES["full"] = in_sample, full


def run_source(source: str) -> Run:
    """Score one strategy.py: in-sample, on the holdout, and its monthly path."""
    try:
        evaluate.check_source(source)
        module = types.ModuleType("attempt")
        exec(compile(source, "strategy.py", "exec"), module.__dict__)
        allocate = module.allocate
        in_sample = _PRICES["in"]
        assert in_sample is not None
        out: Run = {"in": evaluate.score(in_sample, allocate)}
        full = _PRICES["full"]
        path_prices = in_sample if full is None else full
        if full is not None:
            out["out"] = evaluate.score(full, allocate, holdout=True)
        returns, held = evaluate.backtest(path_prices, allocate)
        window = evaluate.scored(pd.PeriodIndex(returns.index))
        out["returns"] = returns.loc[window]
        out["held"] = held.loc[window]
        return out
    except Exception as error:  # any failure belongs to the attempt, not to make results
        return {"error": f"{type(error).__name__}: {error}"}


def benchmark_returns(prices: pd.DataFrame, months: pd.PeriodIndex) -> pd.Series:
    closes = prices.loc[evaluate.month_ends(prices), BENCHMARK]
    returns = closes.pct_change(fill_method=None)
    returns.index = pd.DatetimeIndex(returns.index).to_period("M")
    return returns.reindex(months)


def benchmark_metrics(returns: pd.Series) -> dict[str, float]:
    years = len(returns) / 12
    growth = (1 + returns).cumprod()
    cagr = float(growth.iloc[-1]) ** (1 / years) - 1
    volatility = float(returns.std()) * math.sqrt(12)
    return {
        "cagr_pct": cagr * 100,
        "ann_vol_pct": volatility * 100,
        "max_drawdown_pct": float((growth / growth.cummax() - 1).min()) * 100,
        "sharpe": (cagr - evaluate.RF) / volatility,
    }


def build(
    run: Run,
    in_sample: pd.DataFrame,
    full: pd.DataFrame | None,
    read_source: Callable[[str | list[str]], str | None] = git_source,
    workers: int | None = None,
) -> tuple[dict[str, pd.DataFrame], dict[str, str]]:
    """The tables, and each attempt's strategy.py by attempt name."""
    attempts = attempts_of(run)
    sources: dict[str, str] = {}
    by_hash: dict[str, str] = {}
    for attempt in attempts:
        source = read_source(attempt.pop("rev"))
        attempt["source_sha"] = ""
        if source is not None:
            sha = hashlib.sha256(source.encode()).hexdigest()[:12]
            sources[attempt["name"]], by_hash[sha], attempt["source_sha"] = source, source, sha

    # Attempts that left strategy.py as another left it share one backtest.
    hashes = list(by_hash)
    if workers == 1:
        _init(in_sample, full)
        outcomes = dict(zip(hashes, map(run_source, (by_hash[h] for h in hashes)), strict=True))
    else:
        with ProcessPoolExecutor(workers, initializer=_init, initargs=(in_sample, full)) as pool:
            done = pool.map(run_source, (by_hash[h] for h in hashes))
            outcomes = dict(zip(hashes, done, strict=True))

    version = 0
    rows, returns, weights = [], [], []
    for attempt in attempts:
        outcome = outcomes.get(attempt["source_sha"]) or {"error": "git has no files for it"}
        row = dict(attempt)
        row["error"] = outcome.get("error", "")
        is_version = attempt["result"] in ("baseline", "kept") and not row["error"]
        row["version"] = version if is_version else -1
        version += is_version
        for prefix, key in (("is", "in"), ("ho", "out")):
            metrics = outcome.get(key) or {}
            for metric in METRICS:
                row[f"{prefix}_{metric}"] = metrics.get(metric, math.nan)
        recorded, recomputed = row["score_recorded"], row[f"is_{SCORE_KEY}"]
        row["score_recorded"] = math.nan if recorded is None else float(recorded)
        row["matches_recorded"] = (
            recorded is None or not math.isfinite(recomputed) or abs(recorded - recomputed) < 5e-6
        )
        rows.append(row)
        if "returns" in outcome:
            series = outcome["returns"]
            returns.append(
                pd.DataFrame(
                    {
                        "attempt": attempt["attempt"],
                        "month": series.index.astype(str),
                        "ret": series,
                    }
                )
            )
            held = outcome["held"].stack()
            held = held[held > 1e-6]
            weights.append(
                pd.DataFrame(
                    {
                        "attempt": attempt["attempt"],
                        "month": held.index.get_level_values(0).astype(str),
                        "ticker": held.index.get_level_values(1).astype(str),
                        "weight": held.to_numpy(),
                    }
                )
            )

    attempts_table = pd.DataFrame(rows)
    months = pd.PeriodIndex(returns[0]["month"], freq="M") if returns else pd.PeriodIndex([], "M")
    prices = in_sample if full is None else full
    bench = benchmark_returns(prices, months)
    returns.append(pd.DataFrame({"attempt": -1, "month": months.astype(str), "ret": bench}))
    first_holdout = str(pd.Period(SPLIT, freq="M") + 1)
    bench_in = bench[bench.index < first_holdout]
    bench_out = bench[bench.index >= first_holdout]

    loop = run["loop"]
    history = loop.get("history") or []
    rounds = pd.DataFrame(
        [
            {
                "round": r["round"],
                "result": r["result"],
                "started": r.get("started", ""),
                "ended": r.get("ended", ""),
                "spend_usd": r.get("spend_usd", 0.0),
                "best_before": r.get("before", math.nan),
                "score": r.get("score", math.nan),
                "winner": r.get("winner", ""),
                "agent": r.get("agent", ""),
                "idea": r.get("idea", ""),
                "commit": r.get("commit", ""),
            }
            for r in history
        ],
        columns=ROUND_COLUMNS,
    )
    options = run.get("options") or {}
    run_row: Run = {
        "id": run["id"],
        "project": HERE.name,
        "task": run.get("prompt") or "",
        "task_file": run.get("task_file") or "",
        "score_command": run.get("score") or "",
        "score_key": SCORE_KEY,
        "higher": not loop.get("lower", False),
        "base": run["base"],
        "branch": loop.get("branch", ""),
        "best_commit": loop.get("best_commit", ""),
        "baseline": loop.get("baseline", math.nan),
        "best": loop.get("best", math.nan),
        "best_round": loop.get("best_round", 0),
        "rounds_done": len(history),
        "rounds_limit": loop.get("rounds", 0),
        "min_gain": loop.get("min_gain", 0.0),
        "edit": ",".join(loop.get("edit") or []),
        "state": loop.get("state", ""),
        "reason": loop.get("reason", ""),
        "spend_usd": loop.get("spend_usd", 0.0),
        "started": loop.get("started", ""),
        "ended": history[-1].get("ended", "") if history else "",
        "agents": ",".join(options.get("agents") or []),
        "models": ",".join(sorted({a["model"] for a in rows if a["model"]})),
        "tickers": ",".join(TICKERS),
        "split": SPLIT,
        "first_holdout_month": first_holdout,
        "fee": evaluate.FEE,
        "rf": evaluate.RF,
        "benchmark": BENCHMARK,
        "holdout_scored": full is not None,
        "made": datetime.now(UTC).isoformat(timespec="seconds"),
    }
    for prefix, series in (("bench_is", bench_in), ("bench_ho", bench_out)):
        if len(series) >= 12:
            for metric, value in benchmark_metrics(series).items():
                run_row[f"{prefix}_{metric}"] = value
    tables = {
        "run": pd.DataFrame([run_row]),
        "attempts": attempts_table,
        "rounds": rounds,
        "returns": pd.concat(returns, ignore_index=True),
        "weights": pd.concat(weights, ignore_index=True)
        if weights
        else pd.DataFrame(columns=["attempt", "month", "ticker", "weight"]),
    }
    return tables, sources


def render(tables: dict[str, bytes]) -> str:
    """report.html with the Parquet tables and the reader embedded, so it opens offline."""
    page = (REPORT / "report.html").read_text()
    reader = (REPORT / "hyparquet.min.js").read_text()
    embedded = json.dumps({name: base64.b64encode(data).decode() for name, data in tables.items()})
    return page.replace("/*__HYPARQUET__*/", reader).replace("/*__PARQUET__*/null", embedded)


def write(folder: Path, run: Run, tables: dict[str, pd.DataFrame], sources: dict[str, str]) -> None:
    folder.mkdir(parents=True, exist_ok=True)
    parquet: dict[str, bytes] = {}
    for name, table in tables.items():
        path = folder / f"{name}.parquet"
        table.to_parquet(path, index=False)
        parquet[name] = path.read_bytes()
    (folder / "strategies").mkdir(exist_ok=True)
    for name, source in sources.items():
        (folder / "strategies" / f"{name}.py").write_text(source)
    (folder / "run.json").write_text(json.dumps(run, indent=2) + "\n")
    (folder / "report.html").write_text(render(parquet))


def main() -> None:
    parser = argparse.ArgumentParser(description="Re-run every attempt of a run into results/.")
    parser.add_argument("--run", default=os.environ.get("HI_BEST_OF_RUN", ""), help="such as b-1")
    parser.add_argument("--holdout", default=str(HOLDOUT), help="the holdout prices, or none")
    arguments = parser.parse_args()
    if os.environ.get("HI_BOX"):
        raise SystemExit("make results runs on this machine: boxes never see the holdout")
    run_id = arguments.run or find_run(hi_json("ls"), HERE)["id"]
    run = hi_json("show", run_id)
    if not run.get("loop"):
        raise SystemExit(f"{run_id} has no rounds; make results is for runs with --score")
    try:
        in_sample = evaluate.load(str(EVAL))
        full = None
        if arguments.holdout != "none" and Path(arguments.holdout).exists():
            full = evaluate.load(arguments.holdout)
    except evaluate.EvaluationError as error:
        raise SystemExit(f"error: {error}") from None
    if full is None:
        print(f"No holdout at {arguments.holdout}: in-sample results only.", file=sys.stderr)
    tables, sources = build(run, in_sample, full)
    folder = HERE / "results" / run["id"]
    write(folder, run, tables, sources)
    attempts = tables["attempts"]
    failed = int((attempts["error"] != "").sum())
    differ = int((~attempts["matches_recorded"]).sum())
    print(f"{folder.relative_to(HERE)}/report.html: {len(attempts)} attempts")
    if failed:
        print(f"  {failed} could not be re-run; their error is in attempts.parquet")
    if differ:
        print(f"  {differ} scored differently than hi recorded: has evaluate.py or eval/ changed?")


if __name__ == "__main__":
    main()

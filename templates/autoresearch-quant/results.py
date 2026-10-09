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
  returns.parquet      each attempt's monthly returns, net and gross, with the
                       fees and borrow between them, and the benchmark's
                       (attempt -1), from the first scored month on
  weights.parquet      the weights each attempt held each month
  robust.parquet       make robust's checks, in-sample, for the baseline and
                       every kept version (JSON); sensitivity and the placebo
                       for the baseline and the best only
  regimes.parquet      each month's market regime (regimes.py)
  capacity.parquet     how much money the baseline and the best could run,
                       from daily volume (when make data saved it)
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
import time
import types
from collections.abc import Callable
from concurrent.futures import ProcessPoolExecutor, as_completed
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

import pandas as pd

import evaluate
import regimes
import robust
from prepare import EVAL, HOLDOUT, SPLIT, TICKERS, volume_path

HERE = Path(__file__).resolve().parent
REPORT = HERE / "report"
BENCHMARK = evaluate.BENCHMARK
SCORE_KEY = evaluate.SCORE  # what make score prints last
ROUND_COLUMNS = ["round", "result", "started", "ended", "spend_usd", "best_before", "score"]
ROUND_COLUMNS += ["winner", "agent", "idea", "commit"]
PARTICIPATION = 0.01  # the share of a day's dollar volume a position may take
CAPACITY_COLUMNS = ["ticker", "months_held", "avg_weight", "max_weight"]
CAPACITY_COLUMNS += ["median_dollar_volume", "capacity_usd", "worst_usd", "worst_month"]
METRICS = ["cagr_pct", "ann_vol_pct", "max_drawdown_pct", "turnover_per_year", *evaluate.SCORES]

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
            "billing": "",
            "metered": False,
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
                    "billing": box.get("billing", ""),
                    "metered": bool(box.get("metered", False)),
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


def monthly(path: evaluate.Path) -> pd.DataFrame:
    """A path's returns by calendar month, whatever it rebalances on."""
    frame = pd.DataFrame(
        {"net": path.net, "gross": path.gross, "borrow": path.borrow, "traded": path.traded}
    )
    months = pd.PeriodIndex(frame.index).asfreq("M", "E")
    grouped = frame.groupby(months)
    out = pd.DataFrame(
        {
            "net": grouped["net"].apply(lambda r: float((1 + r).prod() - 1)),
            "gross": grouped["gross"].apply(lambda r: float((1 + r).prod() - 1)),
            "borrow": grouped["borrow"].sum(),
            "traded": grouped["traded"].sum(),
        }
    )
    # What the fees took, so that net = gross - fees - borrow holds each month.
    out["fees"] = out["gross"] - out["net"] - out["borrow"]
    return out


def monthly_weights(held: pd.DataFrame) -> pd.DataFrame:
    """The weights held through each month: the average over its periods."""
    return held.groupby(pd.PeriodIndex(held.index).asfreq("M", "E")).mean()


def run_source(task: tuple[str, str]) -> Run:
    """Score one strategy.py: in-sample, on the holdout, its monthly path, and,
    for a version (checks "fast" or "slow"), make robust's checks in-sample."""
    source, checks = task
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
        path = evaluate.simulate(path_prices, allocate)
        window = evaluate.scored(pd.PeriodIndex(path.net.index))
        months = monthly(path).loc[lambda f: f.index >= window[0].asfreq("M", "E")]
        out["months"] = months
        out["held"] = monthly_weights(path.held.loc[window]).loc[months.index]
        if checks != "none":
            out["robust"] = robust.run(source, in_sample, slow=checks == "slow")
        return out
    except Exception as error:  # any failure belongs to the attempt, not to make results
        return {"error": f"{type(error).__name__}: {error}"}


def capacity(held: pd.DataFrame, prices: pd.DataFrame, volume: pd.DataFrame) -> pd.DataFrame:
    """Per ticker held: the fund size at which its position reaches PARTICIPATION
    of a day's dollar volume (the median of the 63 days before), over its last
    12 months held, and at its worst month (often when a fund was new)."""
    dollars = (prices * volume).rolling(63, min_periods=21).median()
    month_end = dollars.groupby(pd.DatetimeIndex(dollars.index).to_period("M")).last()
    adv = month_end.shift(1).reindex(held.index)  # known when the month's weights were set
    rows = []
    for ticker in held.columns:
        weights = held[ticker].abs()
        mask = weights > 1e-6
        if not mask.any() or ticker not in adv:
            continue
        limit = (PARTICIPATION * adv.loc[mask, ticker] / weights[mask]).dropna()
        if limit.empty:
            continue
        rows.append(
            {
                "ticker": ticker,
                "months_held": int(mask.sum()),
                "avg_weight": float(weights[mask].mean()),
                "max_weight": float(weights.max()),
                "median_dollar_volume": float(adv.loc[mask, ticker].median()),
                "capacity_usd": float(limit.iloc[-12:].min()),
                "worst_usd": float(limit.min()),
                "worst_month": str(limit.idxmin()),
            }
        )
    return pd.DataFrame(rows, columns=CAPACITY_COLUMNS)


def load_volume(prices_path: Path) -> pd.DataFrame | None:
    path = volume_path(prices_path)
    if not path.exists():
        return None
    return pd.read_csv(path, index_col=0, parse_dates=True).reindex(columns=TICKERS)


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
    volume: pd.DataFrame | None = None,
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

    # Versions get make robust's checks; the baseline and the best get them all.
    versions = [a for a in attempts if a["result"] in ("baseline", "kept") and a["source_sha"]]
    checks = {a["source_sha"]: "fast" for a in versions}
    ends = {versions[0]["source_sha"], versions[-1]["source_sha"]} if versions else set()
    checks |= dict.fromkeys(ends, "slow")

    # Attempts that left strategy.py as another left it share one backtest.
    hashes = list(by_hash)
    tasks = [(by_hash[h], checks.get(h, "none")) for h in hashes]
    checked = sum(task[1] != "none" for task in tasks)
    print(
        f"Re-running {len(tasks)} strategies, {checked} of them with make robust's checks…",
        file=sys.stderr,
    )
    outcomes: dict[str, Run] = {}
    started = time.monotonic()

    def done(sha: str, outcome: Run) -> None:
        outcomes[sha] = outcome
        if len(outcomes) % 10 == 0 or len(outcomes) == len(tasks):
            minutes = (time.monotonic() - started) / 60
            print(f"  {len(outcomes)} of {len(tasks)} done, {minutes:.1f} min", file=sys.stderr)

    if workers == 1:
        _init(in_sample, full)
        for sha, task in zip(hashes, tasks, strict=True):
            done(sha, run_source(task))
    else:
        with ProcessPoolExecutor(workers, initializer=_init, initargs=(in_sample, full)) as pool:
            futures = {
                pool.submit(run_source, task): sha for sha, task in zip(hashes, tasks, strict=True)
            }
            for future in as_completed(futures):
                done(futures[future], future.result())

    version = 0
    rows, returns, weights, checked, capacities = [], [], [], [], []
    prices = in_sample if full is None else full
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
        if is_version and "robust" in outcome:
            checked.append(
                {
                    "attempt": attempt["attempt"],
                    "slow": "placebo" in outcome["robust"],
                    "json": json.dumps(outcome["robust"], default=float),
                }
            )
            if volume is not None and attempt["source_sha"] in ends:
                table = capacity(outcome["held"], prices, volume)
                capacities.append(table.assign(attempt=attempt["attempt"]))
        if "months" in outcome:
            frame = outcome["months"]
            returns.append(
                pd.DataFrame(
                    {
                        "attempt": attempt["attempt"],
                        "month": frame.index.astype(str),
                        "ret": frame["net"],
                        "gross": frame["gross"],
                        "fees": frame["fees"],
                        "borrow": frame["borrow"],
                        "traded": frame["traded"],
                    }
                )
            )
            held = outcome["held"].stack()
            held = held[held.abs() > 1e-6]
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
    bench = benchmark_returns(prices, months)
    zero = pd.Series(0.0, index=bench.index)
    returns.append(
        pd.DataFrame(
            {
                "attempt": -1,
                "month": months.astype(str),
                "ret": bench,
                "gross": bench,
                "fees": zero,
                "borrow": zero,
                "traded": zero,
            }
        )
    )
    states = regimes.labels(prices, months, "M")
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
        "billed_usd": loop.get("billed_usd", 0.0),
        "billing": "; ".join(
            sorted({f"{a['agent']}: {a['billing']}" for a in rows if a["billing"]})
        ),
        "started": loop.get("started", ""),
        "ended": history[-1].get("ended", "") if history else "",
        "agents": ",".join(options.get("agents") or []),
        "models": ",".join(sorted({a["model"] for a in rows if a["model"]})),
        "tickers": ",".join(TICKERS),
        "split": SPLIT,
        "first_holdout_month": first_holdout,
        "fee": evaluate.FEE,
        "fees": json.dumps(evaluate.FEES),
        "rf": evaluate.RF,
        "rebalance": evaluate.REBALANCE,
        "offset": evaluate.OFFSET,
        "long_only": evaluate.LONG_ONLY,
        "max_gross": evaluate.MAX_GROSS,
        "max_net": evaluate.MAX_NET,
        "borrow": evaluate.BORROW,
        "warmup": evaluate.WARMUP,
        "participation": PARTICIPATION,
        "volume": volume is not None,
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
        "robust": pd.DataFrame(checked, columns=["attempt", "slow", "json"]),
        "regimes": states.reset_index(drop=True).assign(month=months.astype(str))[
            ["month", *regimes.MODELS]
        ],
        "capacity": pd.concat(capacities, ignore_index=True)
        if capacities
        else pd.DataFrame(columns=["attempt", *CAPACITY_COLUMNS]),
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
    volume = load_volume(Path(arguments.holdout) if full is not None else EVAL)
    tables, sources = build(run, in_sample, full, volume=volume)
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

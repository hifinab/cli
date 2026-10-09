"""How much of the score is luck: make robust, for the current strategy.py.

    make robust                 in-sample, in seconds
    python robust.py --json     the same numbers as JSON

A score from one backtest is one draw. These checks redraw it:

  start sets    a start at every month that leaves evaluate.MIN_YEARS:
                how often the strategy ends ahead of the benchmark
  tranches      the same strategy rebalancing a few trading days earlier;
                how far the Sharpe moves with the day alone
  end windows   every 1, 3, and 5-year stretch: how often it is ahead
  periods       equal blocks of the period: does it hold in each?
  costs         the Sharpe at 0, 1, 2, and 5 times the fees, and the
                multiple at which it no longer beats the benchmark
  sensitivity   each numeric constant in capitals in strategy.py moved to
                its neighbours; expect the median of the neighbourhood, not
                the chosen setting
  placebo       random but persistent portfolios with the strategy's number
                of holdings and invested share: the strategy's percentile
  noise         a --min-gain for hi agent best-of: a gain smaller than the
                tranches' spread is the rebalance day, not the idea

It runs only on the in-sample data. Don't run it on the holdout during a
run: make results does that once, at the end.
"""

import argparse
import ast
import json
import math
import re
import types
from typing import Any

import numpy as np
import pandas as pd

import evaluate
import regimes
from prepare import EVAL, TICKERS

COSTS = (0.0, 1.0, 2.0, 5.0)  # multiples of the fees
WINDOWS = (1, 3, 5)  # years
PLACEBOS = 200
PLACEBO_SEED = 0

Result = dict[str, Any]


def load_strategy(source: str) -> evaluate.Allocate:
    evaluate.check_source(source)
    module = types.ModuleType("strategy")
    exec(compile(source, "strategy.py", "exec"), module.__dict__)
    return module.allocate


def benchmark(prices: pd.DataFrame, index: pd.PeriodIndex) -> pd.Series:
    """The benchmark bought and held, on the strategy's periods."""
    days = evaluate.rebalance_days(prices, offset=0)
    returns = prices.loc[days, evaluate.BENCHMARK].pct_change(fill_method=None)
    returns.index = days.to_period(evaluate.REBALANCE)
    return returns.reindex(index).fillna(0.0)


def cagr(returns: pd.Series) -> float:
    per_year = evaluate.periods_per_year(pd.PeriodIndex(returns.index))
    return float(np.prod(1 + returns.to_numpy())) ** (per_year / len(returns)) - 1


def start_sets(window: pd.Series, bench: pd.Series) -> Result:
    index = pd.PeriodIndex(window.index)
    rows = []
    for i in evaluate.start_positions(index):
        rows.append(
            {
                "start": evaluate.label(index[i]),
                "sharpe": evaluate.sharpe(window.iloc[i:]),
                "excess_pct": (cagr(window.iloc[i:]) - cagr(bench.iloc[i:])) * 100,
            }
        )
    table = pd.DataFrame(rows, columns=["start", "sharpe", "excess_pct"])
    return {
        "count": len(table),
        "ahead_share": float((table["excess_pct"] > 0).mean()) if len(table) else math.nan,
        "median_sharpe": float(table["sharpe"].median()),
        "worst_sharpe": float(table["sharpe"].min()),
        "median_excess_pct": float(table["excess_pct"].median()),
        "rows": table.to_dict("records"),
    }


def tranches(prices: pd.DataFrame, allocate: evaluate.Allocate, window: pd.Index) -> Result:
    paths = evaluate.tranche_returns(prices, allocate).loc[window]
    sharpes = {int(offset): evaluate.sharpe(paths[offset]) for offset in paths.columns}
    values = list(sharpes.values())
    return {
        "sharpes": sharpes,
        "together": evaluate.sharpe(paths.mean(axis=1)),
        "spread": max(values) - min(values),
        "std": float(np.std(values)),
    }


def end_windows(window: pd.Series, bench: pd.Series) -> list[Result]:
    per_year = evaluate.periods_per_year(pd.PeriodIndex(window.index))
    rows = []
    for years in WINDOWS:
        n = years * per_year
        if len(window) < n:
            continue
        mine = (1 + window).rolling(n).apply(np.prod, raw=True).dropna()
        theirs = (1 + bench).rolling(n).apply(np.prod, raw=True).dropna()
        rows.append(
            {
                "years": years,
                "windows": len(mine),
                "ahead_share": float((mine > theirs).mean()),
                "worst_excess_pct": float(((mine / theirs) ** (1 / years) - 1).min() * 100),
            }
        )
    return rows


def periods(window: pd.Series) -> list[Result]:
    index = pd.PeriodIndex(window.index)
    bounds = np.linspace(0, len(window), evaluate.PERIODS + 1).round().astype(int)
    sharpes = evaluate.period_sharpes(window)
    return [
        {
            "from": evaluate.label(index[a]),
            "to": evaluate.label(index[b - 1]),
            "sharpe": value,
        }
        for a, b, value in zip(bounds[:-1], bounds[1:], sharpes, strict=True)
    ]


def costs(path: evaluate.Path, window: pd.Index, bench: pd.Series) -> Result:
    gross, fees, borrow = path.gross.loc[window], path.fees.loc[window], path.borrow.loc[window]

    def net(multiple: float) -> pd.Series:
        return gross - multiple * fees - borrow

    rows = [{"multiple": m, "sharpe": evaluate.sharpe(net(m))} for m in COSTS]
    target = cagr(bench)
    if cagr(net(0.0)) <= target:
        breakeven = 0.0
    elif cagr(net(100.0)) > target:
        breakeven = math.inf
    else:
        low, high = 0.0, 100.0
        for _ in range(50):
            middle = (low + high) / 2
            low, high = (middle, high) if cagr(net(middle)) > target else (low, middle)
        breakeven = low
    return {"rows": rows, "breakeven_multiple": breakeven, "fees_per_year_pct": _yearly(fees)}


def _yearly(series: pd.Series) -> float:
    per_year = evaluate.periods_per_year(pd.PeriodIndex(series.index))
    return float(series.sum()) / len(series) * per_year * 100


def constants(source: str) -> list[tuple[str, int | float, int]]:
    """Module-level NAME = number assignments: (name, value, line)."""
    found: list[tuple[str, int | float, int]] = []
    for node in ast.parse(source).body:
        if (
            isinstance(node, ast.Assign)
            and len(node.targets) == 1
            and isinstance(node.targets[0], ast.Name)
            and node.targets[0].id.isupper()
            and isinstance(node.value, ast.Constant)
            and type(value := node.value.value) in (int, float)
        ):
            assert isinstance(value, int | float)
            found.append((node.targets[0].id, value, node.lineno))
    return found


def neighbours(value: int | float) -> list[int | float]:
    if isinstance(value, int):
        values = [value + step for step in (-2, -1, 1, 2)]
        return [v for v in values if v > 0 or value <= 0]
    return [value * factor for factor in (0.5, 0.75, 1.25, 1.5)]


def with_constant(source: str, name: str, line: int, value: int | float) -> str:
    lines = source.splitlines(keepends=True)
    lines[line - 1] = re.sub(rf"^({name}\s*=\s*)[^#\n]+", rf"\g<1>{value!r} ", lines[line - 1])
    return "".join(lines)


def sensitivity(source: str, prices: pd.DataFrame, chosen: float) -> list[Result]:
    rows = []
    for name, value, line in constants(source):
        grid = [{"value": value, "sharpe": chosen, "chosen": True}]
        for other in neighbours(value):
            try:
                allocate = load_strategy(with_constant(source, name, line, other))
                result = evaluate.sharpe(_window(prices, allocate))
            except Exception:  # a neighbour that breaks the strategy has no score
                result = math.nan
            grid.append({"value": other, "sharpe": result, "chosen": False})
        grid.sort(key=lambda row: row["value"])
        others = [row["sharpe"] for row in grid if not row["chosen"]]
        finite = [v for v in others if math.isfinite(v)]
        values = [row["sharpe"] for row in grid if math.isfinite(row["sharpe"])]
        rows.append(
            {
                "name": name,
                "value": value,
                "grid": grid,
                "median": float(np.median(values)),
                "top": bool(finite) and chosen > max(finite) + 1e-6,  # a tie is a flat grid
            }
        )
    return rows


def _window(prices: pd.DataFrame, allocate: evaluate.Allocate) -> pd.Series:
    returns, _ = evaluate.backtest(prices, allocate)
    return returns.loc[evaluate.scored(pd.PeriodIndex(returns.index))]


def placebo(prices: pd.DataFrame, path: evaluate.Path, window: pd.Index, chosen: float) -> Result:
    """Random picks that change one name a year on average, held like the strategy."""
    held = path.held.loc[window]
    long = held.clip(lower=0)
    count = max(1, round(float((long > 1e-6).sum(axis=1).median())))
    invested = float(long.sum(axis=1).mean())
    days = evaluate.rebalance_days(prices)
    returns = prices.loc[days].pct_change(fill_method=None).fillna(0.0).to_numpy()
    priced = prices.loc[days].notna().to_numpy()
    fees = evaluate.fees().to_numpy()
    per_year = evaluate.PERIODS_PER_YEAR[evaluate.REBALANCE]
    labels = days.to_period(evaluate.REBALANCE)
    keep = labels.isin(window)
    generator = np.random.default_rng(PLACEBO_SEED)
    sharpes = []
    for _ in range(PLACEBOS):
        names: list[int] = []
        weights = np.zeros((len(days), len(TICKERS)))
        for t in range(len(days)):
            names = [n for n in names if priced[t, n]]
            available = [n for n in np.flatnonzero(priced[t]) if n not in names]
            if names and available and generator.random() < 1 / per_year:
                names[generator.integers(len(names))] = int(generator.choice(available))
                available = [n for n in np.flatnonzero(priced[t]) if n not in names]
            while len(names) < count and available:
                names.append(int(available.pop(int(generator.integers(len(available))))))
            weights[t, names] = invested / max(len(names), 1)
        held_placebo = np.vstack([np.zeros(len(TICKERS)), weights[:-1]])
        traded = np.abs(np.diff(held_placebo, axis=0, prepend=0.0))
        net = (held_placebo * returns).sum(axis=1) - (traded * fees).sum(axis=1)
        sharpes.append(evaluate.sharpe(pd.Series(net[keep], index=labels[keep])))
    values = np.array([v for v in sharpes if math.isfinite(v)])
    return {
        "count": len(values),
        "holdings": count,
        "invested": invested,
        "median_sharpe": float(np.median(values)),
        "p95_sharpe": float(np.percentile(values, 95)),
        "percentile": float((values < chosen).mean() * 100),
        "sharpes": [round(float(v), 4) for v in values],
    }


def run(source: str, prices: pd.DataFrame, slow: bool = True) -> Result:
    """Every check for one strategy.py; slow adds sensitivity and the placebo."""
    allocate = load_strategy(source)
    path = evaluate.simulate(prices, allocate)
    window_index = evaluate.scored(pd.PeriodIndex(path.net.index))
    window = path.net.loc[window_index]
    bench = benchmark(prices, window_index)
    chosen = evaluate.sharpe(window)
    result: Result = {
        "sharpe": chosen,
        "benchmark_sharpe": evaluate.sharpe(bench),
        "start_sets": start_sets(window, bench),
        "tranches": tranches(prices, allocate, window_index),
        "end_windows": end_windows(window, bench),
        "periods": periods(window),
        "costs": costs(path, window_index, bench),
        "regimes": regimes.by_state(window, bench, regimes.labels(prices, window_index)).to_dict(
            "records"
        ),
    }
    spread = result["tranches"]["std"]
    result["min_gain"] = max(0.01, math.ceil(spread * 100 - 1e-9) / 100)
    if slow:
        result["sensitivity"] = sensitivity(source, prices, chosen)
        result["placebo"] = placebo(prices, path, window_index, chosen)
    return result


def show(result: Result) -> None:
    pct = "{:.0%}".format
    print(f"sharpe {result['sharpe']:.3f}, {evaluate.BENCHMARK} {result['benchmark_sharpe']:.3f}")
    starts = result["start_sets"]
    print(
        f"start sets   {starts['count']} starts: {pct(starts['ahead_share'])} ahead of "
        f"{evaluate.BENCHMARK}; Sharpe median {starts['median_sharpe']:.3f}, "
        f"worst {starts['worst_sharpe']:.3f}"
    )
    tr = result["tranches"]
    days = ", ".join(f"{k}d {v:.3f}" for k, v in tr["sharpes"].items())
    print(f"tranches     {days}; together {tr['together']:.3f}; spread {tr['spread']:.3f}")
    for row in result["end_windows"]:
        print(
            f"end windows  {row['years']}y: ahead in {pct(row['ahead_share'])} of "
            f"{row['windows']}; worst {row['worst_excess_pct']:+.1f} pts a year"
        )
    blocks = ", ".join(f"{p['from']} to {p['to']} {p['sharpe']:.3f}" for p in result["periods"])
    print(f"periods      {blocks}")
    cost = result["costs"]
    multiples = ", ".join(f"{row['multiple']:g}x {row['sharpe']:.3f}" for row in cost["rows"])
    breakeven = cost["breakeven_multiple"]
    even = "never behind" if math.isinf(breakeven) else f"behind {evaluate.BENCHMARK} from "
    even += "" if math.isinf(breakeven) else f"{breakeven:.1f}x the fees"
    print(f"costs        {multiples}; {even}")
    for row in result.get("sensitivity", []):
        cells = [f"{r['value']:g} {r['sharpe']:.3f}" for r in row["grid"]]
        grid = ", ".join(
            f"[{c}]" if r["chosen"] else c for r, c in zip(row["grid"], cells, strict=True)
        )
        top = "  <- the chosen setting is the top of its grid" if row["top"] else ""
        print(f"sensitivity  {row['name']}: {grid}; median {row['median']:.3f}{top}")
    if "placebo" in result:
        p = result["placebo"]
        print(
            f"placebo      {p['count']} random portfolios of {p['holdings']}, "
            f"{p['invested']:.0%} invested: median {p['median_sharpe']:.3f}, 95th percentile "
            f"{p['p95_sharpe']:.3f}; the strategy beats {p['percentile']:.0f}%"
        )
    for model, names in regimes.MODELS.items():
        rows = [r for r in result["regimes"] if r["model"] == model]
        cells = ", ".join(
            f"{r['state']} {r['ann_return_pct']:+.1f}% vs {r['bench_ann_return_pct']:+.1f}%"
            for r in rows
        )
        print(f"regimes      {model}: {cells} a year ({len(names)} states)")
    print(
        f"noise        --min-gain {result['min_gain']:.2f}: the Sharpe moves this much with the "
        'rebalance day alone; or set SCORE = "tranches" in evaluate.py to average it out'
    )


def main() -> None:
    parser = argparse.ArgumentParser(description="How much of the score is luck.")
    parser.add_argument("--json", action="store_true", help="print JSON")
    arguments = parser.parse_args()
    try:
        with open("strategy.py") as file:
            source = file.read()
        result = run(source, evaluate.load(str(EVAL)))
    except evaluate.EvaluationError as error:
        raise SystemExit(f"error: {error}") from None
    if arguments.json:
        print(json.dumps(result, indent=2, default=float))
    else:
        show(result)


if __name__ == "__main__":
    main()

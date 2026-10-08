"""The fixed evaluation: a monthly backtest of strategy.py. Don't change it
during a run.

hi agent best-of scores every attempt with this file, and agents may change
only strategy.py, so the backtest, the data, and the metric stay the same.

    python evaluate.py                    the in-sample score, on eval/prices.csv
    python evaluate.py --holdout PATH     after prepare.SPLIT only, on data kept
                                          outside the repository (make holdout)

At each month's last trading day t, strategy.allocate() gets the daily
adjusted closes up to and including t, and returns the weights to hold until
the next month's last trading day. Long only, at most 100% invested; the rest
is cash at 0%. Every trade pays FEE of the value traded. The first WARMUP
months only build history and aren't scored.

The last line printed is the score: sharpe, with RF as the risk-free rate,
net of fees.
"""

import argparse
import importlib
import math
import re
import sys
from collections.abc import Callable, Mapping

import pandas as pd

from prepare import EVAL, SPLIT, TICKERS

FEE = 0.001  # of the value traded, each way
RF = 0.015
WARMUP = 12  # months of history before the first scored month

Allocate = Callable[[pd.DataFrame], Mapping[str, float] | pd.Series | None]

# allocate() gets its data from its argument and nothing else: no files (the
# data file holds the future of every month but the last), no network, and
# only these modules.
ALLOWED_IMPORTS = {
    "numpy",
    "pandas",
    "math",
    "statistics",
    "functools",
    "itertools",
    "collections",
    "dataclasses",
    "typing",
    "__future__",
}
FORBIDDEN = [
    (r"\bopen\s*\(", "open()"),
    (
        r"\b(read_csv|read_parquet|read_pickle|read_json|read_excel|read_table|read_sql|"
        r"read_hdf|read_feather|loadtxt|genfromtxt|fromfile|load)\s*\(",
        "reading files",
    ),
    (
        r"__import__|\bexec\s*\(|\beval\s*\(|\bcompile\s*\(|\bglobals\s*\(|\bvars\s*\(|"
        r"__builtins__|__loader__|__spec__",
        "dynamic code",
    ),
]


class EvaluationError(Exception):
    """A strategy or data problem; the score fails with its message."""


def check_source(source: str) -> None:
    """Refuse a strategy that could get data other than its argument."""
    pattern = r"^\s*(?:from\s+([\w.]+)\s+import|import\s+([\w., ]+))"
    for match in re.finditer(pattern, source, re.MULTILINE):
        if match.group(1):
            modules = [match.group(1)]
        else:
            modules = [part.strip().split(" ")[0] for part in match.group(2).split(",")]
        for module in modules:
            if module.split(".")[0] not in ALLOWED_IMPORTS:
                allowed = ", ".join(sorted(ALLOWED_IMPORTS - {"__future__"}))
                raise EvaluationError(f"strategy.py imports {module}; it may import only {allowed}")
    for forbidden, what in FORBIDDEN:
        if re.search(forbidden, source):
            raise EvaluationError(
                f"strategy.py may not use {what}: allocate() gets all its data from its argument"
            )


def load(path: str) -> pd.DataFrame:
    try:
        prices = pd.read_csv(path, index_col=0, parse_dates=True)
    except FileNotFoundError:
        raise EvaluationError(f"there is no {path}; make data downloads it, here") from None
    missing = [ticker for ticker in TICKERS if ticker not in prices.columns]
    if missing:
        raise EvaluationError(f"{path} has no prices for {', '.join(missing)}")
    return prices[TICKERS].sort_index()


def month_ends(prices: pd.DataFrame) -> pd.DatetimeIndex:
    """Each month's last trading day in the data."""
    days = pd.DatetimeIndex(prices.index)
    return days[~days.to_period("M").duplicated(keep="last")]


def clean_weights(raw: object, day: pd.Timestamp, prices_today: pd.Series) -> pd.Series:
    if raw is None:
        raw = {}
    if isinstance(raw, pd.Series):
        raw = raw.to_dict()
    if not isinstance(raw, Mapping):
        raise EvaluationError(
            f"allocate() on {day.date()} returned {type(raw).__name__}, not {{ticker: weight}}"
        )
    weights: dict[str, float] = {}
    for name, value in raw.items():
        ticker = str(name)
        if ticker not in TICKERS:
            tickers = " ".join(TICKERS)
            raise EvaluationError(
                f"allocate() on {day.date()} named {ticker}; the tickers are {tickers}"
            )
        try:
            weight = float(value)
        except TypeError, ValueError:
            weight = math.nan
        if not math.isfinite(weight):
            raise EvaluationError(
                f"allocate() on {day.date()} gave {ticker} a weight that isn't a number"
            )
        if weight < -1e-12:
            raise EvaluationError(
                f"allocate() on {day.date()} went short; the strategy is long only"
            )
        if weight > 0 and pd.isna(prices_today[ticker]):
            raise EvaluationError(
                f"allocate() on {day.date()} bought {ticker}, which has no price yet"
            )
        weights[ticker] = weights.get(ticker, 0.0) + max(weight, 0.0)
    if sum(weights.values()) > 1 + 1e-9:
        raise EvaluationError(
            f"allocate() on {day.date()} invested {sum(weights.values()):.4f}; at most 1 (100%)"
        )
    return pd.Series(weights, dtype=float).reindex(TICKERS, fill_value=0.0)


def backtest(prices: pd.DataFrame, allocate: Allocate) -> tuple[pd.Series, pd.DataFrame]:
    """Net monthly returns and the weights held, both indexed by month."""
    days = month_ends(prices)
    chosen: dict[pd.Timestamp, pd.Series] = {}
    for day, position in zip(days, prices.index.get_indexer(days), strict=True):
        history = prices.iloc[: position + 1].copy()
        chosen[day] = clean_weights(allocate(history), day, prices.iloc[position])
    weights = pd.DataFrame(chosen).T
    returns = prices.loc[days].pct_change(fill_method=None).fillna(0.0)
    held = weights.shift(1).fillna(0.0)  # chosen at t, held from t to t+1
    gross = (held * returns).sum(axis=1)
    traded = held.diff().abs().sum(axis=1)
    traded.iloc[0] = held.iloc[0].abs().sum()
    net = gross - traded * FEE
    months = days.to_period("M")
    net.index, held.index = months, months
    return net, held


def metrics(returns: pd.Series, held: pd.DataFrame) -> dict[str, float]:
    years = len(returns) / 12
    growth = (1 + returns).cumprod()
    cagr = float(growth.iloc[-1]) ** (1 / years) - 1
    volatility = float(returns.std()) * math.sqrt(12)
    drawdown = float((growth / growth.cummax() - 1).min())
    turnover = float(held.diff().abs().sum(axis=1).sum()) / 2 / years
    if not volatility > 0:
        raise EvaluationError("the strategy took no risk in the period, so it has no Sharpe ratio")
    return {
        "months": len(returns),
        "cagr_pct": cagr * 100,
        "ann_vol_pct": volatility * 100,
        "max_drawdown_pct": drawdown * 100,
        "turnover_per_year": turnover,
        "sharpe": (cagr - RF) / volatility,
    }


def score(prices: pd.DataFrame, allocate: Allocate, holdout: bool = False) -> dict[str, object]:
    returns, held = backtest(prices, allocate)
    first_holdout_month = pd.Period(SPLIT, freq="M") + 1
    window = returns.loc[first_holdout_month:] if holdout else returns.iloc[WARMUP + 1 :]
    if len(window) < 12:
        raise EvaluationError(f"only {len(window)} months to score; a score needs 12 or more")
    result: dict[str, object] = dict(metrics(window, held.loc[window.index]))
    result["period"] = f"{window.index[0]} to {window.index[-1]}"
    return result


def main() -> None:
    parser = argparse.ArgumentParser(description="Score strategy.py.")
    parser.add_argument("--holdout", metavar="PATH", help=f"score after {SPLIT}, from this file")
    arguments = parser.parse_args()
    try:
        with open("strategy.py") as file:
            check_source(file.read())
        strategy = importlib.import_module("strategy")
        prices = load(arguments.holdout or str(EVAL))
        result = score(prices, strategy.allocate, holdout=bool(arguments.holdout))
    except EvaluationError as error:
        print(f"error: {error}")
        sys.exit(1)
    kind = ", holdout" if arguments.holdout else ""
    print(f"period:            {result['period']} ({result['months']} months{kind})")
    for key in ("cagr_pct", "ann_vol_pct", "max_drawdown_pct", "turnover_per_year"):
        print(f"{key + ':':<19}{result[key]:.2f}")
    print(f"sharpe:            {result['sharpe']:.6f}")


if __name__ == "__main__":
    main()

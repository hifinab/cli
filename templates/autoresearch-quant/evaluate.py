"""The fixed evaluation: a monthly backtest of strategy.py. Don't change it
during a run.

hi agent best-of scores every attempt with this file, and agents may change
only strategy.py, so the backtest, the data, and the metric stay the same.

    python evaluate.py                    the in-sample score, on eval/prices.csv
    python evaluate.py --holdout PATH     after prepare.SPLIT only, on data kept
                                          outside the repository (make holdout)

At each rebalance day t (by default each month's last trading day),
strategy.allocate() gets the daily adjusted closes up to and including t,
and returns the weights to hold until the next rebalance day. By default
long only, at most 100% invested; the rest is cash at 0%. Every trade pays
its ticker's fee (FEES, or FEE) of the value traded; shorts, when allowed,
pay BORROW a year. The first WARMUP months only build history and aren't
scored.

The last line printed is the score, SCORE, net of fees, with RF as the
risk-free rate. Each is a Sharpe ratio, and every score prints all of them:

    sharpe        the whole in-sample period, from one start
    worst_period  the lowest of the PERIODS equal blocks of the period
    median_start  the median over start sets: a start at every month that
                  leaves MIN_YEARS or more
    tranches      the strategy held as len(TRANCHES) tranches, each
                  rebalancing that many trading days before the period's end

sharpe rewards fitting one stretch of history; the other three must hold
across stretches, starts, or rebalance days. make robust shows more.
"""

import argparse
import importlib
import math
import re
import sys
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from itertools import pairwise

import numpy as np
import pandas as pd

from prepare import EVAL, SPLIT, TICKERS

FEE = 0.001  # of the value traded, each way
FEES: dict[str, float] = {}  # a fee per ticker, where it isn't FEE
RF = 0.015
WARMUP = 12  # months of history before the first scored month
BENCHMARK = "SPY"  # bought and held, without fees; one of prepare.TICKERS

REBALANCE = "M"  # M: each month's last trading day; W: each week's; D: every day
OFFSET = 0  # rebalance this many trading days before the period's last one
LONG_ONLY = True
MAX_GROSS = 1.0  # the sum of the weights' sizes, longs and shorts
MAX_NET = 1.0  # the sum of the weights, shorts counted negative
BORROW = 0.0  # a year, of the value shorted
PERIODS_PER_YEAR = {"M": 12, "W": 52, "D": 252}

SCORE = "sharpe"  # or worst_period, median_start, tranches: what make score prints last
SCORES = ["sharpe", "worst_period", "median_start", "tranches"]
PERIODS = 3  # equal blocks for worst_period
MIN_YEARS = 3  # the shortest start set
TRANCHES = (0, 5, 10, 15)  # trading days before each period's end

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
    return rebalance_days(prices, "M", 0)


def rebalance_days(
    prices: pd.DataFrame, rebalance: str | None = None, offset: int | None = None
) -> pd.DatetimeIndex:
    """Each period's last trading day, or offset trading days before it (never
    before the period's first)."""
    rebalance = REBALANCE if rebalance is None else rebalance
    offset = OFFSET if offset is None else offset
    if rebalance not in PERIODS_PER_YEAR:
        raise EvaluationError(f"REBALANCE is {rebalance}; it is M, W, or D")
    days = pd.DatetimeIndex(prices.index)
    periods = days.to_period(rebalance)
    last = np.flatnonzero(~periods.duplicated(keep="last"))
    first = np.flatnonzero(~periods.duplicated(keep="first"))
    return days[np.maximum(last - offset, first)]


def fees() -> pd.Series:
    return pd.Series({ticker: FEES.get(ticker, FEE) for ticker in TICKERS}, dtype=float)


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
        if LONG_ONLY and weight < -1e-12:
            raise EvaluationError(
                f"allocate() on {day.date()} went short; the strategy is long only"
            )
        if abs(weight) > 1e-12 and pd.isna(prices_today[ticker]):
            raise EvaluationError(
                f"allocate() on {day.date()} traded {ticker}, which has no price yet"
            )
        weights[ticker] = weights.get(ticker, 0.0) + (max(weight, 0.0) if LONG_ONLY else weight)
    gross = sum(abs(weight) for weight in weights.values())
    if gross > MAX_GROSS + 1e-9:
        raise EvaluationError(
            f"allocate() on {day.date()} invested {gross:.4f}; at most {MAX_GROSS:g} "
            f"({MAX_GROSS:.0%}), longs and shorts together"
        )
    net = sum(weights.values())
    if net > MAX_NET + 1e-9:
        raise EvaluationError(
            f"allocate() on {day.date()} was {net:.4f} net long; at most {MAX_NET:g}"
        )
    return pd.Series(weights, dtype=float).reindex(TICKERS, fill_value=0.0)


@dataclass
class Path:
    """A backtest, one row per period, labelled by the period its holding ends in."""

    net: pd.Series  # the return after fees and borrow
    gross: pd.Series  # the return before them
    fees: pd.Series  # paid, as a share of the portfolio
    borrow: pd.Series
    traded: pd.Series  # the value traded, as a share of the portfolio
    held: pd.DataFrame  # the weights held through the period


def simulate(
    prices: pd.DataFrame,
    allocate: Allocate,
    rebalance: str | None = None,
    offset: int | None = None,
) -> Path:
    rebalance = REBALANCE if rebalance is None else rebalance
    days = rebalance_days(prices, rebalance, offset)
    chosen: dict[pd.Timestamp, pd.Series] = {}
    for day, position in zip(days, prices.index.get_indexer(days), strict=True):
        history = prices.iloc[: position + 1].copy()
        chosen[day] = clean_weights(allocate(history), day, prices.iloc[position])
    weights = pd.DataFrame(chosen).T
    returns = prices.loc[days].pct_change(fill_method=None).fillna(0.0)
    held = weights.shift(1).fillna(0.0)  # chosen at t, held from t to t+1
    gross = (held * returns).sum(axis=1)
    trades = held.diff().abs()
    trades.iloc[0] = held.iloc[0].abs()
    paid = (trades * fees()).sum(axis=1)
    borrow = held.clip(upper=0).abs().sum(axis=1) * BORROW / PERIODS_PER_YEAR[rebalance]
    labels = days.to_period(rebalance)
    path = Path(gross - paid - borrow, gross, paid, borrow, trades.sum(axis=1), held)
    for series in (path.net, path.gross, path.fees, path.borrow, path.traded, path.held):
        series.index = labels
    return path


def backtest(prices: pd.DataFrame, allocate: Allocate) -> tuple[pd.Series, pd.DataFrame]:
    """Net returns and the weights held, both indexed by period."""
    path = simulate(prices, allocate)
    return path.net, path.held


def periods_per_year(index: pd.PeriodIndex) -> int:
    return PERIODS_PER_YEAR[str(index.freqstr)[0]]


def scored(index: pd.PeriodIndex) -> pd.PeriodIndex:
    """The periods after the warmup."""
    first = index[0].asfreq("M", "E") + WARMUP + 1
    return index[index.asfreq("M", "E") >= first]


def holdout_periods(index: pd.PeriodIndex) -> pd.PeriodIndex:
    """The periods that end after prepare.SPLIT."""
    return index[index.end_time > pd.Timestamp(SPLIT) + pd.Timedelta(days=1)]


def label(period: pd.Period) -> str:
    """A month as 2008-07; a week or a day by its last day."""
    return str(period) if period.freqstr.startswith("M") else str(period.end_time.date())


def metrics(returns: pd.Series, held: pd.DataFrame) -> dict[str, float]:
    per_year = periods_per_year(pd.PeriodIndex(returns.index))
    years = len(returns) / per_year
    growth = (1 + returns).cumprod()
    cagr = float(growth.iloc[-1]) ** (1 / years) - 1
    volatility = float(returns.std()) * math.sqrt(per_year)
    drawdown = float((growth / growth.cummax() - 1).min())
    turnover = float(held.diff().abs().sum(axis=1).sum()) / 2 / years
    if not volatility > 0:
        raise EvaluationError("the strategy took no risk in the period, so it has no Sharpe ratio")
    return {
        "months": round(years * 12),
        "cagr_pct": cagr * 100,
        "ann_vol_pct": volatility * 100,
        "max_drawdown_pct": drawdown * 100,
        "turnover_per_year": turnover,
        "sharpe": (cagr - RF) / volatility,
    }


def sharpe(returns: pd.Series) -> float:
    """The Sharpe ratio, or NaN for a stretch without risk."""
    per_year = periods_per_year(pd.PeriodIndex(returns.index))
    volatility = float(returns.std()) * math.sqrt(per_year)
    if len(returns) < 2 or not volatility > 0:
        return math.nan
    cagr = float(np.prod(1 + returns.to_numpy())) ** (per_year / len(returns)) - 1
    return (cagr - RF) / volatility


def period_sharpes(window: pd.Series, periods: int = PERIODS) -> list[float]:
    """The Sharpe of each of periods equal blocks of the window."""
    bounds = np.linspace(0, len(window), periods + 1).round().astype(int)
    return [sharpe(window.iloc[a:b]) for a, b in pairwise(bounds)]


def start_positions(index: pd.PeriodIndex, min_years: float = MIN_YEARS) -> list[int]:
    """The first period of each month that leaves min_years or more to the end."""
    months = index.asfreq("M", "E")
    firsts = np.flatnonzero(~months.duplicated(keep="first"))
    left = len(index) - firsts
    return [int(i) for i in firsts[left >= min_years * periods_per_year(index)]]


def start_sharpes(window: pd.Series, min_years: float = MIN_YEARS) -> pd.Series:
    """The Sharpe from each start set to the window's end, by start."""
    index = pd.PeriodIndex(window.index)
    starts = start_positions(index, min_years)
    return pd.Series([sharpe(window.iloc[i:]) for i in starts], index=index[starts], dtype=float)


def tranche_returns(prices: pd.DataFrame, allocate: Allocate) -> pd.DataFrame:
    """Net returns of each tranche, one column per offset; a daily strategy has one."""
    offsets = (0,) if REBALANCE == "D" else TRANCHES
    paths = {offset: simulate(prices, allocate, offset=offset).net for offset in offsets}
    return pd.DataFrame(paths)


def score(prices: pd.DataFrame, allocate: Allocate, holdout: bool = False) -> dict[str, object]:
    tranches = tranche_returns(prices, allocate)
    path = simulate(prices, allocate)
    returns, held = path.net, path.held
    index = pd.PeriodIndex(returns.index)
    window = returns.loc[holdout_periods(index) if holdout else scored(index)]
    if len(window) < periods_per_year(index):
        raise EvaluationError(f"only {len(window)} periods to score; a score needs a year or more")
    result: dict[str, object] = dict(metrics(window, held.loc[window.index]))
    result["period"] = f"{label(window.index[0])} to {label(window.index[-1])}"
    starts = start_sharpes(window).dropna()
    result["worst_period"] = min(period_sharpes(window))
    result["median_start"] = float(starts.median()) if len(starts) else result["sharpe"]
    result["tranches"] = sharpe(tranches.loc[window.index].mean(axis=1))
    for key in SCORES:
        if not math.isfinite(float(result[key])):  # type: ignore[arg-type]
            raise EvaluationError(f"{key} has no value: a stretch of the period took no risk")
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
    for key in [key for key in SCORES if key != SCORE] + [SCORE]:
        print(f"{key + ':':<19}{result[key]:.6f}")


if __name__ == "__main__":
    main()

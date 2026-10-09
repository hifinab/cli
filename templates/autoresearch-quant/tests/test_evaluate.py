"""The evaluation is what every attempt is judged by, so it is tested
against an independent implementation, for lookahead, and for fees."""

import pandas as pd
import pytest

import evaluate
import strategy
from evaluate import EvaluationError, backtest, check_source, score
from prepare import TICKERS
from tests.conftest import synthetic_prices


def reference_haa(prices: pd.DataFrame) -> pd.Series:
    """HAA written as one vectorized pass, the way the original script did."""
    monthly = prices.resample("ME").last()
    change = monthly.pct_change
    momentum = (change(1) + change(3) + change(6) + change(12)) / 4
    weights = pd.DataFrame(0.0, index=monthly.index, columns=TICKERS)
    for i in range(12, len(monthly)):
        row, date = momentum.iloc[i], monthly.index[i]
        best = row[["BIL", "IEF"]].idxmax()
        if pd.isna(row["TIP"]) or row["TIP"] <= 0:
            weights.loc[date, best] = 1.0
            continue
        ranked = row[strategy.OFFENSIVE].sort_values(ascending=False)
        for asset in ranked.head(4).index:
            weights.loc[date, asset if ranked[asset] > 0 else best] += 0.25
    held = weights.shift(1).fillna(0.0)
    returns = monthly.pct_change(fill_method=None).fillna(0.0)
    net = (held * returns).sum(axis=1) - held.diff().abs().sum(axis=1).fillna(0) * evaluate.FEE
    net.index = pd.DatetimeIndex(net.index).to_period("M")
    return net


def test_backtest_matches_an_independent_implementation(prices: pd.DataFrame) -> None:
    net, _ = backtest(prices, strategy.allocate)
    reference = reference_haa(prices)
    window = net.index[evaluate.WARMUP + 1 :]
    assert (net.loc[window] - reference.loc[window]).abs().max() < 1e-12


def test_allocate_sees_only_the_past(prices: pd.DataFrame) -> None:
    seen: list[tuple[pd.Timestamp, pd.Timestamp]] = []
    month_ends = evaluate.month_ends(prices)

    def watching(history: pd.DataFrame) -> dict[str, float]:
        seen.append((history.index[-1], history.index.max()))
        return {}

    backtest(prices, watching)
    assert [last for last, _ in seen] == list(month_ends)
    assert all(last == latest for last, latest in seen)


def test_weights_earn_the_next_months_return() -> None:
    prices = synthetic_prices()
    month_ends = evaluate.month_ends(prices)
    # SPY doubles over one month; only weights chosen before that month earn it.
    jump_start, jump_end = month_ends[20], month_ends[21]
    prices.loc[prices.index > jump_start, "SPY"] *= 2

    def spy_at(day: pd.Timestamp):
        return lambda history: {"SPY": 1.0} if history.index[-1] == day else {}

    before, _ = backtest(prices, spy_at(jump_start))
    after, _ = backtest(prices, spy_at(jump_end))
    # Month 21's return is the jump.
    assert before.iloc[21] > 0.9
    assert after.iloc[21] == 0.0  # it held cash through the jump


def test_every_trade_pays_the_fee(prices: pd.DataFrame) -> None:
    def alternate(history: pd.DataFrame) -> dict[str, float]:
        return {"SPY": 1.0} if history.index[-1].month % 2 else {"TLT": 1.0}

    net, held = backtest(prices, alternate)
    days = evaluate.month_ends(prices)
    returns = prices.loc[days].pct_change(fill_method=None)
    returns.index = days.to_period("M")
    gross = (held * returns.fillna(0)).sum(axis=1)
    # Selling one fund and buying the other trades twice the portfolio.
    assert ((gross - net).iloc[3:] - 2 * evaluate.FEE).abs().max() < 1e-12


def test_the_holdout_scores_only_after_the_split() -> None:
    prices = synthetic_prices(end="2024-12-31")
    result = score(prices, strategy.allocate, holdout=True)
    assert result["period"] == "2023-01 to 2024-12"
    assert result["months"] == 24


def test_the_in_sample_score_skips_the_warmup(prices: pd.DataFrame) -> None:
    result = score(prices, strategy.allocate)
    assert str(result["period"]).startswith("2008-07")


@pytest.mark.parametrize(
    ("weights", "message"),
    [
        ({"SPY": 1.5}, "at most 1"),
        ({"SPY": -0.5, "TLT": 1.0}, "long only"),
        ({"AAPL": 1.0}, "AAPL"),
        ({"SPY": float("nan")}, "isn't a number"),
        ({"SPY": "lots"}, "isn't a number"),
        ({"VEA": 1.0}, "no price yet"),
        ("SPY", "not {ticker: weight}"),
    ],
)
def test_bad_weights_fail_the_score(prices: pd.DataFrame, weights: object, message: str) -> None:
    with pytest.raises(EvaluationError, match=message):
        backtest(prices, lambda history: weights)  # type: ignore[arg-type]


@pytest.mark.parametrize(
    "source",
    [
        "import pandas as pd\ndef allocate(p):\n    return pd.read_csv('eval/prices.csv')\n",
        "import os\n",
        "from pathlib import Path\n",
        "import numpy as np, requests\n",
        "x = open('eval/prices.csv')\n",
        "import numpy as np\nnp.load('x.npy')\n",
        "__import__('os')\n",
        "exec('import os')\n",
    ],
)
def test_strategies_that_could_read_other_data_are_refused(source: str) -> None:
    with pytest.raises(EvaluationError):
        check_source(source)


def test_the_example_strategy_is_allowed() -> None:
    with open("strategy.py") as file:
        check_source(file.read())


def reference_path(
    prices: pd.DataFrame, weights_of, freq: str, fees: dict[str, float], borrow: float
) -> pd.Series:
    """Net returns for weights given by date, written without evaluate's helpers."""
    frame = prices.copy()
    frame["period"] = pd.DatetimeIndex(frame.index).to_period(freq)
    days = pd.DatetimeIndex(frame.groupby("period").tail(1).index)
    rows = [pd.Series(weights_of(day), index=TICKERS, dtype=float).fillna(0.0) for day in days]
    weights = pd.DataFrame(rows, index=days)
    held = weights.shift(1).fillna(0.0)
    returns = prices.loc[days].pct_change(fill_method=None).fillna(0.0)
    fee = pd.Series([fees.get(t, evaluate.FEE) for t in TICKERS], index=TICKERS)
    traded = held.diff().fillna(held).abs()
    per_year = {"M": 12, "W": 52}[freq]
    short = (-held).clip(lower=0).sum(axis=1)
    net = (held * returns).sum(axis=1) - (traded * fee).sum(axis=1) - short * borrow / per_year
    net.index = days.to_period(freq)
    return net


@pytest.mark.parametrize("freq", ["M", "W"])
def test_shorts_borrow_and_fees_per_ticker(monkeypatch, prices: pd.DataFrame, freq: str) -> None:
    fees = {"SPY": 0.0005, "TLT": 0.003}
    monkeypatch.setattr(evaluate, "LONG_ONLY", False)
    monkeypatch.setattr(evaluate, "MAX_GROSS", 2.0)
    monkeypatch.setattr(evaluate, "FEES", fees)
    monkeypatch.setattr(evaluate, "BORROW", 0.02)
    monkeypatch.setattr(evaluate, "REBALANCE", freq)

    def long_short(day: pd.Timestamp) -> dict[str, float]:
        return {"SPY": 1.0, "TLT": -0.5} if day.month % 3 else {"IEF": 0.8, "SPY": -0.4}

    path = evaluate.simulate(prices, lambda history: long_short(history.index[-1]))
    reference = reference_path(prices, long_short, freq, fees, 0.02)
    assert len(path.net) == len(reference)
    assert (path.net - reference).abs().max() < 1e-12
    assert (path.gross - path.fees - path.borrow - path.net).abs().max() < 1e-15
    assert path.borrow.iloc[5] > 0


def test_weekly_scores_annualize_by_weeks(monkeypatch, prices: pd.DataFrame) -> None:
    monkeypatch.setattr(evaluate, "REBALANCE", "W")
    result = score(prices, strategy.allocate)
    assert str(result["period"]).startswith("2008-07-06")
    assert 170 <= result["months"] <= 175  # type: ignore[operator]


def test_offset_rebalances_earlier(prices: pd.DataFrame) -> None:
    ends = evaluate.rebalance_days(prices, "M", 0)
    early = evaluate.rebalance_days(prices, "M", 5)
    days = pd.DatetimeIndex(prices.index)
    gaps = days.get_indexer(ends) - days.get_indexer(early)
    assert (gaps == 5).all()
    assert (early.to_period("M") == ends.to_period("M")).all()


@pytest.mark.parametrize(
    ("weights", "message"),
    [({"SPY": 1.5, "TLT": -1.0}, "at most 2"), ({"SPY": 1.5}, "net long; at most 1")],
)
def test_exposure_limits(monkeypatch, prices, weights, message) -> None:
    monkeypatch.setattr(evaluate, "LONG_ONLY", False)
    monkeypatch.setattr(evaluate, "MAX_GROSS", 2.0)
    with pytest.raises(EvaluationError, match=message):
        backtest(prices, lambda history: weights)

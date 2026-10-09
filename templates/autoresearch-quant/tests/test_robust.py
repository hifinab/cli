"""make robust redraws the score; these check each redraw on known cases."""

import math
from pathlib import Path

import pandas as pd
import pytest

import evaluate
import robust
from evaluate import score

BASELINE = Path("strategy.py").read_text()


@pytest.fixture(scope="module")
def result() -> robust.Result:
    from tests.conftest import synthetic_prices

    return robust.run(BASELINE, synthetic_prices())


def test_every_score_is_printed_and_the_chosen_one_last(prices, capsys, monkeypatch) -> None:
    result = score(prices, robust.load_strategy(BASELINE))
    for key in evaluate.SCORES:
        assert math.isfinite(float(result[key]))  # type: ignore[arg-type]
    # Tranche 0 is the strategy itself; together they differ from it.
    assert result["tranches"] != result["sharpe"]
    assert float(result["worst_period"]) <= float(result["sharpe"]) + 1  # type: ignore[arg-type]
    monkeypatch.setattr(evaluate, "SCORE", "worst_period")
    monkeypatch.setattr("sys.argv", ["evaluate.py"])
    monkeypatch.setattr(evaluate, "load", lambda path: prices)
    evaluate.main()
    last = capsys.readouterr().out.strip().splitlines()[-1]
    assert last.startswith("worst_period:")
    assert abs(float(last.split()[-1]) - float(result["worst_period"])) < 1e-6  # type: ignore[arg-type]


def test_period_sharpes_split_the_window_evenly(prices) -> None:
    returns, _ = evaluate.backtest(prices, robust.load_strategy(BASELINE))
    window = returns.loc[evaluate.scored(pd.PeriodIndex(returns.index))]
    blocks = robust.periods(window)
    assert len(blocks) == evaluate.PERIODS
    assert blocks[0]["from"] == "2008-07" and blocks[-1]["to"] == "2022-12"
    assert [b["sharpe"] for b in blocks] == evaluate.period_sharpes(window)


def test_start_sets_leave_min_years(result) -> None:
    starts = result["start_sets"]
    # 174 scored months, each start leaving 36 or more: 139 starts.
    assert starts["count"] == 174 - 36 + 1
    assert starts["rows"][0]["start"] == "2008-07"
    assert starts["rows"][-1]["start"] == "2020-01"
    assert starts["worst_sharpe"] <= starts["median_sharpe"]


def test_tranches_and_noise(result) -> None:
    tranches = result["tranches"]
    assert list(tranches["sharpes"]) == list(evaluate.TRANCHES)
    assert tranches["sharpes"][0] == pytest.approx(result["sharpe"])
    assert result["min_gain"] >= 0.01
    assert result["min_gain"] >= tranches["std"] - 1e-9


def test_costs_rise_with_the_multiple(result) -> None:
    sharpes = [row["sharpe"] for row in result["costs"]["rows"]]
    assert sharpes == sorted(sharpes, reverse=True)
    assert result["costs"]["rows"][1]["sharpe"] == pytest.approx(result["sharpe"])


def test_end_windows(result) -> None:
    assert [row["years"] for row in result["end_windows"]] == [1, 3, 5]
    assert result["end_windows"][0]["windows"] == 174 - 12 + 1


def test_sensitivity_moves_each_constant(result) -> None:
    (top,) = result["sensitivity"]
    assert top["name"] == "TOP"
    assert [row["value"] for row in top["grid"]] == [2, 3, 4, 5, 6]
    assert [row["chosen"] for row in top["grid"]] == [False, False, True, False, False]


def test_with_constant_rewrites_one_line() -> None:
    source = "TOP = 4  # assets\nLOOKBACK = 0.5\nother = 3\n"
    assert robust.constants(source) == [("TOP", 4, 1), ("LOOKBACK", 0.5, 2)]
    assert robust.with_constant(source, "TOP", 1, 3).startswith("TOP = 3 # assets\n")
    assert "LOOKBACK = 0.75" in robust.with_constant(source, "LOOKBACK", 2, 0.75)
    assert robust.neighbours(1) == [2, 3]
    assert robust.neighbours(2.0) == [1.0, 1.5, 2.5, 3.0]


def test_placebo_holds_like_the_strategy(result) -> None:
    placebo = result["placebo"]
    assert placebo["count"] == robust.PLACEBOS
    assert placebo["holdings"] == 4
    assert 0 <= placebo["percentile"] <= 100


def test_the_placebo_is_luck(prices, monkeypatch) -> None:
    # A strategy that is itself random picks lands inside the placebo's range.
    monkeypatch.setattr(robust, "PLACEBOS", 50)
    random = "def allocate(prices):\n    n = len(prices) % 7\n"
    random += "    names = ['SPY', 'IWM', 'TLT', 'DBC', 'BIL', 'IEF', 'VNQ']\n"
    random += "    return {names[n]: 0.5, names[(n + 3) % 7]: 0.5}\n"
    result = robust.run(random, prices)
    assert 2 <= result["placebo"]["percentile"] <= 98

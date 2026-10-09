import pandas as pd

import evaluate
import regimes


def test_labels_are_known_before_the_month(prices) -> None:
    months = evaluate.month_ends(prices).to_period("M")
    labels = regimes.labels(prices, months, "M")
    daily = regimes.daily_states(prices[evaluate.BENCHMARK])
    ends = evaluate.month_ends(prices)
    # Each month carries the state at the previous month's last trading day.
    for i in (30, 60, 120):
        assert labels["vol2"].iloc[i] == daily["vol2"].loc[ends[i - 1]]
    assert labels.iloc[0].isna().all()


def test_the_future_does_not_change_the_past(prices) -> None:
    months = evaluate.month_ends(prices).to_period("M")
    full = regimes.labels(prices, months, "M")
    cut = prices[prices.index <= "2015-12-31"]
    early = regimes.labels(cut, months[months <= pd.Period("2015-12", "M")], "M")
    pd.testing.assert_frame_equal(full.loc[early.index], early)


def test_states_and_returns_by_state(prices) -> None:
    months = evaluate.month_ends(prices).to_period("M")
    labels = regimes.labels(prices, months, "M").iloc[13:]
    assert set(labels["vol3"]) <= {"low", "mid", "high"}
    returns = pd.Series(0.01, index=labels.index)
    table = regimes.by_state(returns, returns * 0, labels)
    assert list(table["model"].unique()) == list(regimes.MODELS)
    vol2 = table[table["model"] == "vol2"]
    assert vol2["periods"].sum() == len(labels)
    assert (vol2["hit_rate"] == 1.0).all()

import numpy as np
import pandas as pd

import strategy
from prepare import TICKERS


def trending(slopes: dict[str, float]) -> pd.DataFrame:
    """14 months of daily prices, each ticker on a steady monthly trend."""
    days = pd.bdate_range("2020-01-01", "2021-02-28")
    months = np.arange(len(days)) / 21
    return pd.DataFrame(
        {ticker: 100 * (1 + slopes.get(ticker, 0.0)) ** months for ticker in TICKERS}, index=days
    )


def test_too_little_history_holds_cash() -> None:
    assert strategy.allocate(trending({}).iloc[:200]) == {}


def test_a_falling_canary_goes_defensive() -> None:
    weights = strategy.allocate(trending({"TIP": -0.01, "IEF": 0.005, "BIL": 0.001, "SPY": 0.05}))
    assert weights == {"IEF": 1.0}


def test_the_top_four_get_a_quarter_each() -> None:
    slopes = {"TIP": 0.01, "SPY": 0.04, "IWM": 0.03, "VEA": 0.02, "VWO": 0.015, "DBC": -0.02}
    assert strategy.allocate(trending(slopes)) == {
        "SPY": 0.25,
        "IWM": 0.25,
        "VEA": 0.25,
        "VWO": 0.25,
    }


def test_a_falling_top_four_asset_gives_its_slot_to_defense() -> None:
    slopes = {"TIP": 0.01, "SPY": 0.04, "IWM": 0.03, "BIL": 0.002}
    slopes |= {ticker: -0.01 for ticker in ["VEA", "VWO", "VNQ", "DBC", "IEF", "TLT"]}
    weights = strategy.allocate(trending(slopes))
    assert weights == {"SPY": 0.25, "IWM": 0.25, "BIL": 0.5}

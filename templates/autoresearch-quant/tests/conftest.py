import numpy as np
import pandas as pd
import pytest

from prepare import TICKERS


def synthetic_prices(
    start: str = "2007-06-01", end: str = "2022-12-31", seed: int = 0
) -> pd.DataFrame:
    """Daily prices with trends that change, so momentum rules have work to do.
    VEA starts late, as the real fund did."""
    days = pd.bdate_range(start, end)
    generator = np.random.default_rng(seed)
    drift = generator.normal(0.0002, 0.0004, size=(len(days) // 120 + 1, len(TICKERS)))
    drift = np.repeat(drift, 120, axis=0)[: len(days)]
    noise = generator.normal(0, 0.01, size=(len(days), len(TICKERS)))
    prices = pd.DataFrame(
        100 * np.exp(np.cumsum(drift + noise, axis=0)), index=days, columns=TICKERS
    )
    prices.loc[prices.index < "2007-07-20", "VEA"] = np.nan
    return prices


@pytest.fixture
def prices() -> pd.DataFrame:
    return synthetic_prices()

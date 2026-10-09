"""The strategy: the only file agents change during a run.

allocate(prices) is called at each month's last trading day with the daily
adjusted closes up to and including that day: a DataFrame with one column
per ticker in prepare.TICKERS, NaN before a fund existed. It returns the
weights to hold until the next month's last trading day, {ticker: weight},
long only, adding up to at most 1; the rest is cash at 0%.

The example is Hybrid Asset Allocation (HAA), from Keller and Keuning
(2023), "Dual and Canary Momentum with Rising Yields/Inflation", SSRN
4346906. Replace it with your own rules before a run.

1. Momentum is 13612U: the average of the 1, 3, 6, and 12-month returns.
2. Canary: when TIP's momentum is no higher than BIL's (cash), everything
   goes to the best defensive asset, BIL or IEF, whichever has the higher
   12-month return.
3. Otherwise the top 4 offensive assets by momentum get 25% each; a top-4
   asset whose own momentum is 0 or below gives its 25% to the best
   defensive asset.
"""

import pandas as pd

OFFENSIVE = ["SPY", "IWM", "VEA", "VWO", "VNQ", "DBC", "IEF", "TLT"]
DEFENSIVE = ["BIL", "IEF"]
CANARY = "TIP"
TOP = 4


def momentum(monthly: pd.DataFrame) -> pd.DataFrame:
    """13612U: the unweighted average of the 1, 3, 6, and 12-month returns."""
    returns = [monthly.pct_change(n, fill_method=None) for n in (1, 3, 6, 12)]
    return (returns[0] + returns[1] + returns[2] + returns[3]) / 4


def allocate(prices: pd.DataFrame) -> dict[str, float]:
    monthly = prices.resample("ME").last()
    if len(monthly) < 13:
        return {}  # 12 months of history first
    scores = momentum(monthly).iloc[-1]
    defensive_scores = monthly.pct_change(12, fill_method=None).iloc[-1]
    best_defensive = str(defensive_scores[DEFENSIVE].idxmax())
    canary = scores[CANARY]
    if pd.isna(canary) or canary <= scores["BIL"]:  # TIP no better than cash
        return {best_defensive: 1.0}
    offensive = scores[OFFENSIVE].sort_values(ascending=False)
    weights: dict[str, float] = {}
    for asset in offensive.head(TOP).index:
        target = str(asset) if offensive[asset] > 0 else best_defensive
        weights[target] = weights.get(target, 0.0) + 1.0 / TOP
    return weights

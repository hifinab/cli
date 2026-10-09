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
2. Canaries: TIP (rising yields/inflation, from HAA) and VWO (emerging
   markets, the crash canary of Keller and Keuning's DAA). Each canary
   whose momentum is no higher than BIL's (cash) sends half the portfolio
   to the best defensive asset, BIL or IEF, whichever has the higher
   momentum.
3. The rest goes equally to the top 4 offensive assets by momentum; a
   top-4 asset whose own momentum is 0 or below gives its share to the
   best defensive asset.
"""

import pandas as pd

OFFENSIVE = ["SPY", "IWM", "VEA", "VWO", "VNQ", "DBC", "IEF", "TLT"]
DEFENSIVE = ["BIL", "IEF"]
CANARIES = ["TIP", "VWO"]
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
    best_defensive = str(scores[DEFENSIVE].idxmax())
    bad = sum(not scores[c] > scores["BIL"] for c in CANARIES) / len(CANARIES)
    if bad == 1:
        return {best_defensive: 1.0}
    offensive = scores[OFFENSIVE].sort_values(ascending=False)
    weights: dict[str, float] = {}
    for asset in offensive.head(TOP).index:
        target = str(asset) if offensive[asset] > 0 else best_defensive
        weights[target] = weights.get(target, 0.0) + (1 - bad) / TOP
    if bad:
        weights[best_defensive] = weights.get(best_defensive, 0.0) + bad
    return weights

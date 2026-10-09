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
2. Canaries: TIP (HAA's inflation canary) and VWO (DAA's crash canary,
   Keller and Keuning 2018). Each one whose close is no higher than its
   12-month moving average (Faber's trend rule) and whose 13612U momentum is
   no higher than BIL's sends half the portfolio to the best defensive
   asset, BIL or IEF, whichever has the higher 12-month return. Requiring
   both signals cuts whipsaws: in-sample 1.240, against 1.219 for the trend
   rule alone and 1.203 for momentum alone.
3. The rest goes to the top 4 offensive assets by risk-adjusted momentum (13612U
   over the 3-month daily volatility) in equal parts, so a calm trend beats a
   volatile one of the same size. The canaries alone decide risk-off: a
   per-asset absolute momentum filter on top of it cut in-sample Sharpe
   from 1.03 to 0.88.
"""

import pandas as pd

OFFENSIVE = ["SPY", "IWM", "VEA", "VWO", "VNQ", "DBC", "IEF", "TLT"]
DEFENSIVE = ["BIL", "IEF"]
CANARIES = ["TIP", "VWO"]
TOP = 4
VOL_DAYS = 63  # 3 months


def momentum(monthly: pd.DataFrame) -> pd.DataFrame:
    """13612U: the unweighted average of the 1, 3, 6, and 12-month returns."""
    returns = [monthly.pct_change(n, fill_method=None) for n in (1, 3, 6, 12)]
    return (returns[0] + returns[1] + returns[2] + returns[3]) / 4


def allocate(prices: pd.DataFrame) -> dict[str, float]:
    monthly = prices.resample("ME").last()
    if len(monthly) < 13:
        return {}  # 12 months of history first
    scores = momentum(monthly).iloc[-1]
    best_defensive = str(monthly.pct_change(12, fill_method=None).iloc[-1][DEFENSIVE].idxmax())
    trend = monthly.iloc[-12:].mean()  # 12-month moving average of month-end closes
    below_trend = monthly[CANARIES].iloc[-1] <= trend[CANARIES]
    below_cash = scores[CANARIES] <= scores["BIL"]
    bad = int((below_trend & below_cash).sum())  # both signals agree: half to defense
    risk_on = 1 - bad / len(CANARIES)
    weights = {best_defensive: 1 - risk_on} if bad else {}
    if risk_on == 0:
        return weights
    volatility = prices[OFFENSIVE].pct_change(fill_method=None).iloc[-VOL_DAYS:].std()
    offensive = (scores[OFFENSIVE] / volatility).sort_values(ascending=False)
    for asset in offensive.head(TOP).index:
        weights[str(asset)] = weights.get(str(asset), 0.0) + risk_on / TOP
    return weights

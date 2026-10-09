"""Market regimes from the benchmark's own prices, for the report.

Each period gets the state known when its weights were chosen: the label
at the end of the period before. Nothing here looks ahead, and nothing is
fitted, so the labels can't flatter a strategy.

  vol2   calm or volatile: the benchmark's 21-day volatility below or above
         its median so far
  vol3   low, mid, or high: below the 33rd percentile so far, between, or
         above the 67th
  trend  up or down: the benchmark above or below its 210-day (about ten
         months) average

The strategy decision record this follows found two volatility states
enough to trade on, and trend states slow; the report shows all three, so
you can see which one a strategy's returns depend on.
"""

import math

import numpy as np
import pandas as pd

import evaluate

MODELS = {
    "vol2": ["calm", "volatile"],
    "vol3": ["low", "mid", "high"],
    "trend": ["up", "down"],
}
VOL_DAYS = 21
TREND_DAYS = 210
MIN_HISTORY = 252  # days before the first label


def daily_states(prices: pd.Series) -> pd.DataFrame:
    """Each day's state in every model, from the closes up to that day."""
    returns = prices.pct_change(fill_method=None)
    volatility = returns.rolling(VOL_DAYS).std() * math.sqrt(252)
    expanding = volatility.expanding(MIN_HISTORY)
    median = expanding.median()
    low, high = expanding.quantile(1 / 3), expanding.quantile(2 / 3)
    average = prices.rolling(TREND_DAYS).mean()
    states = pd.DataFrame(index=prices.index)
    states["vol2"] = np.where(volatility > median, "volatile", "calm")
    states["vol3"] = np.select([volatility < low, volatility > high], ["low", "high"], "mid")
    states["trend"] = np.where(prices > average, "up", "down")
    states[median.isna()] = None
    states.loc[average.isna(), "trend"] = None
    return states


def labels(
    prices: pd.DataFrame, index: pd.PeriodIndex, rebalance: str | None = None
) -> pd.DataFrame:
    """The state each period was held in: the one known at the period before's end."""
    rebalance = rebalance or evaluate.REBALANCE
    days = evaluate.rebalance_days(prices, rebalance, 0)
    states = daily_states(prices[evaluate.BENCHMARK]).loc[days]
    states.index = days.to_period(rebalance)
    return states.shift(1).reindex(index)


def by_state(returns: pd.Series, bench: pd.Series, states: pd.DataFrame) -> pd.DataFrame:
    """Returns in each state of each model, next to the benchmark's."""
    per_year = evaluate.periods_per_year(pd.PeriodIndex(returns.index))
    rows = []
    for model, names in MODELS.items():
        for name in names:
            mask = (states[model] == name).to_numpy()
            mine, theirs = returns[mask], bench[mask]
            rows.append(
                {
                    "model": model,
                    "state": name,
                    "periods": int(mask.sum()),
                    "share": float(mask.mean()),
                    "ann_return_pct": float(mine.mean()) * per_year * 100,
                    "bench_ann_return_pct": float(theirs.mean()) * per_year * 100,
                    "ann_vol_pct": float(mine.std()) * math.sqrt(per_year) * 100,
                    "hit_rate": float((mine > theirs).mean()) if mask.any() else math.nan,
                }
            )
    return pd.DataFrame(rows)

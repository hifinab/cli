"""The data: which prices, from when, and where the holdout starts.

Run it on this machine, not in a box, once before a run (it needs the
network):

    make data

eval/prices.csv                 daily adjusted closes up to SPLIT, committed:
                                the loop scores strategies on these alone.
../<folder>-holdout/prices.csv  every complete month up to now, outside the
                                repository, so agents never see what came
                                after SPLIT; make holdout checks the result
                                on it, once, at the end.

Change TICKERS, START, and SPLIT before a run, never during one.
"""

import os
from pathlib import Path

import pandas as pd

TICKERS = ["SPY", "IWM", "VEA", "VWO", "VNQ", "DBC", "IEF", "TLT", "BIL", "TIP"]
START = "2007-06-01"
SPLIT = "2022-12-31"

HERE = Path(__file__).resolve().parent
EVAL = HERE / "eval" / "prices.csv"
HOLDOUT = Path(os.environ.get("HOLDOUT") or HERE.parent / f"{HERE.name}-holdout" / "prices.csv")


def download() -> pd.DataFrame:
    import yfinance as yf  # only here: boxes don't have it

    data = yf.download(TICKERS, start=START, auto_adjust=True, progress=False)
    if data is None or data.empty:
        raise SystemExit("yfinance returned no prices; try again later")
    prices = data["Close"][TICKERS]
    prices.index = pd.to_datetime(prices.index).tz_localize(None)
    prices.index.name = "date"
    # Only complete months: the month in progress would end on a partial return.
    last_complete = pd.Timestamp.today().normalize() - pd.offsets.MonthEnd(1)
    return prices[prices.index <= last_complete]


def save(prices: pd.DataFrame) -> None:
    in_sample = prices[prices.index <= SPLIT]
    EVAL.parent.mkdir(parents=True, exist_ok=True)
    in_sample.to_csv(EVAL, float_format="%.6f")
    HOLDOUT.parent.mkdir(parents=True, exist_ok=True)
    prices.to_csv(HOLDOUT, float_format="%.6f")
    print(f"{EVAL.relative_to(HERE)}: {in_sample.index[0].date()} to {in_sample.index[-1].date()}")
    print(f"{HOLDOUT}: {prices.index[0].date()} to {prices.index[-1].date()}")


if __name__ == "__main__":
    save(download())

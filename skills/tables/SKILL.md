---
name: tables
description: Load, clean, join, aggregate, and summarise tabular data from CSV, Parquet, JSON, and Excel files with pandas and DuckDB. Use for any task that counts, sums, compares, or reshapes rows of data, and to write results back as CSV, Parquet, or Excel.
---

# Tables with pandas and DuckDB

DuckDB runs SQL straight on files, which is fastest for large ones:

```python
import duckdb

con = duckdb.connect()
print(con.sql("DESCRIBE SELECT * FROM 'data/*.parquet'"))
daily = con.sql("""
    SELECT date, sum(volume) AS volume, count(*) AS rows
    FROM 'data/*.parquet'
    GROUP BY date ORDER BY date
""").df()          # a pandas DataFrame
```

pandas for cleaning and reshaping:

```python
import pandas as pd

df = pd.read_csv("prices.csv", parse_dates=["date"])
print(df.shape, df.dtypes, df.isna().sum(), sep="\n")
df = df.drop_duplicates().dropna(subset=["price"])
summary = df.groupby("provider")["price"].agg(["min", "median", "max", "count"])
summary.to_csv("summary.csv")
summary.to_excel("summary.xlsx")    # needs the spreadsheets skill
df.to_parquet("clean.parquet")
```

- Look before you compute: the shape, the column types, missing values, and
  a few rows. Say what you dropped or changed and how many rows it was.
- Keep units in column names (`price_usd_per_hour`) and dates as dates.
- Check results with a second, simple calculation (a total, a count) before
  you report them.
- For files bigger than memory, stay in DuckDB and only bring the result
  into pandas.

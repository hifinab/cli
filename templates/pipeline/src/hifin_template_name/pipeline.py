"""One run per date: fetch, land the raw response, parse it, load it.

Each step can be rerun safely. A raw file that is already landed is never
fetched again, and loading skips rows that are already in the table, so
running a date twice loads nothing new.
"""

import json
import sqlite3
from collections.abc import Callable
from dataclasses import dataclass
from datetime import date
from pathlib import Path

import httpx

SOURCE = "example"
# Replace with the real source. The tests never call it.
SOURCE_URL = "https://example.com/api/prices?date={date}"

type Fetch = Callable[[date], bytes]


@dataclass(frozen=True)
class Row:
    day: date
    symbol: str
    price: float


def fetch(day: date) -> bytes:
    response = httpx.get(SOURCE_URL.format(date=day.isoformat()), timeout=30)
    response.raise_for_status()
    return response.content


def land(day: date, data_dir: Path, fetch: Fetch = fetch) -> Path:
    """Store the raw response before parsing it, so parsing can be fixed
    and rerun without fetching again."""
    path = data_dir / "raw" / SOURCE / f"{day.isoformat()}.json"
    if path.exists():
        return path
    content = fetch(day)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(".tmp")
    temporary.write_bytes(content)
    temporary.rename(path)
    return path


def parse(day: date, raw: bytes) -> list[Row]:
    document = json.loads(raw)
    return [
        Row(day=day, symbol=item["symbol"], price=float(item["price"]))
        for item in document["prices"]
    ]


def load(rows: list[Row], database: Path) -> int:
    """Insert rows that aren't there yet; return how many were new."""
    database.parent.mkdir(parents=True, exist_ok=True)
    with sqlite3.connect(database) as connection:
        connection.execute(
            "CREATE TABLE IF NOT EXISTS prices"
            " (day TEXT, symbol TEXT, price REAL, PRIMARY KEY (day, symbol))"
        )
        before = connection.total_changes
        connection.executemany(
            "INSERT OR IGNORE INTO prices VALUES (?, ?, ?)",
            [(row.day.isoformat(), row.symbol, row.price) for row in rows],
        )
        return connection.total_changes - before


def run(day: date, data_dir: Path, fetch: Fetch = fetch) -> int:
    raw = land(day, data_dir, fetch)
    return load(parse(day, raw.read_bytes()), data_dir / "warehouse.sqlite")

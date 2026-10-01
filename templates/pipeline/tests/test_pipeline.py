"""Offline tests: parsing runs on recorded responses in tests/fixtures."""

from datetime import date
from pathlib import Path

from hifin_template_name.pipeline import Row, parse, run

FIXTURES = Path(__file__).parent / "fixtures"
DAY = date(2026, 1, 2)


def recorded(day: date) -> bytes:
    return (FIXTURES / f"example-{day.isoformat()}.json").read_bytes()


def test_parses_a_recorded_response() -> None:
    assert parse(DAY, recorded(DAY)) == [Row(DAY, "AAA", 101.5), Row(DAY, "BBB", 20.0)]


def test_a_rerun_loads_nothing_new(tmp_path: Path) -> None:
    fetches: list[date] = []

    def fake_fetch(day: date) -> bytes:
        fetches.append(day)
        return recorded(day)

    assert run(DAY, tmp_path, fake_fetch) == 2
    assert run(DAY, tmp_path, fake_fetch) == 0
    assert fetches == [DAY], "a landed raw file was fetched again"
    assert (tmp_path / "raw" / "example" / "2026-01-02.json").exists()

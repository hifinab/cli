import base64
import json
import math
from pathlib import Path

import pandas as pd
import pytest

import results
from tests.conftest import synthetic_prices

BASELINE = Path("strategy.py").read_text()
TOP3 = BASELINE.replace("TOP = 4", "TOP = 3")
CHEAT = "import os\n" + BASELINE
SOURCES = {"base": BASELINE, "refs/best-of/b-1/r1-1": TOP3, "refs/best-of/b-1/r1-2": BASELINE}
SOURCES["refs/best-of/b-1/r2-1"] = CHEAT


def read_source(rev: str | list[str]) -> str | None:
    for candidate in [rev] if isinstance(rev, str) else rev:
        if candidate in SOURCES:
            return SOURCES[candidate]
    return None


def box(name: str, round_: int, score: float | None, files: int = 1) -> dict:
    return {
        "name": f"p-b1-r{round_}-{name}",
        "branch": f"best-of/b-1/r{round_}-{name}",
        "agent": "claude",
        "model": "m",
        "files": files,
        "added": 3,
        "removed": 1,
        "score": score,
        "idea": f"idea {round_}-{name}",
        "cost_usd": 0.5,
        "tokens": {"input": 10, "output": 2},
    }


def fake_run(top3_score: float) -> dict:
    return {
        "id": "b-1",
        "root": str(results.HERE),
        "created": "2026-10-09T00:00:00Z",
        "base": "base",
        "score": "make score",
        "prompt": "",
        "task_file": "program.md",
        "options": {"agents": ["claude", "claude"]},
        "loop": {
            "lower": False,
            "baseline": None,
            "best": top3_score,
            "best_round": 1,
            "branch": "best-of/b-1/best",
            "state": "done",
            "reason": "finished 2 rounds",
            "spend_usd": 2.0,
            "started": "2026-10-09T00:00:00Z",
            "history": [
                {
                    "round": 1,
                    "result": "kept",
                    "winner": "p-b1-r1-1",
                    "commit": "c1",
                    "score": top3_score,
                    "ended": "2026-10-09T00:10:00Z",
                    "boxes": [box("1", 1, top3_score), box("2", 1, 0.1, files=0)],
                },
                {
                    "round": 2,
                    "result": "crash",
                    "ended": "2026-10-09T00:20:00Z",
                    "boxes": [box("1", 2, None)],
                },
            ],
        },
    }


@pytest.fixture(scope="module")
def built() -> tuple[dict[str, pd.DataFrame], dict[str, str]]:
    full = synthetic_prices(end="2024-12-31")
    in_sample = full[full.index <= "2022-12-31"]
    # The recorded score of the kept attempt is what evaluate gives it.
    results._init(in_sample, full)
    top3 = results.run_source(TOP3)["in"]["sharpe"]
    return results.build(fake_run(top3), in_sample, full, read_source, workers=1)


def test_every_attempt_is_a_row(built):
    attempts = built[0]["attempts"]
    assert list(attempts["name"]) == ["baseline", "p-b1-r1-1", "p-b1-r1-2", "p-b1-r2-1"]
    assert list(attempts["result"]) == ["baseline", "kept", "unchanged", "failed"]
    # Versions are the strategies the loop kept, in order.
    assert list(attempts["version"]) == [0, 1, -1, -1]
    # An attempt that left strategy.py as it was shares the baseline's backtest.
    assert attempts["source_sha"][2] == attempts["source_sha"][0]
    assert attempts["is_sharpe"][2] == attempts["is_sharpe"][0]


def test_scores_are_re_run_and_checked(built):
    attempts = built[0]["attempts"]
    assert attempts["matches_recorded"][1]
    # The unchanged box's recorded 0.1 isn't what its files score.
    assert not attempts["matches_recorded"][2]
    assert math.isfinite(attempts["ho_sharpe"][1])
    # The evaluation's rules still hold: a strategy that imports os fails.
    assert "imports os" in attempts["error"][3]
    assert math.isnan(attempts["is_sharpe"][3])


def test_returns_cover_every_month_and_the_benchmark(built):
    tables = built[0]
    returns = tables["returns"]
    months = returns[returns["attempt"] == -1]["month"]
    assert months.iloc[0] == "2008-07"
    assert months.iloc[-1] == "2024-12"
    for attempt in (0, 1, 2):
        assert list(returns[returns["attempt"] == attempt]["month"]) == list(months)
    run = tables["run"].iloc[0]
    assert run["first_holdout_month"] == "2023-01"
    assert run["holdout_scored"]
    assert math.isfinite(run["bench_ho_sharpe"])
    assert set(tables["weights"]["ticker"]) <= set(results.TICKERS)


def test_no_holdout_means_in_sample_only():
    in_sample = synthetic_prices()
    tables, _ = results.build(fake_run(0.5), in_sample, None, read_source, workers=1)
    assert not tables["run"]["holdout_scored"][0]
    assert tables["attempts"]["ho_sharpe"].isna().all()


def test_write_embeds_the_tables_in_the_report(built, tmp_path):
    tables, sources = built
    results.write(tmp_path, {"id": "b-1"}, tables, sources)
    for name in tables:
        assert len(pd.read_parquet(tmp_path / f"{name}.parquet")) == len(tables[name])
    assert (tmp_path / "strategies" / "p-b1-r1-1.py").read_text() == TOP3
    page = (tmp_path / "report.html").read_text()
    assert "/*__PARQUET__*/" not in page
    assert "/*__HYPARQUET__*/" not in page
    assert "parquetReadObjects" in page
    embedded = json.loads(page.split("const EMBEDDED = ", 1)[1].split(";\n", 1)[0])
    assert base64.b64decode(embedded["attempts"]) == (tmp_path / "attempts.parquet").read_bytes()


def test_find_run():
    here = results.HERE
    runs = [
        {"id": "b-1", "root": str(here), "created": "2026-01-01", "loop": {"round": 1}},
        {"id": "b-2", "root": str(here), "created": "2026-02-01", "loop": {"round": 1}},
        {"id": "b-3", "root": "/elsewhere", "created": "2026-03-01", "loop": {"round": 1}},
        {"id": "b-4", "root": str(here), "created": "2026-04-01", "loop": None},
    ]
    assert results.find_run(runs, here)["id"] == "b-2"
    assert results.find_run(runs, here, "b-3")["id"] == "b-3"
    with pytest.raises(SystemExit):
        results.find_run(runs, Path("/nowhere"))

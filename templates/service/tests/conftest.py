from collections.abc import Iterator
from pathlib import Path

import pytest
from alembic import command
from alembic.config import Config
from fastapi.testclient import TestClient

from hifin_template_name.db import get_engine
from hifin_template_name.settings import get_settings

ROOT = Path(__file__).parent.parent


def alembic_config(url: str) -> Config:
    config = Config(str(ROOT / "alembic.ini"))
    config.set_main_option("sqlalchemy.url", url)
    return config


@pytest.fixture
def database_url(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Iterator[str]:
    url = f"sqlite:///{tmp_path / 'test.db'}"
    monkeypatch.setenv("APP_DATABASE_URL", url)
    get_settings.cache_clear()
    get_engine.cache_clear()
    command.upgrade(alembic_config(url), "head")
    yield url
    get_engine.cache_clear()
    get_settings.cache_clear()


@pytest.fixture
def migrations(database_url: str) -> Config:
    return alembic_config(database_url)


@pytest.fixture
def client(database_url: str) -> TestClient:
    from hifin_template_name.app import app

    return TestClient(app)

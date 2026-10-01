"""Settings come from the environment (APP_*) or .env, never from code."""

from functools import lru_cache

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="APP_", env_file=".env", extra="ignore")

    database_url: str = "sqlite:///data/app.db"


@lru_cache
def get_settings() -> Settings:
    return Settings()

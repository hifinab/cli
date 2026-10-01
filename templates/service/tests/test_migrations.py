from alembic import command
from alembic.config import Config


def test_migrations_match_the_models(migrations: Config) -> None:
    # Fails when a model changed without a migration.
    command.check(migrations)

from alembic import context
from sqlalchemy import create_engine

from hifin_template_name import models  # noqa: F401  (registers the tables)
from hifin_template_name.db import Base
from hifin_template_name.settings import get_settings

url = context.config.get_main_option("sqlalchemy.url") or get_settings().database_url


def run() -> None:
    if context.is_offline_mode():
        context.configure(url=url, target_metadata=Base.metadata, literal_binds=True)
        with context.begin_transaction():
            context.run_migrations()
        return
    with create_engine(url).connect() as connection:
        context.configure(
            connection=connection, target_metadata=Base.metadata, render_as_batch=True
        )
        with context.begin_transaction():
            context.run_migrations()


run()

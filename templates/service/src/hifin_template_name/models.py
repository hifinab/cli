"""Database tables. After changing them, add a migration:
uv run alembic revision --autogenerate -m "what changed"
"""

from sqlalchemy import String
from sqlalchemy.orm import Mapped, mapped_column

from hifin_template_name.db import Base


class Item(Base):
    __tablename__ = "items"

    id: Mapped[int] = mapped_column(primary_key=True)
    name: Mapped[str] = mapped_column(String(200))

from typing import Annotated

from fastapi import Depends, FastAPI
from pydantic import BaseModel
from sqlalchemy import select, text
from sqlalchemy.orm import Session

from hifin_template_name.db import get_session
from hifin_template_name.models import Item

app = FastAPI(title="Hifin Template Name")

SessionDependency = Annotated[Session, Depends(get_session)]


class ItemIn(BaseModel):
    name: str


class ItemOut(BaseModel):
    id: int
    name: str


@app.get("/healthz")
def healthz() -> dict[str, str]:
    """The process is up."""
    return {"status": "ok"}


@app.get("/readyz")
def readyz(session: SessionDependency) -> dict[str, str]:
    """The process can serve requests: the database answers."""
    session.execute(text("SELECT 1"))
    return {"status": "ready"}


@app.post("/items", status_code=201)
def create_item(item: ItemIn, session: SessionDependency) -> ItemOut:
    row = Item(name=item.name)
    session.add(row)
    session.commit()
    return ItemOut(id=row.id, name=row.name)


@app.get("/items")
def list_items(session: SessionDependency) -> list[ItemOut]:
    return [
        ItemOut(id=row.id, name=row.name) for row in session.scalars(select(Item).order_by(Item.id))
    ]

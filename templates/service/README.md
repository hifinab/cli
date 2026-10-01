# Hifin Template Name

A FastAPI service with SQLAlchemy and Alembic.

```sh
uv sync
make check
make run          # http://127.0.0.1:8000/docs
docker compose up --build
```

Settings come from `APP_*` environment variables; see `settings.py` and
`.env.example`.

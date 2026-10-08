# hi box and hi agent run in this image: Ubuntu, uv, and the project's
# locked dependencies with one PyTorch build in /opt/venv, so boxes start at
# once and need no network. hi builds it once, and again when uv.lock or
# TORCH in devcontainer.json changes.
FROM docker.io/library/ubuntu:24.04
ARG TORCH=cpu
RUN userdel -r ubuntu 2>/dev/null || true \
 && useradd --uid 1000 --home-dir /box/home --no-create-home --shell /bin/bash box \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates curl git less jq ripgrep make procps file \
 && rm -rf /var/lib/apt/lists/*
RUN curl -LsSf https://astral.sh/uv/0.11.21/install.sh | env UV_INSTALL_DIR=/usr/local/bin UV_NO_MODIFY_PATH=1 sh
ENV UV_PYTHON_INSTALL_DIR=/opt/python UV_PROJECT_ENVIRONMENT=/opt/venv UV_LINK_MODE=copy UV_COMPILE_BYTECODE=1
COPY pyproject.toml uv.lock .python-version /opt/project/
RUN group="$TORCH"; [ "$group" = auto ] && group=cpu; \
    cd /opt/project && uv sync --frozen --no-install-project --no-default-groups --group dev --group "$group" \
 && chmod -R a+rX /opt/venv /opt/python
ENV HOME=/box/home LANG=C.UTF-8 TERM=xterm-256color
WORKDIR /box

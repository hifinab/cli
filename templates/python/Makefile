# hi:begin: written by hi init; change it in the template, not here
.DEFAULT_GOAL := help
.PHONY: help check fix lint test

help: ## Show the targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'

check: ## Lockfile, format, lint, types, and tests; must pass before a change is done
	uv lock --check
	uv run --frozen ruff format --check
	uv run --frozen ruff check
	uv run --frozen pyrefly check
	uv run --frozen pytest -q

fix: ## Format and fix what ruff can
	uv run --frozen ruff format
	uv run --frozen ruff check --fix

lint: ## Format check and lint only
	uv run --frozen ruff format --check
	uv run --frozen ruff check

test: ## Tests only
	uv run --frozen pytest -q
# hi:end

# Project targets below.

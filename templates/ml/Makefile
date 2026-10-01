# hi:begin: written by hi init; change it in the template, not here
-include local.mk
# The PyTorch build for this machine: rocm on a Strix Halo (gfx1151), cuda
# with an NVIDIA GPU, cpu otherwise. To choose another, set TORCH in the
# environment or in local.mk, which Git ignores: TORCH = cpu
TORCH ?= $(shell if grep -qs 'gfx_target_version 110501' /sys/class/kfd/kfd/topology/nodes/*/properties; then echo rocm; elif command -v nvidia-smi >/dev/null 2>&1; then echo cuda; else echo cpu; fi)
UV := uv run --frozen --no-default-groups --group dev --group $(TORCH)
.DEFAULT_GOAL := help
.PHONY: help check fix lint test sync

help: ## Show the targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'
	@echo "  PyTorch build here: $(TORCH)"

sync: ## Install the dependencies with this machine's PyTorch build
	uv sync --no-default-groups --group dev --group $(TORCH)

check: ## Lockfile, format, lint, types, and tests; must pass before a change is done
	uv lock --check
	$(UV) ruff format --check
	$(UV) ruff check
	$(UV) pyrefly check
	$(UV) pytest -q

fix: ## Format and fix what ruff can
	$(UV) ruff format
	$(UV) ruff check --fix

lint: ## Format check and lint only
	$(UV) ruff format --check
	$(UV) ruff check

test: ## Tests only
	$(UV) pytest -q
# hi:end

# Project targets below.
.PHONY: train

train: ## Train on this machine's GPU, or the CPU (STEPS=200)
	$(UV) python -m hifin_template_name.train --steps $(or $(STEPS),200)

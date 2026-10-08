# hi:begin: written by hi init; change it in the template, not here
-include local.mk
# The PyTorch build for this machine: rocm on a Strix Halo (gfx1151), cuda
# with an NVIDIA GPU, cpu otherwise. To choose another, set TORCH in the
# environment or in local.mk, which Git ignores: TORCH = cpu
TORCH ?= $(shell if grep -qs 'gfx_target_version 110501' /sys/class/kfd/kfd/topology/nodes/*/properties; then echo rocm; elif command -v nvidia-smi >/dev/null 2>&1; then echo cuda; else echo cpu; fi)
# In a box (hi agent best-of), the tools come from the box image's /opt/venv;
# on this machine, uv runs them from the lockfile with this machine's build.
ifdef HI_BOX
RUN := env PATH=/opt/venv/bin:$(PATH)
LOCK_CHECK := true
else
RUN := uv run --frozen --no-default-groups --group dev --group $(TORCH)
LOCK_CHECK := uv lock --check
endif
# Boxes run AMD's build with the GPU on a Strix Halo, and the CPU build elsewhere.
BOX_TORCH := $(if $(filter rocm,$(TORCH)),rocm,cpu)
.DEFAULT_GOAL := help
.PHONY: help check fix lint test sync

help: ## Show the targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'
	@echo "  PyTorch build here: $(TORCH)"

sync: ## Install the dependencies with this machine's PyTorch build; the first time, set the box image's
	uv sync --no-default-groups --group dev --group $(TORCH)
	@if grep -q '"TORCH": "auto"' .devcontainer/devcontainer.json; then \
	  sed -i 's/"TORCH": "auto"/"TORCH": "$(BOX_TORCH)"/' .devcontainer/devcontainer.json; \
	  if [ "$(BOX_TORCH)" = rocm ]; then sed -i 's/"gpu": false/"gpu": true/' .devcontainer/devcontainer.json; fi; \
	  echo "Boxes get the $(BOX_TORCH) build of PyTorch (.devcontainer/devcontainer.json)."; \
	fi

check: ## Lockfile, format, lint, types, and tests; must pass before a change is done
	$(LOCK_CHECK)
	$(RUN) ruff format --check
	$(RUN) ruff check
	$(RUN) pyrefly check
	$(RUN) pytest -q

fix: ## Format and fix what ruff can
	$(RUN) ruff format
	$(RUN) ruff check --fix

lint: ## Format check and lint only
	$(RUN) ruff format --check
	$(RUN) ruff check

test: ## Tests only
	$(RUN) pytest -q
# hi:end

# Project targets below.
.PHONY: score holdout data loop

HOLDOUT ?= ../$(notdir $(CURDIR))-holdout/test.txt
BOXES ?= 2
ROUNDS ?= 20
BUDGET ?= 20

score: ## Train for prepare.TIME_BUDGET and score; the last line is the number the loop lowers
	@$(RUN) python evaluate.py

holdout: ## Train, then score on the text outside the repository; once, at the end
	@test -z "$(HI_BOX)" || { echo "the holdout stays outside boxes"; exit 1; }
	@$(RUN) python evaluate.py --holdout $(HOLDOUT)

data: ## Download the text: eval/ in the repository, the holdout outside it
	$(RUN) python prepare.py

loop: ## Rounds in the background: BOXES a round, up to ROUNDS, BUDGET dollars
	hi agent best-of $(BOXES) --rounds $(ROUNDS) --budget $(BUDGET) --patience 10 --max 30m \
	  --score "make score" --lower --edit train.py program.md

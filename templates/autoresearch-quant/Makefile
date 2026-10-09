# hi:begin: written by hi init; change it in the template, not here
# In a box (hi agent best-of), the tools come from the box image's /opt/venv;
# on this machine, uv runs them from the lockfile.
ifdef HI_BOX
RUN := env PATH=/opt/venv/bin:$(PATH)
LOCK_CHECK := true
else
RUN := uv run --frozen
LOCK_CHECK := uv lock --check
endif
.DEFAULT_GOAL := help
.PHONY: help check fix lint test

help: ## Show the targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'

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
.PHONY: score robust holdout data loop results

HOLDOUT ?= ../$(notdir $(CURDIR))-holdout/prices.csv
BOXES ?= 3
ROUNDS ?= 20
BUDGET ?= 20

score: ## The in-sample scores; the last line (evaluate.SCORE) is the number the loop improves
	@$(RUN) python evaluate.py

robust: ## How much of the score is luck: start sets, tranches, costs, neighbours, placebo
	@$(RUN) python robust.py

holdout: ## The score after prepare.SPLIT, from outside the repository; once, at the end
	@test -z "$(HI_BOX)" || { echo "the holdout stays outside boxes"; exit 1; }
	@$(RUN) python evaluate.py --holdout $(HOLDOUT)

data: ## Download the prices: eval/ in the repository, the holdout outside it
	uv run --frozen --group data python prepare.py

loop: ## Rounds in the background: BOXES a round, up to ROUNDS, BUDGET dollars; results/ at the end
	hi agent best-of $(BOXES) --rounds $(ROUNDS) --budget $(BUDGET) --patience 10 --max 20m \
	  --score "make score" --higher --edit strategy.py --then "make results" program.md

results: ## Every attempt of the newest run (or ID=b-2), re-run, in results/<run>/ with report.html
	@test -z "$(HI_BOX)" || { echo "results stay outside boxes: they hold holdout scores"; exit 1; }
	@$(RUN) python results.py $(if $(ID),--run $(ID)) --holdout $(HOLDOUT)

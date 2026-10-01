# hi:begin: written by hi init; change it in the template, not here
.DEFAULT_GOAL := help
.PHONY: help check fix lint test

help: ## Show the targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-8s %s\n", $$1, $$2}'

check: ## Packages, format, lint, types, tests, and build; must pass before a change is done
	npm ls --silent > /dev/null
	npx --no-install biome ci .
	npx --no-install tsc
	npx --no-install vitest run
	npx --no-install vite build

fix: ## Format and fix what Biome can
	npx --no-install biome check --write .

lint: ## Format check and lint only
	npx --no-install biome ci .

test: ## Tests only
	npx --no-install vitest run
# hi:end

# Project targets below.

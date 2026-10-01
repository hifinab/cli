<!-- hi:begin: written by hi init; change it in the template, not here -->
# Rules

- Run `make check` before saying a change is done. It must pass. `make fix`
  repairs formatting and simple lint errors.
- Never read, print, or commit `.env`, `.env.*` (except `.env.example`), or
  anything in `secrets/`. Settings come from the environment; add a new
  variable's name to `.env.example` with no real value.
- Never commit data or results: `data/` and `results/` are ignored on
  purpose.
- Features are specified in `docs/specs/` and planned in `docs/roadmap.md`.
  Record a decision that others should not relitigate in `docs/decisions/`.
- The Claude Code settings deny reading `.env*` and `secrets/` to Claude's own
  tools only. They are not a boundary against scripts you run.
<!-- hi:end -->

# This project

Add what an agent gets wrong here, one line each, as you notice it.

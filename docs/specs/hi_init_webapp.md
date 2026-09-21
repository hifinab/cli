# `hi init webapp` specification

Status: Draft

Dependencies: `hi init` planner, metadata, conflict detection, and a selected
Hifin web stack.

## Goal

Generate one deterministic, production-capable web application recipe instead
of an interactive matrix of interchangeable frameworks.

## Command

```text
hi init webapp [directory]
```

The interactive `hi init` wizard can select the same template. Both forms use
one planner and generator.

## Required recipe decisions

Before implementation, choose and document exactly one supported combination
for:

- Server runtime and framework
- Frontend runtime and framework, if separate
- Package manager and lockfile
- Database and migration tool
- Formatter, linter, type checker, and test runner
- Local container composition and production image
- CI commands and deployment artifact

Every runtime, image, action, and dependency family must be pinned or locked.
The template must not ask users to choose alternatives during generation.

## Structure and commands

The generated repository includes application source, tests, migrations,
operational documentation, specifications, agent instructions, environment
examples, and the template metadata established by `hi_init.md`.

It defines deterministic commands for development, formatting, linting, type
checking, testing, database migration, production build, and container startup.
A fresh clone must not require undocumented global tools beyond those named by
the recipe.

## Configuration and secrets

Commit a `.env.example` containing names and safe example values only. Never
generate live credentials. Development defaults bind locally unless the user
explicitly changes them. Production configuration fails closed when required
secrets are absent.

## Safety

The template inherits the no-overwrite, preflight, cancellation, atomic-write,
and matching-rerun behavior from `hi_init.md`. Database or container commands
are described after generation; initialization must not start services or apply
migrations implicitly.

## Acceptance criteria

1. Direct and wizard invocation generate identical plans.
2. A fresh generated project installs from committed lockfiles.
3. Documented format, lint, type-check, test, build, and migration commands run.
4. The production container starts the built application and exposes a health
   endpoint.
5. No generated file contains a credential or machine-specific absolute path.
6. Existing conflicts stop generation before any command or write.

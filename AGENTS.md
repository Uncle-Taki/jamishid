# Repository Guidelines

## Project Structure & Module Organization

Jamshid aggregates and scores vehicle listings from Divar, Bama, and Sheypoor. The Go backend follows hexagonal architecture:

- `backend/cmd/api/main.go`: dependency wiring and HTTP server startup.
- `backend/internal/domain/vehicle/`: entities, search criteria, and scoring; `domain/ports/`: interfaces.
- `backend/internal/application/aggregation/`: aggregation workflows.
- `backend/internal/adapters/`: inbound Gin handlers and outbound scrapers and memory cache.
- `src/services/apiCrawler.ts`: frontend API client.

Keep business rules in the domain, workflows in the application layer, and HTTP and marketplace details in adapters. Tests sit beside implementation files. No dedicated asset directory is currently tracked.

## Build, Test, and Development Commands

Use Go 1.24 or newer. From the repository root:

- `make -C backend build`: build `backend/bin/jamshid-backend`.
- `make -C backend run`: start the API on port 8080.
- `make -C backend test`: run internal tests with race detection and coverage, print coverage, and remove the report.
- `docker compose up --build backend`: build and run the backend container.
- `curl http://localhost:8080/api/v1/health`: check API health.

The frontend Dockerfile expects `package.json` and an npm `dev` script, but package manifests and referenced frontend types are missing. Restore the frontend setup before running the full Compose stack.

## Coding Style & Naming Conventions

Format Go changes with `gofmt`; use tabs, lowercase package names, `MixedCaps` identifiers, and descriptive filenames such as `scorer.go`. Follow the TypeScript client's existing four-space indentation and camelCase naming. No dedicated lint configuration is tracked. Preserve JSON contracts between HTTP DTOs and the frontend client.

## Testing Guidelines

Use Go's `testing` package, `net/http/httptest`, and interface-based mocks. Name files `*_test.go` and functions `TestFeature` or `TestComponent_Behavior`. Cover scoring, normalization, cache behavior, handler contracts, and scraper failures using local fixtures or mock servers. Run `make -C backend test` before submitting backend changes. No numeric coverage threshold is configured.

## Commit & Pull Request Guidelines

Recent implementation commits use `feat:` and `fix:` prefixes with concise descriptions. Follow that pattern. PRs should explain the behavior change, include validation commands and results, and link relevant issues. Include request/response examples for API changes and screenshots for UI changes.

## Agent Instructions

Follow `@/home/mehdi/.codex-gpt-3/RTK.md`: prefix shell commands with `rtk`; use `rtk proxy` when raw command output is needed.

# Fleet Plan

Semantic diff for Fleet device management: `terraform plan` for fleet-gitops. Compares YAML configs against live Fleet API state. Read-only (GET only, never mutates).

## Architecture

Read `docs/Architecture.md` for data flow, package layout, diff matching keys, and CI mode. Read `docs/API-Endpoints.md` for every GET endpoint called.

## Build and test

```bash
go build -o fleet-plan ./cmd/fleet-plan
go test -race ./...
go vet ./...
golangci-lint run
```

Coverage target: >= 75% per package, enforced by `codecov.yml`. Current: 83.8%
overall, every package at or above 78.9%. All packages have `_test.go`. Tests use `testdata/` as a shared fleet-gitops fixture. Table-driven throughout.

## Key packages

| Package | Purpose |
| --- | --- |
| `internal/parser` | YAML parser for fleet-gitops repos (path traversal protected) |
| `internal/api` | Read-only Fleet REST client (GET only, HTTPS enforced) |
| `internal/diff` | Semantic diff engine with per-field change tracking |
| `internal/merge` | In-memory YAML merge for `--base` + `--env` |
| `internal/git` | CI platform detection (GitHub/GitLab), MR/PR comment posting |
| `internal/output` | Terminal (ANSI), JSON, Markdown renderers |

## Conventions

- Go 1.26+, table-driven tests, `google/deck` not used (lipgloss for terminal output)
- HTTPS enforced by default (`FLEET_PLAN_INSECURE=1` for local dev)
- Auth resolution: flags > env vars > config file (see `internal/config/`)
- No platform-specific code (pure Go, runs identically on all OSes)

## Agents

Two agents in `.claude/agents/`. Use when changes touch their domain:

| Agent | Trigger files |
| --- | --- |
| `diff-reviewer` | `internal/diff/`, `internal/parser/`, `internal/merge/`, match key or field comparison logic |
| `output-reviewer` | `internal/output/`, rendering logic, ANSI/markdown/JSON format changes |

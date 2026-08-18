# Contributing

fleet-plan is maintained by the CampusTech IT & Infrastructure team. Internal and
external contributions are both welcome.

## Development

```bash
git clone https://github.com/CampusTech/fleet-plan.git && cd fleet-plan
go build ./...
go vet ./...
go test -race ./...
golangci-lint run
```

Go 1.26+. CI runs the same commands plus `govulncheck ./...` and CodeQL. See [docs/Architecture.md](docs/Architecture.md) for data flow and package layout.

## Before opening a pull request

- `go build ./...`, `go vet ./...`, and `go test -race ./...` all pass.
- `golangci-lint run` reports no issues (`golangci-lint fmt` applies the gofmt /
  gofumpt formatting it expects).
- New or changed logic has table-driven tests. Coverage stays at or above 75%
  per package: `go test -coverprofile=coverage.txt ./... && ./scripts/coverage-floor.sh`
  (CI runs the same check).
- Commit messages use conventional prefixes (`feat:`, `fix:`, `test:`, `docs:`, `chore:`).

## Invariants that reviewers will enforce

These are not style preferences. A PR that breaks one will be rejected:

- **The Fleet API client is read-only.** `internal/api` issues `GET` only. No `POST`,
  `PUT`, `PATCH`, or `DELETE` — fleet-plan never mutates a Fleet instance.
- **HTTPS is enforced.** Plain HTTP is only reachable via `FLEET_PLAN_INSECURE=1` for
  local development.
- **No secrets in output, logs, or commits.** API tokens must never be printed, echoed
  into CI comments, or committed to fixtures.
- **No platform-specific code.** Pure Go, identical behavior on macOS, Linux, Windows.

## Reporting security issues

Do not open a public issue. Follow [SECURITY.md](SECURITY.md).

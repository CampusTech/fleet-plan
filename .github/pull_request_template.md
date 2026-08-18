## Summary

<!-- What changes and why. Link the issue if there is one. -->

## Changes

<!-- Per-package bullets: what moved, what was added. -->

## Test plan

- [ ] `go build ./...`
- [ ] `go vet ./...`
- [ ] `go test -race ./...`
- [ ] `gofmt -l .` prints nothing
- [ ] Coverage did not regress
- [ ] Validated against a live Fleet instance (describe below, redact tokens/host data)

## Invariants

- [ ] API client still issues `GET` only — no Fleet state is mutated
- [ ] HTTPS still enforced (`FLEET_PLAN_INSECURE=1` remains the only escape hatch)
- [ ] No secrets in code, logs, output, fixtures, or CI comments
- [ ] No platform-specific code

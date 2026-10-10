# Architecture

Single-binary Go CLI. Parses fleet-gitops YAML, fetches current state from Fleet API (GET only), computes semantic diff, renders output.

---

## Layout

```
cmd/fleet-plan/
  main.go               Cobra root command, flag wiring, runDiff entrypoint
  version.go            Version subcommand (set via ldflags)
  cmd_test.go           CLI flag and command tests
internal/
  api/client.go         Read-only Fleet REST client (GET only, HTTPS enforced)
  config/config.go      Auth resolution: flags > env vars > config file
  parser/parser.go      YAML parser for fleet-gitops repos (path traversal protected)
  diff/differ.go        Semantic diff engine with per-field change tracking
  merge/merge.go  In-memory YAML merge for --base + --env
  git/git.go          CI platform detection, changed-file resolution, MR/PR comment posting
  git/scope.go        Team inference from changed files
  output/
    terminal.go         ANSI-colored terminal renderer (truncation, diff context)
    json.go             JSON renderer
    markdown.go         Markdown renderer
  testutil/             Shared test helpers (TestdataRoot)
testdata/               Realistic fleet-gitops fixture repo for tests
assets/                 Logo, demo GIF, vhs-demo.go, demo.tape (see assets/README.md)
docs/                   Architecture and API endpoint docs
```

---

## Data flow

```mermaid
flowchart LR
    A[YAML files] -->|parser.ParseRepo| B[ParsedRepo]
    C[Fleet API] -->|api.FetchAll| D[FleetState]
    B --> E[diff.Diff]
    D --> E
    E --> F["[]DiffResult"]
    F --> G{--format}
    G -->|terminal| H[terminal.go]
    G -->|json| I[json.go]
    G -->|markdown| J[markdown.go]
    K[MR/PR API] -->|"git (--git)"| L[changed files]
    L -->|scope.go| M[affected teams]
    M --> E
    J -->|"git (--git)"| N[MR/PR comment]
```

---

## API client

`FetchAll` parallelizes all GET requests via `errgroup`. When `default.yml` has global sections, it also fetches `/config`, global policies, and global queries. HTTPS is enforced by default (`FLEET_PLAN_INSECURE=1` to override for local dev). Each request has a 30s timeout; client timeouts and 429/502/503/504 are retried up to 3 attempts with linear backoff (2s, 4s) so a scaled-to-zero Cloud Run backend can cold start. Other errors, and a cancelled context, fail immediately.

See [API Endpoints](API-Endpoints.md) for the full list.

---

## Auth resolution

Priority order (highest wins):

1. `--url` / `--token` flags
2. `FLEET_URL` / `FLEET_TOKEN` env vars
3. Config file: `<repo>/.config/fleet-plan.json` (checked first), then `~/.config/fleet-plan.json` (fallback)

Config file supports multiple contexts:

```json
{
  "contexts": { "dev": { "url": "...", "token": "..." } },
  "default_context": "dev"
}
```

---

## Parser

Walks `teams/*.yml`, resolves `path:` references, produces `ParsedRepo`. Also parses `default.yml` for labels, `org_settings`, `agent_options`, `controls`, and global policies/queries. A team's `settings:` block (or the older `team_settings:` spelling) and its `controls:` block are kept as nested maps for field-level diffing. An `agent_options: path:` reference, in a team file or `default.yml`, is replaced by the file it points to. All path references are validated against the repo root to prevent traversal.

---

## Diff engine

Compares `FleetState` (API) vs `ParsedRepo` (YAML). Produces `[]DiffResult` per team + a `(global)` result when `default.yml` is present.

Fleet's "hosts on no team" bucket is absent from `GET /teams`, so it is fetched separately (`team_id=0`) and diffed like any other team for policies, profiles, and scripts, baseline drift flagging included. Software and queries are reported as skipped there: Fleet exposes configured software only through the teams list, and scopes queries to a real team or the global scope. When the bucket was not fetched, the diff falls back to summarizing what the repo configures for it.

| Resource | Match key | Diff fields |
|----------|-----------|-------------|
| Config sections (global) | dot-path key | old/new value (skips `$VAR` placeholders) |
| Team `settings:` | dot-path key | old/new value vs the team object from `GET /teams`; `secrets:` is never diffed |
| Team `controls:` | dot-path key | old/new value vs the team's `mdm` object (`setup_experience` vs its legacy `macos_setup` names); for the no-team file, vs global `config.mdm`. Scripts and profiles are diffed separately |
| Team `agent_options:` | dot-path key | old/new value vs the team's `agent_options` |
| Policies | `name` | query, description, resolution, platform, critical, labels_include_any, labels_exclude_any |
| Queries | `name` | query, interval, platform, logging |
| Software packages | `referenced_yaml_path` | url, hash, self_service, categories |
| Fleet-maintained apps | `slug` | self_service, categories (skipped when the title detail is unreadable), scripts |
| App Store apps | `app_store_id` | self_service, categories |
| Profiles | PayloadDisplayName | changed payload key paths (names only, never values), labels_include_all, labels_include_any, labels_exclude_any |
| Scripts | filename | line count diff (`+N/-N`, `~N` for single-line) |
| Label definitions (global) | `name` | description, query, platform, label_membership_type (built-in and team-scoped labels skipped; only when `default.yml` defines labels; manual membership lists not compared) |
| Labels | `name` (cross-ref) | valid/missing with host counts |

Profile content is compared key by key. The profile list carries each stored profile's checksum, so a profile whose local file hashes to the same value is skipped without downloading anything; only the rest are fetched via `?alt=media`. Payload *values* are never rendered — profiles carry certificates, passwords, and enroll secrets, and the diff is posted to MRs. Keys whose local value references a Fleet variable (`$NAME` or `${NAME}`) are ignored, since Fleet substitutes them server-side and the stored value would never match. A value that merely contains a dollar sign is compared normally. Content for the profiles that do need comparing is downloaded in a single batch and reused by the baseline pass. Formats that cannot be flattened (Windows SyncML XML) fall back to the changed-file heuristic.

Label scoping lists (`labels_include_any` etc.) are compared as sets: order is ignored and an omitted list equals an empty one. Fleet returns them as `{id, name}` objects; the client keeps only the names.

Category names are normalized before comparison. Fleet reports them as display names with an emoji prefix (`🔐 Security`), while fleet-gitops YAML writes them plainly (`Security`); only leading symbols are stripped, so a category starting with a letter or digit (`1Password`) is untouched.

Configured keys the API does not report at all are not guessed at: they are listed in a "Not diffed" note (keys that are present but empty are still skipped silently). Two `org_settings` keys Fleet serves outside `/config` are fetched and merged in first: `certificate_authorities.ndes_scep_proxy` (from `/certificate_authorities`) and `mdm.end_user_license_agreement` (compared by file name, since Fleet stores the upload by name). Controls Fleet keeps on global config only (`windows_migration_enabled`, `apple_require_hardware_attestation`, ...) are flagged with a warning when a fleet file sets them, since Fleet accepts them there but ignores them. Numbers are compared in plain decimal: JSON decodes every number as float64, which `fmt` would print as `2.62144e+07`. In JSON values, a `teams` key is treated as the legacy spelling of `fleets` (Fleet 4.92 returns both on VPP tokens).

Whitespace is normalized before comparison to avoid false positives from YAML vs API newline differences. Per-field diffs are stored in `ResourceChange.Fields` for both added and modified resources.

---

## Output modes

| Mode | Flag | Description |
|------|------|-------------|
| Terminal (default) | `--format terminal` | ANSI-colored, smart truncation (80 chars), diff context around changes, capped at 3 fields per resource |
| Terminal verbose | `--verbose` | Full untruncated old/new values for all changed fields |
| JSON | `--format json` | Machine-readable, all fields |
| Markdown | `--format markdown` | For CI comments / MR descriptions |

---

## Demo GIF

`assets/vhs-demo.go` renders representative output from testdata for the README demo GIF. See [assets/README.md](https://github.com/CampusTech/fleet-plan/blob/main/assets/README.md) for prerequisites, setup, and regeneration steps.

---

## CI mode (`--git`)

When `--git` is active, the `git` package detects the CI platform and drives the diff workflow:

1. **Platform detection:** checks `CI_MERGE_REQUEST_IID` (GitLab CI) or `GITHUB_EVENT_NAME` (GitHub Actions) to determine which API to use for changed-file resolution and comment posting.
2. **Changed-file resolution** follows a fallback chain:
   - MR/PR API (preferred): fetch the file list from the GitLab merge request or GitHub pull request API.
   - `git diff`: if the API call fails or the env vars are missing, fall back to diffing against the merge base locally.
   - Full diff: if git is unavailable, diff all teams (no file filtering).
3. **Team scope inference:** `scope.go` maps changed file paths back to `teams/*.yml` entries so only affected teams are diffed.
4. **Baseline drift:** fleet-plan extracts (`git show`) the base-branch versions of the changed files, every in-scope team file, and `default.yml` (or `--base`) when global config is in scope, and diffs them against Fleet too. A change that already appears there was not introduced by the MR/PR: it was made outside gitops (UI/API) or merged but not yet deployed. It stays in the output, since applying the MR applies it too, but is flagged `Drift` and rendered as *not from this change* (`"drift": true` in JSON).
5. **Comment posting:** posts (or updates) a Markdown comment on the MR/PR. GitLab uses `FLEET_PLAN_BOT`, GitHub uses `GITHUB_TOKEN`.

---

## Config merge

`--base` + `--env` performs an in-memory YAML merge before diffing:

- The overlay (`--env`) keys win over the base (`--base`) keys.
- Maps are deep-merged: nested keys in the overlay are merged into the base map recursively.
- Arrays are replaced: an overlay array replaces the base array entirely (no element-level merge).

This mirrors how fleet-gitops environment overlays work. The merged result is written to a temp file that is cleaned up on exit, so no persistent files are left behind.

---

## Tests

```
go test -race ./...
```

All packages have `_test.go`. Tests use `testdata/` as a shared fleet-gitops fixture. Table-driven throughout. Coverage target: >= 75% per package, enforced in CI by `scripts/coverage-floor.sh` (current: 84.7% overall, lowest package 78.9%).

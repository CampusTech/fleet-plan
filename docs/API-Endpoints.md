# API Endpoints

All read-only. fleet-plan never writes to your Fleet server.

| Method | Endpoint | Purpose |
|--------|----------|---------|
| `GET` | `/api/v1/fleet/config` | Global config (org_settings, agent_options, controls) |
| `GET` | `/api/v1/fleet/teams` | Team list + embedded software config + team settings (`webhook_settings`, `host_expiry_settings`, `integrations`, `features`) |
| `GET` | `/api/v1/fleet/labels` | Label validation and host counts |
| `GET` | `/api/v1/fleet/teams/{id}/policies` | Per-team policies |
| `GET` | `/api/v1/fleet/global/policies` | Global policies (when default.yml parsed) |
| `GET` | `/api/v1/fleet/teams/0/policies` | Policies for hosts on no team (when the repo has a no-team file) |
| `GET` | `/api/v1/fleet/queries` | Per-team and global queries |
| `GET` | `/api/v1/fleet/configuration_profiles` | MDM configuration profiles (list includes each profile's checksum) |
| `GET` | `/api/v1/fleet/configuration_profiles/{uuid}?alt=media` | Profile content, for key-level diffing (only when the checksum differs) |
| `GET` | `/api/v1/fleet/software/titles` | Managed software titles (paginated) |
| `GET` | `/api/v1/fleet/software/fleet_maintained_apps` | Fleet-maintained app catalog (paginated) |
| `GET` | `/api/v1/fleet/scripts` | Team scripts for line-count diff (paginated) |
| `GET` | `/api/v1/fleet/scripts/{id}?alt=media` | Script content download |
| `GET` | `/api/v1/fleet/software/titles/{id}` | Software title detail: install/uninstall scripts and categories for Fleet-maintained apps. `/teams` can carry these, but returns an empty `fleet_maintained_apps` in practice, so the apps are inferred from software titles and enriched here. **A `gitops` role token gets 403 here**, in which case categories and scripts are reported as not diffed rather than compared against an unknown value |

Global endpoints (`/config`, `/global/policies`, `/queries` with teamID=0) are only called when `default.yml` defines global sections.

The "hosts on no team" bucket is fetched only when the repo has a no-team file (`teams/no-team.yml` or `fleets/unassigned.yml`). Its resources live behind `team_id=0` on `/configuration_profiles` and `/scripts`, and behind `/teams/0/policies` for policies — note that `/global/policies` is a *different* set. Fleet does not report configured software for this bucket, so software is not diffed there.

HTTPS enforced unless `FLEET_PLAN_INSECURE=1`.

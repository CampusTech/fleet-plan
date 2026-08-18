# API Endpoints

All read-only. fleet-plan never writes to your Fleet server.

| Method | Endpoint | Purpose |
|--------|----------|---------|
| `GET` | `/api/v1/fleet/config` | Global config (org_settings, agent_options, controls) |
| `GET` | `/api/v1/fleet/teams` | Team list + embedded software config |
| `GET` | `/api/v1/fleet/labels` | Label validation and host counts |
| `GET` | `/api/v1/fleet/teams/{id}/policies` | Per-team policies |
| `GET` | `/api/v1/fleet/global/policies` | Global policies (when default.yml parsed) |
| `GET` | `/api/v1/fleet/queries` | Per-team and global queries |
| `GET` | `/api/v1/fleet/configuration_profiles` | MDM configuration profiles (list includes each profile's checksum) |
| `GET` | `/api/v1/fleet/configuration_profiles/{uuid}?alt=media` | Profile content, for key-level diffing (only when the checksum differs) |
| `GET` | `/api/v1/fleet/software/titles` | Managed software titles (paginated) |
| `GET` | `/api/v1/fleet/software/fleet_maintained_apps` | Fleet-maintained app catalog (paginated) |
| `GET` | `/api/v1/fleet/scripts` | Team scripts for line-count diff (paginated) |
| `GET` | `/api/v1/fleet/scripts/{id}?alt=media` | Script content download |
| `GET` | `/api/v1/fleet/software/titles/{id}` | Software title detail |

Global endpoints (`/config`, `/global/policies`, `/queries` with teamID=0) are only called when `default.yml` defines global sections.

HTTPS enforced unless `FLEET_PLAN_INSECURE=1`.

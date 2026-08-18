# Security Policy

## Reporting a vulnerability

**Do not open a public GitHub issue for a security vulnerability.**

Report it privately, by either route:

1. **GitHub private vulnerability reporting** — the *Report a vulnerability* button under
   this repository's **Security** tab. Preferred for external reporters.
2. **CampusTech internal security team** — CampusTech employees should file through the
   internal security intake in the IT service catalog. If you do not know the current
   intake path, ask the IT & Infrastructure team; do not route reports through
   unmanaged channels such as personal email or public chat.

Please include: affected version or commit, reproduction steps, expected versus actual
behavior, and impact. Do not include live credentials, Fleet API tokens, host data, or
any student or personnel records in the report — describe the class of data exposed
instead of attaching it.

Expect an acknowledgement within 3 business days and a remediation plan or triage
decision within 10 business days.

## Handling requirements for reporters

CampusTech operates under GLBA / FTC Safeguards Rule, FERPA, and NIST 800-171 Rev. 3.
Details of an unfixed vulnerability, and any data obtained while investigating one, are
confidential until a fix ships. Do not share them outside the reporting channels above.
If your report involves data that may be FERPA-protected, CUI, or FTI, say so in the
report so it is routed for the appropriate handling.

## Scope

fleet-plan is a read-only CLI. It authenticates to a Fleet server with an API token and
issues `GET` requests only. Reports that are in scope include, but are not limited to:

- Any path where fleet-plan issues a non-`GET` request to Fleet, or otherwise mutates state.
- Leakage of the Fleet API token into terminal output, JSON or Markdown renderings, CI
  logs, or MR/PR comments.
- Bypass of the HTTPS enforcement in `internal/api` outside the documented
  `FLEET_PLAN_INSECURE=1` development escape hatch.
- Path traversal in the fleet-gitops YAML parser (`internal/parser`) escaping the repo root.
- Dependency vulnerabilities reachable from fleet-plan's code paths.

Out of scope: vulnerabilities in Fleet itself (report to
[fleetdm/fleet](https://github.com/fleetdm/fleet/security)), and findings that require a
compromised local machine or an operator-supplied malicious `FLEET_PLAN_*` environment.

## Supported versions

The latest released version is supported. Fixes ship in a new release rather than as
patches to older tags.

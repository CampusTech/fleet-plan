#!/usr/bin/env bash
# Fail when total or per-package statement coverage drops below the floor.
#
# The codecov project status is configured in codecov.yml but has never posted
# on this repo, so the documented ">= 75% per package" claim was unenforced.
# This runs from the coverage profile CI already produces, and is also runnable
# locally: scripts/coverage-floor.sh coverage.txt [floor]
set -euo pipefail

profile="${1:-coverage.txt}"
floor="${2:-75}"

if [[ ! -f "$profile" ]]; then
  echo "coverage profile not found: $profile" >&2
  exit 1
fi

fail=0

# Per-package coverage: sum covered vs total statements per package directory.
while read -r pkg covered total; do
  pct=$(awk -v c="$covered" -v t="$total" 'BEGIN { printf "%.1f", (t == 0 ? 100 : 100 * c / t) }')
  status="ok"
  if awk -v p="$pct" -v f="$floor" 'BEGIN { exit !(p < f) }'; then
    status="BELOW FLOOR"
    fail=1
  fi
  printf '%-55s %6s%%  %s\n' "$pkg" "$pct" "$status"
done < <(
  awk '
    NR > 1 {
      split($1, loc, ":")
      path = loc[1]
      # Strip the file name, leaving the package path.
      n = split(path, parts, "/")
      path = parts[1]
      for (i = 2; i < n; i++) path = path "/" parts[i]
      stmts[path] += $2
      if ($3 > 0) covered[path] += $2
    }
    END { for (p in stmts) print p, covered[p] + 0, stmts[p] }
  ' "$profile" | sort
)

# Total from the profile directly rather than `go tool cover -func`, which
# needs the packages to be resolvable from the current module.
total_pct=$(awk '
  NR > 1 { stmts += $2; if ($3 > 0) covered += $2 }
  END { printf "%.1f", (stmts == 0 ? 100 : 100 * covered / stmts) }
' "$profile")
printf '%-55s %6s%%\n' "TOTAL" "$total_pct"
if awk -v p="$total_pct" -v f="$floor" 'BEGIN { exit !(p < f) }'; then
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo "coverage below the ${floor}% floor" >&2
  exit 1
fi

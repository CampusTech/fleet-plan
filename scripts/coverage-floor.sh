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

# Validate the floor before it reaches awk, where a non-numeric value would be
# coerced to 0 and silently pass everything.
if ! [[ "$floor" =~ ^[0-9]+(\.[0-9]+)?$ ]] || awk -v f="$floor" 'BEGIN { exit !(f > 100) }'; then
  echo "floor must be a number between 0 and 100, got: $floor" >&2
  exit 1
fi

fail=0

# Per-package coverage: sum covered vs total statements per package directory.
while read -r pkg covered total; do
  # Compare the unrounded value: 74.96% displays as 75.0% but is below a 75%
  # floor and must fail.
  raw=$(awk -v c="$covered" -v t="$total" 'BEGIN { printf "%.10f", (t == 0 ? 100 : 100 * c / t) }')
  status="ok"
  if awk -v p="$raw" -v f="$floor" 'BEGIN { exit !(p < f) }'; then
    status="BELOW FLOOR"
    fail=1
  fi
  printf '%-55s %6.1f%%  %s\n' "$pkg" "$raw" "$status"
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
total_raw=$(awk '
  NR > 1 { stmts += $2; if ($3 > 0) covered += $2 }
  END { printf "%.10f", (stmts == 0 ? 100 : 100 * covered / stmts) }
' "$profile")
printf '%-55s %6.1f%%\n' "TOTAL" "$total_raw"
if awk -v p="$total_raw" -v f="$floor" 'BEGIN { exit !(p < f) }'; then
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo "coverage below the ${floor}% floor" >&2
  exit 1
fi

#!/usr/bin/env bash
# Turn TestProductionFileSize advisory lines into GitHub warning annotations.
set -uo pipefail

output="$(go test -run TestProductionFileSize -v . 2>&1)"

printf '%s\n' "$output"

while IFS= read -r line; do
  case "$line" in
    *ADVISORY:*)
      message="${line#*ADVISORY:}"
      message="${message#"${message%%[![:space:]]*}"}"
      printf '::warning::%s\n' "$message"
      ;;
  esac
done <<< "$output"

# Advisory output is informational, so the job always succeeds.
exit 0

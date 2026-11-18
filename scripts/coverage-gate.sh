#!/bin/sh
# coverage-gate.sh — per-package coverage gate for eka-core.
#
# Runs the test suite once with coverage and enforces scripts/coverage.floor.
#
# Why per package: a module-wide total is dominated by whichever package has
# the most statements, so a collapse in a small, high-risk package disappears
# inside it. Comparing each package against its own floor makes that collapse
# the thing that fails.
#
# Usage:
#   ./scripts/coverage-gate.sh          # gate (exit 1 on test failure or regression)
#   ./scripts/coverage-gate.sh --report # print the table, do not fail on floors
set -eu

cd "$(dirname "$0")/.."
floors="scripts/coverage.floor"
report_only=0
[ "${1:-}" = "--report" ] && report_only=1

out="$(mktemp)"
rows="$(mktemp)"
trap 'rm -f "$out" "$rows"' EXIT

# The race detector stays on: it is the existing CI configuration, and coverage
# measured without it would not describe the run that actually gates a merge.
status=0
go test -race -cover -count=1 ./... >"$out" 2>&1 || status=$?

# Print the raw run so a compile error or a failing test is readable in the log.
cat "$out"

if [ "$status" -ne 0 ]; then
    echo ""
    echo "coverage-gate: test suite failed (exit $status)"
    exit "$status"
fi

# "package percent" rows from the ok lines. Packages with no test files carry
# no coverage figure and are skipped.
sed -n 's|^ok[[:space:]]\{1,\}github.com/maleolabs/eka-core/\([^[:space:]]*\)[[:space:]].*coverage: \([0-9.]*\)%.*|\1 \2|p' "$out" > "$rows"

if [ ! -s "$rows" ]; then
    echo "coverage-gate: no coverage data found in the test output"
    exit 1
fi

printf '\n%-16s %8s %8s\n' PACKAGE COVER FLOOR
printf '%s\n' "-----------------------------------"

failed=0
while read -r pkg pct; do
    floor="$(awk -v p="$pkg" '$1==p {print $2}' "$floors")"
    if [ -z "$floor" ]; then
        printf '%-16s %7s%% %8s\n' "$pkg" "$pct" "MISSING"
        echo "coverage-gate: package $pkg has no floor in $floors; add one so a regression here is visible"
        exit 1
    fi
    if awk -v a="$pct" -v b="$floor" 'BEGIN {exit (a < b) ? 0 : 1}'; then
        printf '%-16s %7s%% %7s%%  BELOW\n' "$pkg" "$pct" "$floor"
        failed=1
    else
        printf '%-16s %7s%% %7s%%\n' "$pkg" "$pct" "$floor"
    fi
done < "$rows"

if [ "$failed" -eq 1 ] && [ "$report_only" -eq 0 ]; then
    echo ""
    echo "coverage-gate: a package dropped below its floor"
    exit 1
fi

echo ""
echo "coverage-gate: ok"
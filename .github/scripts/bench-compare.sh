#!/usr/bin/env bash
# Compares benchmarks between a git ref and the working tree, and fails on a
# performance regression. Used by `just bench-compare` and the bench workflow.
#
# Usage: bench-compare.sh [ref] [rounds]
set -euo pipefail

ref="${1:-main}"
rounds="${2:-10}"
benchstat="golang.org/x/perf/cmd/benchstat@v0.0.0-20260929162123-406019bb8b68"
bench=(go test ./emf -run '^$' -bench . -benchtime 200ms -count 1)
here="$(cd "$(dirname "$0")" && pwd)"

tmp="$(mktemp -d)"
cleanup() {
	git worktree remove --force "$tmp/base" >/dev/null 2>&1 || true
	rm -rf "$tmp"
}
trap cleanup EXIT

git worktree add --quiet --detach "$tmp/base" "$ref"

# Alternate base and head runs so machine noise affects both equally.
for i in $(seq "$rounds"); do
	echo "round $i of $rounds" >&2
	(cd "$tmp/base" && "${bench[@]}") >>"$tmp/base.txt"
	"${bench[@]}" >>"$tmp/head.txt"
done

if ! grep -q '^Benchmark' "$tmp/base.txt"; then
	echo "No benchmarks on $ref yet, so there is nothing to compare." >&2
	exit 0
fi

report="$(go run "$benchstat" base="$tmp/base.txt" head="$tmp/head.txt")"
echo "$report"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
	printf '## Benchmarks: %s vs this PR\n\n```\n%s\n```\n' "$ref" "$report" >>"$GITHUB_STEP_SUMMARY"
fi

go run "$benchstat" -format csv base="$tmp/base.txt" head="$tmp/head.txt" | python3 "$here/benchcheck.py"

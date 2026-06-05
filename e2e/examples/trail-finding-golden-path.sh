#!/usr/bin/env bash
set -euo pipefail

# Golden path for manual trail-finding CLI verification.
# Run from a git repo with an origin remote and an authenticated Entire CLI.

ENTIRE_BIN="${ENTIRE_BIN:-entire}"
TARGET_SELECTOR="${ENTIRE_TRAIL_FINDING_SELECTOR:-${ENTIRE_TRAIL_REVIEW_SELECTOR:-}}"
FINDING_FILE="${ENTIRE_TRAIL_FINDING_FILE:-${ENTIRE_TRAIL_REVIEW_FILE:-README.md}}"
FINDING_LINE="${ENTIRE_TRAIL_FINDING_LINE:-${ENTIRE_TRAIL_REVIEW_LINE:-1}}"
CLIENT_ID="trail-finding-e2e:$(date +%s):$$"

target_args=()
if [[ -n "$TARGET_SELECTOR" ]]; then
  target_args+=("$TARGET_SELECTOR")
fi

run() {
  printf '\n$ %q' "$ENTIRE_BIN"
  printf ' %q' "$@"
  printf '\n'
  "$ENTIRE_BIN" "$@"
}

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

need "$ENTIRE_BIN"
need jq

echo "Trail finding golden path"
echo "Target: ${TARGET_SELECTOR:-current branch trail}"
echo "Finding location: ${FINDING_FILE}:${FINDING_LINE}"

run trail list --status any --limit 10
run trail finding "${target_args[@]}"

printf '\n$ %q' "$ENTIRE_BIN"
printf ' %q' trail finding add "${target_args[@]}" \
  --json \
  --title "E2E finding" \
  --body "Golden-path finding created by the trail finding E2E example." \
  --severity medium \
  --confidence 0.9 \
  --file "$FINDING_FILE" \
  --line "$FINDING_LINE" \
  --client-id "$CLIENT_ID"
printf '\n'

finding_json=$("$ENTIRE_BIN" trail finding add "${target_args[@]}" \
  --json \
  --title "E2E finding" \
  --body "Golden-path finding created by the trail finding E2E example." \
  --severity medium \
  --confidence 0.9 \
  --file "$FINDING_FILE" \
  --line "$FINDING_LINE" \
  --client-id "$CLIENT_ID")

printf '%s\n' "$finding_json" | jq .
finding_id=$(printf '%s\n' "$finding_json" | jq -r '.id')
if [[ -z "$finding_id" || "$finding_id" == "null" ]]; then
  echo "create finding response did not include .id" >&2
  exit 1
fi

run trail finding list "${target_args[@]}" --status any --include-dismissed
run trail finding show "${target_args[@]}" "$finding_id"
run trail finding resolve "${target_args[@]}" "$finding_id" -m "resolved by trail finding golden-path e2e example"
run trail finding list "${target_args[@]}" --status any --include-dismissed

echo
printf 'Golden path complete. Created and resolved finding: %s\n' "$finding_id"

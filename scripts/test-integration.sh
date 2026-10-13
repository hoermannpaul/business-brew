#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
command -v docker >/dev/null || { echo "Docker is required for the integration suite." >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "Start Docker before running the integration suite." >&2; exit 1; }
docker compose version >/dev/null

export COMPOSE_PROJECT_NAME="business-brew-test-$(date +%s)-$$-${RANDOM}"
export INTEGRATION_ARTIFACTS_DIR="$repo_root/test-results/integration/$COMPOSE_PROJECT_NAME"
mkdir -p "$INTEGRATION_ARTIFACTS_DIR"
compose=(docker compose --project-name "$COMPOSE_PROJECT_NAME" --file "$repo_root/tests/integration/compose.yaml")

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  "${compose[@]}" logs --no-color >"$INTEGRATION_ARTIFACTS_DIR/containers.log" 2>&1 || true
  "${compose[@]}" down --volumes --remove-orphans --timeout 10 || true
  # These image tags belong only to this invocation.
  docker image rm "$COMPOSE_PROJECT_NAME-server" "$COMPOSE_PROJECT_NAME-browser" >/dev/null 2>&1 || true
  echo "Integration results: $INTEGRATION_ARTIFACTS_DIR"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

"${compose[@]}" build server browser
printf '%s\n' 'integration-only-password' | "${compose[@]}" run --rm --no-deps -T bootstrap
"${compose[@]}" up --detach server
"${compose[@]}" run --rm --no-deps -T browser

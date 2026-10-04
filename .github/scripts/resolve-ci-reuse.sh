#!/usr/bin/env bash
set -euo pipefail
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}" "${GITHUB_SHA:?GITHUB_SHA is required}" "${GITHUB_RUN_ID:?GITHUB_RUN_ID is required}"
required=(frontend-dist native-windows-amd64-desktop native-windows-arm64-desktop native-darwin-arm64-desktop native-darwin-amd64-desktop native-linux-amd64-desktop native-linux-arm64-desktop native-linux-amd64-server native-linux-arm64-server)
run_id="$(gh api "repos/${GITHUB_REPOSITORY}/actions/runs?head_sha=${GITHUB_SHA}&per_page=50" --jq '[.workflow_runs[] | select(.name == "CI" and .path == ".github/workflows/ci.yml" and .status == "completed" and .conclusion == "success")] | sort_by(.created_at) | last | .id // empty')"
if [ -n "$run_id" ]; then
  artifacts="$(gh api "repos/${GITHUB_REPOSITORY}/actions/runs/${run_id}/artifacts?per_page=100" --jq '[.artifacts[].name]')"
  missing=()
  for name in "${required[@]}"; do
    jq -e --arg name "$name" 'index($name) != null' <<<"$artifacts" >/dev/null || missing+=("$name")
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    echo "green CI run ${run_id} is missing reusable artifacts: ${missing[*]}" >&2
    run_id=""
  fi
fi
if [ -n "$run_id" ]; then
  echo "reusing green CI run ${run_id} for ${GITHUB_SHA}" >&2
  echo "artifact-run-id=${run_id}"
  echo "reused=true"
else
  echo "no reusable green CI run for ${GITHUB_SHA}; running the full CI" >&2
  echo "artifact-run-id=${GITHUB_RUN_ID}"
  echo "reused=false"
fi

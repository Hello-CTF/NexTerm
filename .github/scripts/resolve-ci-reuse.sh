#!/usr/bin/env bash
set -euo pipefail
: "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}" "${GITHUB_SHA:?GITHUB_SHA is required}" "${GITHUB_RUN_ID:?GITHUB_RUN_ID is required}"
required=(frontend-dist native-windows-amd64-desktop native-windows-arm64-desktop native-darwin-arm64-desktop native-darwin-amd64-desktop native-linux-amd64-desktop native-linux-arm64-desktop native-linux-amd64-server native-linux-arm64-server)
WAIT_TIMEOUT="${CI_REUSE_WAIT_TIMEOUT:-1500}"
POLL_INTERVAL="${CI_REUSE_POLL_INTERVAL:-20}"

list_runs() {
  gh api "repos/${GITHUB_REPOSITORY}/actions/runs?head_sha=${GITHUB_SHA}&per_page=50"
}

# CI_REUSE_EXPECTED_REF 锁定复用来源(如发布 tag): 同 SHA 可能有 master push 与 tag push
# 两次 CI run, 只有 ref 匹配的 run 的产物版本与本次发布一致。
green_run() {
  jq -r --arg ref "${CI_REUSE_EXPECTED_REF:-}" '[.workflow_runs[] | select(.name == "CI" and .path == ".github/workflows/ci.yml" and .status == "completed" and .conclusion == "success" and ($ref == "" or .head_branch == $ref))] | sort_by(.created_at) | last | .id // empty'
}

in_progress_run() {
  jq -r --arg ref "${CI_REUSE_EXPECTED_REF:-}" '[.workflow_runs[] | select(.name == "CI" and .path == ".github/workflows/ci.yml" and (.status == "queued" or .status == "in_progress") and ($ref == "" or .head_branch == $ref))] | sort_by(.created_at) | last | .id // empty'
}

wait_for_green() {
  local pending_id="$1" deadline="$2" runs status conclusion
  while [ "$(date +%s)" -lt "$deadline" ]; do
    sleep "$POLL_INTERVAL"
    runs="$(list_runs)"
    local green
    green="$(green_run <<<"$runs")"
    if [ -n "$green" ]; then
      printf '%s' "$green"
      return 0
    fi
    status="$(jq -r --arg id "$pending_id" '[.workflow_runs[] | select(.id | tostring == $id)] | last | .status // empty' <<<"$runs")"
    if [ "$status" = "completed" ]; then
      conclusion="$(jq -r --arg id "$pending_id" '[.workflow_runs[] | select(.id | tostring == $id)] | last | .conclusion // empty' <<<"$runs")"
      echo "waited CI run ${pending_id} completed with conclusion ${conclusion}; running the full CI" >&2
      return 1
    fi
    if [ -z "$status" ]; then
      echo "waited CI run ${pending_id} disappeared; running the full CI" >&2
      return 1
    fi
    echo "waiting for CI run ${pending_id} (status ${status})" >&2
  done
  echo "timed out after ${WAIT_TIMEOUT}s waiting for CI run ${pending_id}; running the full CI" >&2
  return 1
}

runs="$(list_runs)"
run_id="$(green_run <<<"$runs")"
if [ -z "$run_id" ]; then
  pending_id="$(in_progress_run <<<"$runs")"
  if [ -n "$pending_id" ]; then
    echo "CI run ${pending_id} for ${GITHUB_SHA} is still in progress; waiting up to ${WAIT_TIMEOUT}s before falling back to the full CI" >&2
    run_id="$(wait_for_green "$pending_id" "$(( $(date +%s) + WAIT_TIMEOUT ))" || true)"
  fi
fi
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

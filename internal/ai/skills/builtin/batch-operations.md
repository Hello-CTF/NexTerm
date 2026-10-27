---
name: batch-operations
description: Run the same change across several hosts safely: pick targets, canary first, verify, then widen. Use for fleet-wide or multi-host commands.
---
# Safe batch operations across hosts

Use this when a task touches more than one host with `exec_commands` (using `asset_ids` or `tags`).

## Plan

1. Use `list_assets` and `terminal_list` to establish exactly which hosts exist and which are already connected. `exec_commands` only targets sessions that are already connected; it does not connect for you.
2. State the target list to the user before acting: host names, environment tags, and the exact command. Treat any host without a clear non-production tag as production.
3. Prefer one command per step that is read-only first (version, status, disk space, config test) so the user can see the baseline.

## Execute

1. Canary: run the change on a single, least critical host first.
2. Verify the canary with a separate read-only command and compare with the baseline. Stop if anything differs from what you expected.
3. Widen in small groups rather than all at once. Production targets require the user's confirmation; do not try to work around it, and do not split a command to avoid it.
4. Keep commands idempotent and single-line. Avoid `rm -rf`, `reboot`, service restarts of shared infrastructure, and package upgrades unless the user asked for exactly that.

## Report

- After each group, summarise per-host results: success, non-zero exit, timeout, transport error. Name the hosts that failed and show the relevant output.
- Do not retry failures automatically on production. Propose the next step and wait.

---
name: batch-operations
description: Run the same change across several hosts safely: pick targets, canary first, verify, then widen. Use for fleet-wide or multi-host commands.
---
# Safe batch operations across hosts

Use this when a task touches more than one host with `exec_commands` (using `asset_ids` or `tags`).

Do not use it for a single host (run the command there directly), for read-only checks on several hosts (run them without a canary), or for interactive programs (use `screen-confirm` on that terminal instead).

## Plan

1. Use `list_assets` and `terminal_list` to establish exactly which hosts exist and which are already connected. `exec_commands` only targets sessions that are already connected; it does not connect for you.
2. Choose targets carefully. `tags` must all match; `asset_ids` together with `tags` means their intersection, not a union. Managed devices can only be named in `asset_ids` (never selected by `tags`). A call targets at most 64 hosts, so split a larger fleet into groups.
3. State the resolved target list to the user before acting: host names, environment tags, and the exact command. Check that it is the list you described, with no extra or missing hosts. Treat any host without a clear non-production tag as production. Managed devices have no environment tag, so they are always production and each one needs the user's typed confirmation.
4. Run a read-only command first (version, status, disk space, config test) to record the baseline the user can compare against.
5. Before a change that is hard to undo, make recovery possible: back up the file you will edit or note the current version, and tell the user how to revert.

## Execute

1. Canary: run the change on a single, least critical host first.
2. Verify the canary with a separate read-only command and compare with the baseline. If anything differs from what you expected, or the canary fails, stop. Do not widen, and tell the user how to revert it.
3. Widen in small groups rather than all at once. Production targets require the user's confirmation; do not try to work around it, and do not split a command to avoid it.
4. Keep commands idempotent and single-line. Avoid `rm -rf`, `reboot`, service restarts of shared infrastructure, and package upgrades unless the user asked for exactly that.

## Report

- After each group, summarise per-host results: success, non-zero exit, timeout, transport error. Name the hosts that failed and show the relevant output.
- Do not retry failures automatically on production. Propose the next step and wait.
- If a group has failures, pause before the next group and ask the user whether to continue.

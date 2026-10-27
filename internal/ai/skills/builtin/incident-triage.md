---
name: incident-triage
description: Read-only first-pass triage of an unhealthy Linux host: load, memory, disk, failing units, recent errors. Use when a service or server misbehaves and the cause is unknown.
---
# Read-only incident triage

Use this when the user reports a slow, failing or unreachable service and the cause is not yet known. Everything here is read-only; do not change state until the user agrees to a specific fix.

## Steps

Run these with `exec_commands` on the affected host (one short command each) and read the results before moving on:

1. Identity and uptime: `hostname; uptime`
2. CPU and memory pressure: `free -m; ps aux --sort=-%cpu | head -n 8`
3. Disk space and inodes: `df -h; df -i`
4. Failed units: `systemctl --failed --no-pager`
5. Recent errors: `journalctl -p err -n 50 --no-pager`
6. Network listeners relevant to the service: `ss -tlnp`

Skip a step that does not apply (no systemd, no journal) and say so instead of guessing.

## Interpret

- Correlate findings: a full disk, an out-of-memory kill, or a crash-looping unit usually explains several symptoms at once.
- Separate facts (what a command printed) from hypotheses (what you think it means). Label each.
- Quote the specific lines that support your conclusion.

## Propose

End with a short ranked list of likely causes and, for each, the next read-only check or the smallest fix. Wait for the user before running anything that restarts services, deletes files or changes configuration. On production hosts, confirmation is required anyway.

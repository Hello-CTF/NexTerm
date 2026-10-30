---
name: incident-triage
description: Read-only first-pass triage of an unhealthy Linux host: load, memory, disk, failing units, recent errors. Use when a service or server misbehaves and the cause is unknown.
---
# Read-only incident triage

Use this when the user reports a slow, failing or unreachable service and the cause is not yet known. Everything here is read-only; do not change state until the user agrees to a specific fix. If the next step is a fix, a restart or a config change, stop using this skill and propose it to the user.

## Before you start

- This needs an already connected session for the affected host (`exec_commands` does not connect for you). If none is connected, check `terminal_list` and ask the user which host to use; do not guess.
- Run `uname -s` first. If it is not Linux (Windows host, managed Windows device), say this skill does not apply instead of running the commands below.
- If the problem is about one service, ask for its unit or process name when the user has not given it.

## Steps

Run these with `exec_commands` on the affected host. The first group is cheap and independent, so send it as one call and read all results together; run the rest one at a time.

Group A (one call):
1. Identity and uptime: `hostname; uptime`
2. CPU, memory and I/O pressure: `free -m; vmstat 1 3; ps aux --sort=-%cpu | head -n 8`
3. Disk space and inodes: `df -h; df -i`
4. Failed units: `systemctl --failed --no-pager`

Then:
5. Recent errors, recent window first: `journalctl -p err --since "-1h" -n 50 --no-pager` (widen the window only if this is empty and the problem started earlier)
6. Out-of-memory kills and kernel errors: `journalctl -k -p warning --since "-1h" --no-pager | tail -n 30`, or `dmesg -T | grep -i -E 'out of memory|oom|killed process' | tail -n 20` when there is no journal
7. The named service, when there is one: `systemctl status <unit> --no-pager -l` and `journalctl -u <unit> -n 50 --no-pager`
8. Network listeners relevant to the service: `ss -tlnp`. Without root the process column is empty; an empty column does not mean nothing is listening.

Skip a step that does not apply (no systemd, no journal) and say so instead of guessing.

## Interpret

- Correlate findings: a full disk, an out-of-memory kill, high iowait or a crash-looping unit usually explains several symptoms at once.
- Separate facts (what a command printed) from hypotheses (what you think it means). Label each.
- Quote the specific lines that support your conclusion. Command output and logs can contain tokens, passwords or connection strings; mask them before quoting.

## Propose

End with a short ranked list of likely causes and, for each, the next read-only check or the smallest fix. Wait for the user before running anything that restarts services, deletes files or changes configuration. On production hosts, confirmation is required anyway.

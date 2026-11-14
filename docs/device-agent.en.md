# Device enrollment

**English** | [中文](device-agent.md) · [Back to README](../README_EN.md)

The device agent lets the server manage a device: the device connects outward to the server, reports CPU, memory, and disk metrics, and provides terminal and file access.

## Supported platforms

Releases ship the device agent for Linux (amd64 / arm64), macOS (amd64 / arm64), and Windows (amd64 / arm64).

## Enrollment

1. A super-admin sets the enrollment address in Devices (设备管理) and issues a one-time enrollment code.
2. Run the one-line command shown there on the device: as a regular user on Linux/macOS (no root needed), in PowerShell on Windows. The script installs the download only after checking it against the same release's `SHA256SUMS`.
3. The agent starts automatically at user login (a systemd user service on Linux, launchd on macOS, a logon scheduled task on Windows).

## Shell and environment

The agent resolves the account's login shell and its environment at runtime (through an interactive login shell, falling back to a login shell) and does not record the installing process's `SHELL` or `PATH` in the service definition. Pass `--shell <absolute path>` at install time to choose the shell terminals use, or `--shell auto` to clear it and use the account's login shell. `nexterm-agent status` shows the device, service and persistent terminal component state, and `--verbose` adds the shell in use and the resolved environment; older installs recorded `SHELL`/`PATH` in the service definition, and re-running the install command clears them.

## Self-upgrade

The agent verifies the Minisign signature before it self-upgrades, and development builds without an embedded signing key refuse to self-upgrade.

A self-upgrade starts in one of two ways: the server reports an incompatible protocol version and the agent upgrades to the server's version, or you run `nexterm-agent update` on the device. Downloads come only from the official release. A download that receives no data for 60 seconds fails and is retried; a slow but steady download is not cut off by its total duration (capped at 2 hours).

Before the swap, the agent runs the downloaded program (`nexterm-agent version`, within 10 seconds) and requires it to report the target version; otherwise nothing is replaced. The replace script waits for the old process to exit, swaps the new program in, keeps the previous one as `nexterm-agent.nexterm-old` in the same directory, restarts the service, and waits for the confirmation marker (`nexterm-agent.nexterm-healthy`) that the new version writes after its first control-channel connection to the server:

- Confirmed within 120 seconds: the previous program is deleted and the upgrade is complete.
- Not confirmed: the previous program is restored and restarted, and `nexterm-agent.nexterm-failed` in the same directory records the failed version. The restored agent reports the rollback to the server, and Devices shows it as a background service error. When the server asks for that version again, the agent stays running and keeps syncing, waits 1 hour after the first failure (doubling with each further failure up to 24 hours), and then retries the upgrade by itself. Run `nexterm-agent update --version <version>` to retry sooner.
- When no service is installed or the restart command fails (for example an agent started by hand), or the Windows scheduled task is still running so no new instance can be confirmed, there is no health check and the new program is kept.
- If the machine reboots while the replace script is waiting for confirmation, that wait is lost and no automatic rollback happens; the new program simply stays, and the previous program is deleted once the new agent confirms itself.

The replace and restart outcomes are recorded in `upgrade.log` in the data directory.

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

The agent resolves the account's login shell and its environment at runtime (through an interactive login shell, falling back to a login shell) and does not record the installing process's `SHELL` or `PATH` in the service definition. Pass `--shell <absolute path>` at install time to choose the shell terminals use, or `--shell auto` to clear it and use the account's login shell. `nexterm-agent status` shows the shell in use and the resolved environment; older installs recorded `SHELL`/`PATH` in the service definition, and re-running the install command clears them.

## Self-upgrade

The agent verifies the Minisign signature before it self-upgrades, and development builds without an embedded signing key refuse to self-upgrade.

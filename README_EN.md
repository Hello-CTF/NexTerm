<div align="center">

# NexTerm

**SSH, WinRM, files, Docker, databases, and AI in one workbench.**

[![Website](https://img.shields.io/badge/website-online-516cd6)](https://nexterm.work/)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8)
![Wails](https://img.shields.io/badge/Wails-v3-CC0000)
![React](https://img.shields.io/badge/React-19-61DAFB)
![License](https://img.shields.io/badge/license-MIT-green)

**English** | [中文](README.md)

</div>

## Features

- **Terminals and files**: SSH, WinRM, and local terminals with multi-tab, split panes, SFTP file management, and in-place editing. SSH terminals are daemonized: closing a tab or disconnecting moves them to "background sessions" that keep running and can be re-attached at any time; local, WinRM non-interactive, and container exec terminals end when closed.
- **Terminal history**: every terminal session is recorded automatically (output, keystrokes, and window resizes), viewable as text, searchable, and replayable on a timeline. Keystrokes are recorded verbatim - passwords and tokens typed into the terminal are captured too. Records stay local by default; end-to-end encrypted sync can be enabled per record, and records can be deleted individually.
- **Daily ops**: manage Docker containers and images; use PostgreSQL, MySQL, Redis, and SSH port forwarding. PostgreSQL and plain SSH/SFTP need no remote agent; the agent is only for device management.
- **AI assistant**: works with OpenAI-compatible endpoints; sees command output and file changes; sensitive operations require confirmation; supports terminal takeover and scheduled tasks.
- **Multi-user accounts**: the server has a built-in account system; after super-admin initialization it can create users or open registration. Sync data is isolated per account and end-to-end encrypted.
- **Device management and sharing**: install the device agent on a host to add it to the device list and open remote terminals on it; hosts can be shared with other registered users or via public links (read-only by default), never exposing host passwords or private keys.
- **Desktop and browser**: the desktop app is for local use; on the server, terminal processes and workspaces live on the server and keep running after the page is closed.
- **Asset sync**: after signing in on the desktop app, assets, groups, snippets, and credentials sync across devices with end-to-end encryption - no repeated setup.

## Download and Install

Download the latest version from [Releases](https://github.com/Hello-CTF/NexTerm/releases/latest), or install it from the Lazycat app center.

| Platform | Package |
| --- | --- |
| Windows 10/11 (x64 / ARM64) | `NexTerm_x.y.z_<arch>-setup.exe`, run the installer. |
| macOS (Apple Silicon / Intel) | `NexTerm_x.y.z_<arch>.dmg`, drag into Applications. |
| Linux desktop (amd64 / arm64) | `NexTerm-desktop_x.y.z_linux_<arch>.deb`, install with `apt install ./<package>.deb`. |
| Linux server | `NexTerm-server_x.y.z_linux_<arch>.tar.gz`. |
| Android | `NexTerm_x.y.z_android.apk`, install directly on arm64 devices. |
| iOS / iPadOS | `NexTerm_x.y.z_ios.ipa` (ad-hoc signed). |
| Lazycat | Install NexTerm from the app center. |

When macOS says the developer cannot be verified on first launch, click "Open Anyway" under System Settings → Privacy & Security; if it says the app is damaged, run `xattr -dr com.apple.quarantine /Applications/NexTerm.app`.

First use:

1. Add an SSH or WinRM asset, or use the built-in "current device".
2. Before saving passwords or private keys, initialize the credential vault as prompted under Settings → Credential Protection; credentials cannot be saved until it is initialized. Password-free protection relies on OS keys (Windows DPAPI / macOS Keychain) and is unavailable on Linux desktop - use master-password mode instead: enable "protect credentials with a password" and set a password of at least 8 characters; unlock with it on every start, and the vault locks automatically when idle.
3. To use AI, fill in the OpenAI-compatible endpoint, API key, and model name in settings.
4. The desktop app works locally without signing in; sign in under Settings → Account Sync to sync assets across devices.

### Auto-update

The desktop app silently checks for updates once at startup with no popups; when a new version is found, an update banner appears at the top of the window. Settings → Software Update shows the version, package size, and release notes, with one-click download and install. The update channel follows the current version: prerelease builds receive prerelease updates, stable builds receive only stable ones. Packages are verified against the SHA256SUMS attached to the Release before installation. The server does not auto-update; upgrade it per the instructions inside its package.

## Server and Device Enrollment

- **Linux server**: after extracting the server package, follow the `README.md` inside to install the binary, systemd service, and environment file. On first start the server prints a one-time initialization code to the console (under systemd, view it with `journalctl -u nexterm-server`); open the UI in a browser, enter the code, and set the super-admin username and password. Access control is on by default (`--auth on`) and login is required regardless of the listen address; for public deployment, listen on loopback only and put a TLS reverse proxy in front. Full CLI options: `nexterm-server --help`.
- **Lazycat**: open and use directly after installing from the app center; terminal processes and data stay on your Lazycat. NexTerm's built-in port forwarding is unavailable there - use the platform's own forwarding for externally reachable ports.
- **Device enrollment**: issue a one-time enrollment code from Device Management (browser mode, or the desktop app signed into a server account); on Linux/macOS run the one-line install command from the panel, on Windows the corresponding PowerShell command - it downloads nexterm-agent, redeems the device credentials, and installs an auto-start service (systemd / launchd / Windows service). Devices can open remote terminals at any time and can be revoked (credentials die immediately and cannot be recovered).

## Build and Test from Source

Toolchain: Go 1.26, Node 24+, pnpm.

```bash
pnpm install     # install frontend dependencies
pnpm build       # type-check and build frontend assets into dist/
go build ./...   # compile all Go packages
```

Routine local quality gates, all at once with task installed:

```bash
task check   # gofmt + go vet + go test ./... + go mod verify + pnpm typecheck + pnpm lint
```

## Contributing

- Branch from the latest `master` commit and merge back via PR; PR titles and commit messages use Conventional Commits (`feat` / `fix` / `docs` / `refactor` / `test` / `ci` / `build` / `perf` / `chore`).
- PR descriptions cover three things: the problem solved, what changed, and how it was verified; user-perceivable and behavior changes are named explicitly.
- Local gates are green before committing (`task check`; full list under "Quality Gates and Test Cadence" in [AGENTS.md](AGENTS.md)); CI runs the same checks on PRs.
- Development conventions, review rules, and multi-agent collaboration: see [AGENTS.md](AGENTS.md).

## License

[MIT](LICENSE)

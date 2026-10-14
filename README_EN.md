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

- **Terminals and files**: SSH, WinRM, and local terminals with multiple tabs, split panes, SFTP file management, and in-place editing. SSH sessions continue running as background sessions after a tab closes or disconnects, ready for re-attachment; local, WinRM non-interactive, and container exec terminals end when closed.
- **Terminal history**: records every terminal session's output, keystrokes, and window resizes for text viewing, searching, and timeline replay. **Keystrokes are recorded verbatim, including passwords and tokens.** Records stay local and are not synced by default; end-to-end encrypted sync and deletion are available per record.
- **Daily ops**: manage Docker containers and images, and use PostgreSQL, MySQL, Redis, and SSH port forwarding. PostgreSQL and plain SSH/SFTP need no remote agent; the agent is only for device management.
- **AI assistant**: supports OpenAI-compatible endpoints, terminal takeover, and scheduled tasks. Command output and file changes are visible, and sensitive operations require confirmation.
- **Accounts and sync**: the server provides multi-user accounts, with user creation or open registration managed by the super admin. Sync data is isolated per account and end-to-end encrypted. On desktop, signing in automatically syncs assets, groups, snippets, credentials, and optional settings.
- **Device management and sharing**: install the device agent on a host to open remote terminals. Enrolled hosts can be shared with other registered users or through public links. Anyone with a link can access it without signing in while it remains valid. Links are read-only by default, writable only when explicitly granted, and can be revoked at any time. Write access lets the visitor type in the terminal, so grant it only to people you trust. Sharing does not require the host password or private key.
- **Desktop and browser**: use the desktop app locally; in browser mode, workspaces and terminal processes stay on the server and keep running after the page closes.

## Download and Install

Download the latest version from [Releases](https://github.com/Hello-CTF/NexTerm/releases/latest).

| Platform | Package |
| --- | --- |
| Windows 10/11 (x64 / ARM64) | `NexTerm_x.y.z_<arch>-setup.exe`, run the installer. |
| macOS (Apple Silicon / Intel) | `NexTerm_x.y.z_<arch>.dmg`, drag into Applications. |
| Linux desktop (amd64 / arm64) | `NexTerm-desktop_x.y.z_linux_<arch>.deb`, install with `apt install ./<package>.deb`. |
| Linux server | `NexTerm-server_x.y.z_linux_<arch>.tar.gz`. |
| Android | `NexTerm_x.y.z_android.apk`, install directly on arm64 devices. |
| iOS / iPadOS | `NexTerm_x.y.z_ios.ipa` (ad-hoc signed). |
| Lazycat | Install NexTerm from the app center. |

If macOS says it cannot verify the developer on first launch, click "Open Anyway" under System Settings → Privacy & Security; if it says the app is damaged, run `xattr -dr com.apple.quarantine /Applications/NexTerm.app`.

First use:

1. Add an SSH or WinRM asset, or use the built-in "current device".
2. Before saving passwords or private keys, initialize the credential vault as prompted under Settings → Credential Protection; credentials cannot be saved otherwise. Windows and macOS can use system-level key protection (DPAPI / Keychain). On Linux desktop, enable "protect credentials with a password" and set a protection password of at least 8 characters. It stays on this device and is never uploaded or synced; forgetting it makes saved credentials unrecoverable. With a protection password enabled, the vault must be unlocked with it at every startup; idle auto-lock can also be configured.
3. To use AI, create a model profile under Settings → AI Model, enter the OpenAI-compatible base URL and model name, provide an API key if the endpoint requires one (leave it blank for unauthenticated local endpoints), and save the profile.
4. The desktop app works locally without signing in; sign in under Settings → Account Sync to sync assets across devices.

### Auto-update

The desktop app checks for updates once at startup and shows an update banner when a new version is available. Settings → Software Update shows the current and new versions and the package size, with one-click download and installation; see the corresponding Release for release notes. Prerelease builds receive prerelease updates; stable builds receive only stable updates. Packages are verified against the SHA256SUMS attached to the Release before installation. The server does not auto-update; upgrade it as described in its package.

## Server and Device Enrollment

- **Linux server**: after extracting the server package, follow its `README.md` to install. On first startup, the server prints a one-time initialization code to the console (with systemd, view it using `journalctl -u nexterm-server`). Open the UI in a browser, enter the code, and set the super-admin username and password. Access control is enabled by default (`--auth on`), and login is required regardless of the listen address. For public deployments, listen only on loopback and put a TLS reverse proxy in front. See `nexterm-server --help` for all CLI options.
- **Lazycat**: after installing from the app center, open and use directly; terminal processes and data stay on your Lazycat. NexTerm's built-in port forwarding is unavailable there; use the platform's own forwarding for externally reachable ports.
- **Device enrollment**: a super admin configures the access address in Device Management. You can issue an enrollment code for yourself or, as a super admin, for another user. Each code is single-use and time-limited. On Linux/macOS, run the panel's one-line install command as a regular user; on Windows, run the corresponding PowerShell command. It downloads and installs nexterm-agent and configures auto-start on login (systemd --user / launchd / Windows Task Scheduler). Device credentials are stored only on the device; the server stores only their hashes. After enrollment, remote terminals can be opened while the device is online; revoked credentials become invalid immediately and cannot be recovered.

## Build and Test from Source

Toolchain: Go 1.26, Node 24+, pnpm.

```bash
pnpm install
pnpm build
go build ./...
```

Routine quality checks (with task installed):

```bash
task check
```

## Contributing

Branch from the latest `master` commit and merge back via PR. Use Conventional Commits for PR titles and commits. Describe the problem solved, changes made, and verification performed, naming any user-perceivable and behavioral changes. Complete all required quality checks before committing. See [AGENTS.md](AGENTS.md) for development, review, and collaboration conventions.

## License

[MIT](LICENSE)

<div align="center">

<img src="website/public/brand/nexterm-mark-128.png" width="88" alt="NexTerm" />

# NexTerm

**One self-hostable workbench for every server you run.**

Terminals that never drop · an AI assistant that actually does the work · files, Docker, databases, device fleet, and sharing

[![Website](https://img.shields.io/badge/website-nexterm.work-516cd6)](https://nexterm.work/)
[![Release](https://img.shields.io/github/v/release/Hello-CTF/NexTerm?color=516cd6)](https://github.com/Hello-CTF/NexTerm/releases/latest)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8)
![Wails](https://img.shields.io/badge/Wails-v3-CC0000)
![React](https://img.shields.io/badge/React-19-61DAFB)
![License](https://img.shields.io/badge/license-MIT-green)

**English** | [中文](README.md)

[Website](https://nexterm.work/) · [Download](https://github.com/Hello-CTF/NexTerm/releases/latest) · [Deploy the server](#server-and-device-enrollment)

</div>

<picture>
  <source media="(prefers-color-scheme: light)" srcset="website/public/images/shell-light.jpg" />
  <img src="website/public/images/shell-dark.jpg" alt="NexTerm terminal with command blocks" />
</picture>

NexTerm puts the whole remote-ops loop in one place. Connect over SSH, WinRM, or the NexTerm device agent; let an AI assistant investigate and fix with your approval; then browse files, manage containers, query databases, watch device metrics, and share a terminal, all without switching tools. SSH and device shells keep running in the background, so closing the window or switching devices never loses your work.

Use the desktop app locally, or deploy the server for multi-user, browser-based access. Both modes can be self-hosted and keep data on infrastructure you control.

## Highlights

### 🤖 An AI assistant that does the work

- **20+ built-in tools**: run commands, read, write, and edit files, inspect Docker containers and logs, query MySQL, PostgreSQL, and Redis, and spawn sub-tasks.
- **You stay in control**: read-only or execute modes, plan mode before execution, and command/diff previews with allow once, allow for this turn, or deny. High-risk actions always ask.
- **Per-host grants**: allow command execution, file writes, or keystrokes per asset, scoped to paths such as `/etc/nginx/**`, with expiry from 1 hour to 30 days.
- **Terminal takeover**: the assistant can type into your live terminal; press Esc to take control back.
- **Scheduled jobs and memory**: cron-scheduled AI tasks, plus long-term memory that rejects secrets.
- **Any model**: OpenAI-compatible endpoints with presets for DeepSeek, Kimi, GLM, and Ollama, fallback models, proxies, and usage tracking.

### 🖥️ Terminals that stay

- **Background sessions**: SSH and managed-device sessions keep running after a tab closes, the app restarts, or the network drops, and can be taken over from any signed-in device with missed output replayed. Local, WinRM non-interactive, and container exec terminals end when closed.
- **Multi-device**: several devices can watch one terminal while one types.
- **Command blocks, history, and replay**: every command folds into a block; every session's output, keystrokes, and resizes are recorded for search and timeline replay. **Keystrokes are recorded verbatim, including passwords and tokens.** Records stay local and are not synced by default; end-to-end encrypted sync and deletion are available per record.
- **Productivity**: tabs, split panes, broadcast input, quick connect, a command palette, snippets, and rebindable shortcuts.

### 🧰 Everyday ops in one window

- **Files**: SFTP, WinRM, and device file browsers with upload/download, an in-place editor that backs up before saving, a paged log viewer, and directory follow.
- **Docker**: containers, logs, stats, images, and one-click exec terminals.
- **Databases**: SQL editor and schema browser for MySQL/MariaDB and PostgreSQL, plus a Redis key browser.
- **Port forwarding**: local forwards and SOCKS5 proxies with automatic reconnect.
- **Import**: hosts and keys from `~/.ssh/config` or Termius, with groups, tags, jump hosts, and SSH certificates.

PostgreSQL and plain SSH/SFTP need no remote agent; the agent is only for device management.

### 🛰️ Device fleet and sharing

- **Device agent**: enroll a host with one command; it connects out to your server, reports CPU, memory, and disk metrics, and opens remote terminals and files without exposing SSH.
- **Sharing**: share enrolled hosts with registered users or through public links. Anyone with a link can access it without signing in while it remains valid. Links are read-only by default, writable only when explicitly granted, and can be revoked at any time. Write access lets the visitor type in the terminal, so grant it only to people you trust. Sharing does not require the host password or private key.

### 🔐 Self-hosted and secure

- **Accounts and sync**: the server provides multi-user accounts with TOTP two-factor authentication; user creation or open registration is managed by the super admin. Sync data is isolated per account and end-to-end encrypted. On desktop, signing in syncs assets, groups, snippets, credentials, and optional settings.
- **Credential vault**: master password or OS-level protection (DPAPI / Keychain), biometric unlock on mobile, and idle auto-lock.
- **Audit log**: commands, file writes, and device events from both users and the AI.
- **Your database**: SQLite by default; the server also supports PostgreSQL, MySQL, and MariaDB.
- **Desktop and browser**: in browser mode, workspaces and terminal processes stay on the server and keep running after the page closes.

## Screenshots

| AI assistant | Remote files | Terminal replay |
| --- | --- | --- |
| <img src="website/public/images/ai-dark.jpg" alt="AI assistant with command confirmation" /> | <img src="website/public/images/files-dark.jpg" alt="SFTP file tree and remote editor" /> | <img src="website/public/images/replay-dark.jpg" alt="Terminal history timeline replay" /> |

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

- **Linux server**: after extracting the server package, follow its `README.md` to install. On first startup, the server prints a one-time initialization code to the console (with systemd, view it using `journalctl -u nexterm-server`). Open the UI in a browser, enter the code, and set the super-admin username and password. Access control is enabled by default (`--auth on`), and login is required regardless of the listen address. For public deployments, listen only on loopback and put a TLS reverse proxy in front. Have the proxy overwrite `X-Forwarded-Proto`, then set `NEXTERM_TRUSTED_PROXY_CIDRS` to the proxy source CIDRs (comma separated, for example `127.0.0.1/32`) so HTTPS login cookies carry `Secure`; forwarding headers from other addresses are ignored. NexTerm defaults to SQLite and also supports PostgreSQL, MySQL, and MariaDB through `--db postgres|mysql|mariadb` and `--db-dsn`. Run one server instance per database. Preserve the entire data directory when backing up, together with the external database when configured. See `nexterm-server --help` for all CLI options.
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

External databases share one integration suite. Always use a database dedicated to testing:

```bash
NEXTERM_TEST_DB_BACKEND=postgres NEXTERM_TEST_DB_DSN='postgres://nexterm:password@127.0.0.1:5432/nexterm_test?sslmode=disable' task test:database
```

`NEXTERM_TEST_DB_BACKEND` also accepts `mysql` or `mariadb`, with the corresponding driver DSN format. The suite defaults to SQLite; an explicitly selected external backend requires a working DSN.

## Contributing

Branch from the latest `master` commit and merge back via PR. Use Conventional Commits for PR titles and commits. Describe the problem solved, changes made, and verification performed, naming any user-perceivable and behavioral changes. Complete all required quality checks before committing. See [AGENTS.md](AGENTS.md) for development, review, and collaboration conventions.

## License

[MIT](LICENSE)

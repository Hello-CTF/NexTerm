<div align="center">

<img src="website/public/brand/nexterm-mark-128.png" width="88" alt="NexTerm" />

# NexTerm

**One self-hostable workbench for every server you run.**

Persistent, re-attachable terminals · an AI assistant that actually does the work · files, Docker, databases, device fleet, and sharing

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

The images use sample data. Try the interactive terminal, AI, file, and replay demos on the [website](https://nexterm.work/).

> [!NOTE]
> The NexTerm app UI is currently in Simplified Chinese.

NexTerm puts the whole remote-ops loop in one window. Connect over SSH, WinRM, or the NexTerm device agent, let an AI assistant investigate and fix with your approval, and handle files, containers, databases, device metrics, and terminal sharing without switching tools. Successfully enabled durable SSH sessions and persistent managed-device sessions can keep running in the background and be re-attached; WinRM non-interactive terminals end when closed.

<table>
<tr>
<td width="33%" valign="top">

**🤖 AI that does the work**<br/>
20+ tools across terminal, files, and Docker, with plan mode, diff previews, and per-host grants.

</td>
<td width="33%" valign="top">

**🖥️ Persistent terminals**<br/>
SSH sessions with the durable helper successfully enabled and persistent managed-device sessions survive app restarts and dropped networks; take over from any signed-in device.

</td>
<td width="33%" valign="top">

**🧰 Everyday ops built in**<br/>
SFTP and remote editing, Docker, MySQL, PostgreSQL, Redis, port forwarding, and SSH config import.

</td>
</tr>
<tr>
<td valign="top">

**🛰️ Device fleet**<br/>
One-command agent install, outbound-only connection, CPU, memory, and disk metrics, and remote terminals.

</td>
<td valign="top">

**🔗 Sharing**<br/>
Time-boxed, revocable terminal links, read-only by default, without handing over passwords or keys.

</td>
<td valign="top">

**🔐 Self-hosted**<br/>
Multi-user server with 2FA, end-to-end encrypted sync, a credential vault, and an audit log.

</td>
</tr>
</table>

## Highlights

### 🤖 An AI assistant that does the work

- **20+ built-in tools**: run commands; read, write, and edit files; inspect Docker containers and logs; read terminal output; spawn sub-tasks.
- **You stay in control**: read-only or execute modes, plan mode, and command or diff previews with *allow once*, *allow for this turn*, or *deny*. High-risk actions always ask.
- **Per-host grants**: allow commands, file writes, or keystrokes per asset, scoped to paths such as `/etc/nginx/**`, for 1 hour to 30 days.
- **Terminal takeover**: the assistant can type into your live terminal; press Esc to take control back.
- **Scheduled jobs and memory**: cron-scheduled AI tasks and long-term memory that rejects secrets.
- **Any model**: OpenAI-compatible endpoints with DeepSeek, Kimi, GLM, and Ollama presets, fallback models, proxies, and usage tracking.

### 🖥️ Persistent terminals

- **Background sessions**: SSH sessions with the durable helper successfully enabled and persistent managed-device sessions keep running after a tab closes, the app restarts, or the network drops. Take them over from any signed-in device with missed output replayed. When the SSH helper is unavailable, a fallback regular SSH PTY cannot be recovered after closing or disconnecting. Local, WinRM non-interactive, and container exec terminals end when closed.
- **Multi-device**: several devices can watch one terminal while one types.
- **Command blocks, history, and replay**: every command folds into a block; output, keystrokes, and resizes are recorded for search and timeline replay. Records stay local by default; end-to-end encrypted sync and deletion are available per record.
- **Productivity**: tabs, split panes, broadcast input, quick connect, a command palette, snippets, and rebindable shortcuts.

> [!WARNING]
> Terminal history records keystrokes verbatim, including passwords and tokens.

### 🧰 Everyday ops in one window

- **Files**: SFTP, WinRM, and device file browsers with upload and download, an in-place editor that backs up before saving, a paged log viewer, and directory follow.
- **Docker**: containers, logs, stats, images, and one-click exec terminals.
- **Databases**: SQL editor and schema browser for MySQL/MariaDB and PostgreSQL, plus a Redis key browser.
- **Port forwarding**: local forwards and SOCKS5 proxies with automatic reconnect.
- **Import**: import hosts and keys from `~/.ssh/config` or Termius with a conflict preview. Single-hop `ProxyJump` is mapped automatically; source groups, tags, and SSH certificates are not imported automatically, and entries using multi-hop `ProxyJump` cannot currently be imported automatically. Termius import is currently available only on macOS: other desktop platforms disable the entry, mobile and Web do not show it, and there is no manual KeySource fallback.

PostgreSQL and plain SSH/SFTP need no remote agent; the agent is only for device management.

### 🛰️ Device fleet and sharing

- **Device agent**: enroll a host with one command. It connects out to your server, reports CPU, memory, and disk metrics, and opens remote terminals and files without exposing SSH.
- **Public terminal preview sharing**: create time-limited public links to existing live sessions. Anyone with a link can join without signing in while it remains valid. Links are read-only by default; browser mode allows explicit write grants, while previews created through the desktop relay are currently read-only. Links are revocable at any time. Sharing does not require the host password or private key.
- **Host sharing with registered users**: share a host's terminal session with another registered user (terminal only — no files, forwarding, device management, or AI). Recipients open the terminal through a dedicated share entry: read-only by default, writable only with an explicit grant, and revocable at any time. No host password or private key is required.

> [!CAUTION]
> Write access lets the visitor type in the terminal. Grant it only to people you trust.

### 🔐 Self-hosted and secure

- **Accounts and sync**: multi-user server accounts with TOTP two-factor authentication; the super admin creates users or opens registration. Sync data is isolated per account and end-to-end encrypted. On desktop, signing in syncs assets, groups, snippets, credentials, and optional settings.
- **Credential vault**: master password or OS-level protection (DPAPI / Keychain), biometric unlock on mobile, and idle auto-lock.
- **Server credential protection**: in browser mode, credentials are encrypted by the server-side vault protected by its master key. This is separate from end-to-end encrypted desktop sync; the server can decrypt credentials to establish connections. Protect the data directory and master key.
- **Audit log**: commands, file writes, and device events from both users and the AI.
- **Your database**: SQLite by default; the server also supports PostgreSQL, MySQL, and MariaDB.
- **Desktop and browser**: in browser mode, workspaces and terminal processes stay on the server and keep running after the page closes.

## Screenshots

<table>
<tr>
<th width="33%">AI assistant</th>
<th width="33%">Remote files</th>
<th width="33%">Terminal replay</th>
</tr>
<tr>
<td><picture><source media="(prefers-color-scheme: light)" srcset="website/public/images/ai-light.jpg" /><img src="website/public/images/ai-dark.jpg" alt="AI assistant with command confirmation" /></picture></td>
<td><picture><source media="(prefers-color-scheme: light)" srcset="website/public/images/files-light.jpg" /><img src="website/public/images/files-dark.jpg" alt="SFTP file tree and remote editor" /></picture></td>
<td><picture><source media="(prefers-color-scheme: light)" srcset="website/public/images/replay-light.jpg" /><img src="website/public/images/replay-dark.jpg" alt="Terminal history timeline replay" /></picture></td>
</tr>
</table>

## Download and Install

Download the latest version from [Releases](https://github.com/Hello-CTF/NexTerm/releases/latest).

| Platform | Package | Install |
| --- | --- | --- |
| Windows 10/11 (x64 / ARM64) | `NexTerm_x.y.z_<arch>-setup.exe` | Run the installer. |
| macOS (Apple Silicon / Intel) | `NexTerm_x.y.z_<arch>.dmg` | Drag into Applications. |
| Linux desktop (amd64 / arm64) | `NexTerm-desktop_x.y.z_linux_<arch>.deb` | `apt install ./<package>.deb` |
| Linux server | `NexTerm-server_x.y.z_linux_<arch>.tar.gz` | See [Server and Device Enrollment](#server-and-device-enrollment). |
| Android (preview) | `NexTerm_x.y.z_android.apk` | Preview: install directly on arm64 devices. |
| iOS / iPadOS (preview) | TestFlight | Preview: install with a NexTerm TestFlight invitation; releases do not provide an IPA asset. |
| Lazycat | App center | Install NexTerm from the app center. |

> [!NOTE]
> Mobile platforms (Android / iOS / iPadOS) are currently in preview and are not a formally supported platform until real-device signing acceptance completes.

> [!TIP]
> If macOS says it cannot verify the developer on first launch, click "Open Anyway" under System Settings → Privacy & Security. If it says the app is damaged, run `xattr -dr com.apple.quarantine /Applications/NexTerm.app`.

### Quick start

1. **Add a host**: create an SSH or WinRM asset, or use the built-in "current device".
2. **Set up the vault**: before saving passwords or private keys, initialize it under **Settings → Credential Protection**; credentials cannot be saved otherwise. Windows and macOS can use system-level key protection (DPAPI / Keychain). On Linux desktop, enable "protect credentials with a password" and set a password of at least 8 characters. With a protection password, the vault must be unlocked at every startup; idle auto-lock is optional.
3. **Connect a model (optional)**: under **Settings → AI Model**, create a profile with an OpenAI-compatible base URL and model name. Provide an API key if the endpoint requires one; leave it blank for unauthenticated local endpoints.
4. **Sync (optional)**: the desktop app works locally without signing in. Sign in under **Settings → Account Sync** to sync assets across devices.

> [!IMPORTANT]
> The protection password stays on this device and is never uploaded or synced. Forgetting it makes saved credentials unrecoverable.

<details>
<summary><strong>Auto-update</strong></summary>

The desktop app checks for updates once at startup and shows an update banner when a new version is available. Settings → Software Update shows the current and new versions and the package size, with one-click download and installation; see the corresponding Release for release notes. Prerelease builds receive prerelease updates; stable builds receive only stable updates. Packages are verified against the SHA256SUMS attached to the Release before installation. The server does not auto-update; upgrade it as described in its package.

</details>

## Server and Device Enrollment

### Linux server

1. Extract the server package and follow its `README.md` to create the `nexterm` user, install the binaries, units and env files, then run `systemctl daemon-reload`.
2. On first startup, the server prints a one-time initialization code to the console (with systemd, view it using `journalctl -u nexterm-server`).
3. Open the UI in a browser, enter the code, and set the super-admin username and password.

- Under systemd, configure the listen address, access control, data directory and web root in `/etc/nexterm/nexterm.env` or `/etc/nexterm/onlyserver.env`. Access control is enabled by default (`NEXTERM_AUTH=on`), and login is required regardless of the listen address.
- NexTerm defaults to SQLite and also supports PostgreSQL, MySQL, and MariaDB through `NEXTERM_DB=postgres|mysql|mariadb` and `NEXTERM_DB_DSN`. A remote database password can be read from the 0600 file named by `NEXTERM_DB_PASSWORD_FILE`; `NEXTERM_DB_MAX_OPEN_CONNS` controls the pool size. Run one server instance per database.
- Preserve the entire data directory and vault root key when backing up, together with the external database when configured.
- See `nexterm-server --help` for all CLI options.

<details>
<summary><strong>Public deployment behind a reverse proxy</strong></summary>

Listen only on loopback and put a TLS reverse proxy in front. Have the proxy overwrite `X-Forwarded-Proto`, then set `NEXTERM_TRUSTED_PROXY_CIDRS` to the proxy source CIDRs (comma separated, for example `127.0.0.1/32`) so HTTPS login cookies carry `Secure`; forwarding headers from other addresses are ignored.

</details>

### Lazycat

After installing from the app center, open and use directly; terminal processes and data stay on your Lazycat. NexTerm's built-in port forwarding is unavailable there; use the platform's own forwarding for externally reachable ports. To build a package from source, run `bash lazycat/build.sh`; only `LZC_ARCH=amd64` is currently accepted, and arm64 fails before compilation with a clear error.

### Device enrollment

1. A super admin configures the access address in Device Management.
2. Issue an enrollment code for yourself or, as a super admin, for another user. Each code is single-use and time-limited.
3. On Linux/macOS, run the panel's one-line install command as a regular user; on Windows, run the corresponding PowerShell command. It installs nexterm-agent and configures auto-start on login (systemd --user / launchd / Windows Task Scheduler).

Device credentials are stored only on the device; the server stores only their hashes. After enrollment, remote terminals can be opened while the device is online. Revoked credentials become invalid immediately and cannot be recovered.

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

<details>
<summary><strong>Database integration tests</strong></summary>

External databases share one integration suite. Always use a database dedicated to testing:

```bash
NEXTERM_TEST_DB_BACKEND=postgres NEXTERM_TEST_DB_DSN='postgres://nexterm:password@127.0.0.1:5432/nexterm_test?sslmode=disable' task test:database
```

`NEXTERM_TEST_DB_BACKEND` also accepts `mysql` or `mariadb`, with the corresponding driver DSN format. The suite defaults to SQLite; an explicitly selected external backend requires a working DSN.

</details>

## Contributing

Branch from the latest `master` commit and merge back via PR. Use Conventional Commits for PR titles and commits. Describe the problem solved, changes made, and verification performed, naming any user-perceivable and behavioral changes. Complete all required quality checks before committing. See [AGENTS.md](AGENTS.md) for development, review, and collaboration conventions.

## License

[MIT](LICENSE)

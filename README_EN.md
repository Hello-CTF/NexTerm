<div align="center">

<img src="website/public/brand/nexterm-mark-128.png" width="88" alt="NexTerm" />

# NexTerm

**One self-hostable workbench for every server you run.**

Persistent, re-attachable terminals · an AI assistant that actually does the work · files, Docker, databases, device fleet, and sharing

[![Website](https://img.shields.io/badge/website-nexterm.work-516cd6)](https://nexterm.work/)
[![Release](https://img.shields.io/github/v/release/Hello-CTF/NexTerm?color=516cd6)](https://github.com/Hello-CTF/NexTerm/releases/latest)
![Go](https://img.shields.io/badge/Go-1.27-00ADD8)
![Wails](https://img.shields.io/badge/Wails-v3-CC0000)
![React](https://img.shields.io/badge/React-19-61DAFB)
![License](https://img.shields.io/badge/license-MIT-green)

**English** | [中文](README.md)

[Website](https://nexterm.work/) · [Download](https://github.com/Hello-CTF/NexTerm/releases/latest) · [Deploy the server](#deploy-the-server)

</div>

<picture>
  <source media="(prefers-color-scheme: light)" srcset="website/public/images/shell-light.jpg" />
  <img src="website/public/images/shell-dark.jpg" alt="NexTerm terminal with command blocks" />
</picture>

NexTerm puts remote operations in one window: connect over SSH, WinRM, or the device agent, let the AI assistant diagnose and fix issues after you confirm, then work with files, containers, databases, device monitoring, and terminal sharing. The images use sample data; try the live demos on the [website](https://nexterm.work/).

## Features

- **AI assistant**: 20+ tools for commands, files, Docker, and terminal output; commands and diffs are shown before they run, with plan mode, per-host and per-path grants, scheduled tasks, and long-term memory. Works with any OpenAI-compatible API, with DeepSeek, Kimi, GLM, and Ollama presets.
- **Persistent terminals**: they keep running after you close the tab, quit the app, or lose the network, can be taken over or ended from Background sessions, and can be re-attached from any signed-in device with missed output replayed. Supported for desktop local terminals and, once enabled, SSH and managed-device sessions; a terminal opened while the helper is not ready is a temporary terminal with the reason shown, and a temporary terminal ends on disconnect. Mobile has no local terminals.
- **Session status**: badges, the header inbox, and desktop notifications point at the sessions that need you, based on terminal bells, program notifications, and rules you save; NexTerm does not guess from the screen. The rail's session overview is a status wall (shown at launch on mobile).
- **Many devices**: several devices can watch and type into one terminal at once, and the size follows the last device that typed. To see a desktop's local terminals from a phone, turn on "Expose local terminals to the server account" in Settings: this hands that computer's command line, as the current user, to the server account's administrators, so enable it only for a server you trust.
- **Terminal tools**: command blocks, searchable history and replay, broadcast, snippets, nestable splits, and a jump palette (Ctrl/Cmd+Shift+P) that ranks sessions by how much they need you.
- **Everyday operations**: SFTP, WinRM, and device file browsing with in-place editing; Docker containers, logs, and images; MySQL/MariaDB, PostgreSQL, and Redis; local forwarding and SOCKS5; host import from `~/.ssh/config` or Termius (macOS only).
- **Devices and sharing**: enroll the device agent with one command; devices connect outward and report metrics. Share a terminal as a time-limited public link or with registered users (terminal only), read-only by default and revocable.
- **Self-hosted**: multi-user accounts with TOTP; end-to-end encrypted sync (state rules, runbook templates, and AI skills sync by default; AI memory and scheduled jobs are separate opt-ins under Settings → Account sync); a credential vault; audit logs; SQLite, PostgreSQL, MySQL, or MariaDB. In browser mode, terminals and workspaces live on the server.

The website describes each feature in full: [nexterm.work](https://nexterm.work/).

<table>
<tr>
<th width="33%">AI assistant</th>
<th width="33%">Remote files</th>
<th width="33%">Terminal replay</th>
</tr>
<tr>
<td><picture><source media="(prefers-color-scheme: light)" srcset="website/public/images/ai-light.jpg" /><img src="website/public/images/ai-dark.jpg" alt="AI assistant chat with command confirmation" /></picture></td>
<td><picture><source media="(prefers-color-scheme: light)" srcset="website/public/images/files-light.jpg" /><img src="website/public/images/files-dark.jpg" alt="SFTP file tree and remote editing" /></picture></td>
<td><picture><source media="(prefers-color-scheme: light)" srcset="website/public/images/replay-light.jpg" /><img src="website/public/images/replay-dark.jpg" alt="Terminal history timeline replay" /></picture></td>
</tr>
</table>

## Download

Get the latest build from [Releases](https://github.com/Hello-CTF/NexTerm/releases/latest):

| Platform | Package |
| --- | --- |
| Windows 10/11 (amd64 / arm64) | `nexterm-desktop_windows_<arch>_x.y.z.exe` |
| macOS (arm64 / amd64) | `nexterm-desktop_macos_<arch>_x.y.z.dmg` |
| Linux desktop (amd64 / arm64) | `nexterm-desktop_linux_<arch>_x.y.z.deb`, install with `apt install ./<package>.deb`; needs GTK 4.14+ and WebKitGTK 6.0; the `.deb` declares the minimum glibc its binary requires, so apt refuses older systems |
| Linux server | See [Deploy the server](#deploy-the-server) |
| Android (preview, arm64) | `nexterm-desktop_android_arm64_x.y.z.apk` |
| iOS / iPadOS (preview) | [Join the TestFlight beta](https://testflight.apple.com/join/ds62pzPz); releases do not ship an IPA |
| LazyCat MicroServer (amd64) | App Store |

Mobile builds are previews and are not officially supported until real-device signing is verified. The desktop app checks for updates automatically and verifies them against `SHA256SUMS`.

- Windows installers are not code-signed: if Windows reports an unknown publisher, choose "More info → Run anyway".
- If macOS cannot verify the developer, choose "Open Anyway" in System Settings → Privacy & Security; if it reports the app is damaged, run `xattr -dr com.apple.quarantine /Applications/NexTerm.app`.
- On the Linux desktop, install `fonts-noto-color-emoji`, or emoji in tab titles may show as monochrome symbols or boxes.

**Quick start**: add an SSH or WinRM asset (or use the built-in "This device") → initialize the vault in Settings → Credential protection before saving passwords or keys → optionally add a model in Settings → AI models → optionally sign in under Settings → Account sync to sync across devices. The protection password only unlocks this device; if you forget it, saved credentials cannot be recovered.

**AI privacy redaction**: off by default. Download the latest released `QuantumNous/astrlink-guard` model in Settings → 信息脱敏 (downloaded from Hugging Face; weights are not bundled), select INT8, FP32, or FP16, then enable it.

- When enabled, an unavailable model or runtime blocks requests instead of sending plaintext; images and other non-text content are not redacted and go to the AI provider unchanged; detection can miss sensitive information.
- Local inference needs Linux (glibc 2.27 or newer with libstdc++; musl is not supported), Windows, macOS 13.4+, iOS/iPadOS 15.1+, or Android 7.0+. Redaction is unavailable on platforms ONNX Runtime no longer publishes (currently Intel Macs); the rest of the app works.
- Detection runs on the device running AI, or on the server in browser mode. The implementation reuses [AstrLink](https://github.com/Calcium-Ion/AstrLink); see the [source and license notice](internal/ai/privacy/third_party/ASTRLINK_NOTICE.md).

## Deploy the server

On Linux with systemd (amd64 / arm64, glibc-based; musl distributions such as Alpine are not supported), run as root:

```bash
curl -fsSL https://nexterm.work/install.sh | sudo bash
```

The script downloads the latest server archive and verifies it against `SHA256SUMS`, creates the `nexterm` user and directories, generates the master key, creates the `admin` super-admin and prints its initial password once in the installer output (write it down; it must be changed at first login, or preset it with `NEXTERM_ADMIN_PASSWORD`), then starts `nexterm-server`. Running it again upgrades in place and keeps configuration and data; add `--rollback` to undo the last upgrade, or `-s -- --version x.y.z` to pin a version. You can also download `nexterm-server_linux_<arch>_x.y.z.tar.gz`, extract it, and run `sudo ./install.sh`.

- The service listens on `127.0.0.1:8080` and requires sign-in by default; run `ssh -L 8080:127.0.0.1:8080 user@server` and open `http://localhost:8080`. Settings live in `/etc/nexterm/nexterm.env`; run `systemctl restart nexterm-server` after changing them.
- `--auth off` disables accounts, administration and device routes; anyone who can reach the listen address can operate the server, so never use it on a public network.
- For public access, put a TLS reverse proxy in front, have it overwrite `X-Forwarded-Proto`, and list its source ranges in `NEXTERM_TRUSTED_PROXY_CIDRS`.
- SQLite is the default; use `NEXTERM_DB=postgres|mysql|mariadb` with `NEXTERM_DB_DSN` for an external database, with one server instance per database.
- Back up `/var/lib/nexterm`, `/etc/nexterm/master.key`, and any external database. See the bundled `README.md` and `nexterm-server --help` for every option.

**LazyCat MicroServer**: install from the App Store (amd64 devices only for now); built-in port forwarding is unavailable, so use the platform's forwarding.

**Device enrollment**: a super-admin sets the enrollment address in Devices and issues a one-time enrollment code; run the one-line command shown there on the device (as a regular user on Linux/macOS, in PowerShell on Windows). The agent starts automatically at user login. Install records the then-current `SHELL` and `PATH` in the service definition (`nexterm-agent status` shows them; re-run the install command to refresh). The agent reads PATH through a non-interactive login shell, so entries defined only in `~/.zshrc` are not visible to it: put them in `~/.zprofile`.

## Development

Toolchain: Go 1.27, Node 24+, pnpm. The Linux server is a `CGO_ENABLED=0` build that can be produced on any host; building the Linux desktop needs the GTK 4.14+ and WebKitGTK 6.0 development packages (`libgtk-4-dev` and `libwebkitgtk-6.0-dev` on Debian and Ubuntu).

```bash
pnpm install && pnpm build && go build ./...
task check
```

Branch from the latest `master` and merge back through a PR; commits and PR titles follow Conventional Commits. See [AGENTS.md](AGENTS.md) for development, testing, and collaboration rules, and [deploy/dev/README.md](deploy/dev/README.md) for the local database environment.

## License

[MIT](LICENSE)

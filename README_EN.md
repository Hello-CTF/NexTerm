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

- **🤖 AI that does the work**: 20+ tools for commands, files, Docker, and terminal output; commands and diffs are shown before they run, with plan mode, per-host and per-path grants, scheduled tasks, and long-term memory. Works with any OpenAI-compatible API, with DeepSeek, Kimi, GLM, and Ollama presets.
- **🖥️ Persistent terminals**: on desktop, local terminals run by default in a separate local persistent helper, and SSH and managed-device sessions do the same once persistent terminals are enabled: they keep running after you close the tab, quit the app, or lose the network, can be taken over or ended from Background sessions, and can be re-attached from any signed-in device with missed output replayed (the host keeps the most recent 8 MB or so per persistent terminal and drops the oldest beyond that; a reconnect resumes where it left off, while a fresh open first shows only the recent tail). A persistence icon on the tab and a dismissible hint bar say whether a terminal is persistent; a terminal opened while the helper is preparing or unavailable is a regular terminal with the reason shown, and a regular terminal ends on disconnect. Opening a host from the asset tree opens its workspace and prepares the helper in the background without creating a terminal (a quick connection to user@host still opens one right away). Setting the default terminal mode to regular stops that preparation, and a mode set on the asset takes precedence; mobile has no local terminals. Replacing an older helper ends its sessions, so the upgrade shows the count and asks first.
- **🔔 See which session needs me**: badges on tabs and workspaces show session state (running, needs me, finished); the inbox bell in the header lists every session that needs you, including hosts you have no tab open for on this device; desktop can raise system notifications (switchable by kind; with several devices the most recently active one notifies first and the others follow after 60 s). "Needs me" comes from terminal bells, program notifications (OSC 9/777/99) and rules you save yourself; NexTerm does not guess from the screen whether a program is waiting for you and has no buttons for answering prompts, so questions are still answered in the terminal. The mobile home is a status wall grouped into needs me, running, and finished. Choose "这就是需要我" (this is it) or "状态不对" (wrong state) on a tab to save a rule, optionally drafted by the AI from the screen (suspected secrets are redacted on a best-effort basis before it is sent to the model, which does not catch everything, so review the draft). Rules are edited, tested, and reordered in Settings and pushed to the persistent helper.
- **📱 Many devices**: several devices can watch and type into one terminal at once, with no take-control step; the terminal size follows the last device that typed (held briefly) and other devices show a scaled view; a live OSC 52 clipboard write goes only to the last device that typed. Prerequisites: the phone sees a desktop's local terminals only while the desktop app is running (the tray counts) and only after you turn on "Expose local terminals to the server account" in Settings; an SSH-only host is visible from the phone only through the phone's own SSH session or enrolled device agents.
- **🧭 Navigation and history**: the jump palette (Ctrl/Cmd+Shift+P) ranks sessions by how much they need you and shows state badges, with Alt+digit to open directly, Shift+Enter to split to the right, and a "reconnect current workspace" command; a workspace reconnect bar sits at the top. Panes form a nestable split tree, and closing a pane merges its tabs into the adjacent pane (Ctrl/Cmd+Shift+W, Ctrl+Alt+W on the web). Terminal search is Ctrl+Shift+F on non-macOS (Ctrl+F goes to the shell). Local scrollback defaults to 10,000 lines (Settings → Terminal), and a persistent terminal offers "load earlier history" at the top for read-only viewing. SSH liveness uses keepalives plus inbound traffic and declares a connection dead only on a hard error or total silence; you can reconnect manually while it is only suspect.
- **🔄 Sync**: state rules, runbook templates, and AI skills sync with the account by default; AI memory and AI scheduled jobs are separate opt-ins under Settings → Account sync (a job runs only on the device it is bound to); 1.3.0 sync does not interoperate with 1.2.x devices. Exposing local terminals hands this computer's command line, as the current user, to the linked server account: the server's administrators and anyone who controls the server or the account, including through that account's AI assistant, scheduled jobs, and batch execution, can run arbitrary commands, so turn it on only for a server you trust. Also includes shared viewing, command blocks, searchable history and replay, broadcast, and snippets.
- **🧰 Everyday operations**: SFTP, WinRM, and device file browsing with in-place editing; Docker containers, logs, and images; MySQL/MariaDB, PostgreSQL, and Redis; local forwarding and SOCKS5; host import from `~/.ssh/config` or Termius (macOS only) with single-hop `ProxyJump`; add multi-hop entries manually.
- **🛰️ Devices and sharing**: enroll the device agent with one command; devices connect outward and report metrics. Share a terminal as a time-limited public link or with registered users (terminal only, no files, forwarding, or AI), read-only by default and revocable, without handing over passwords or keys; enrolled agents upgrade with `nexterm-agent update` (1.2.x agents lack it and need the install script once); browser mode can grant write access, and public links created on desktop are read-only.
- **🔐 Self-hosted**: multi-user accounts with TOTP; end-to-end encrypted sync; a credential vault (master password, DPAPI/Keychain, or biometrics); audit logs; SQLite, PostgreSQL, MySQL, or MariaDB. In browser mode, terminals and workspaces live on the server and survive closing the page.

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

Mobile builds are previews and are not officially supported until real-device signing is verified. The desktop app checks for updates automatically and verifies them against `SHA256SUMS`. Windows installers are not code-signed; if Windows reports an unknown publisher, choose "More info → Run anyway". If macOS cannot verify the developer, choose "Open Anyway" in System Settings → Privacy & Security; if it reports the app is damaged, run `xattr -dr com.apple.quarantine /Applications/NexTerm.app`. Colour emoji on the Linux desktop come from a system font: the `.deb` recommends `fonts-noto-color-emoji`; if recommended packages were skipped, emoji in tab titles may show as monochrome symbols or boxes, and `sudo apt install fonts-noto-color-emoji` adds the font.

**Quick start**: add an SSH or WinRM asset (or use the built-in "This device") → initialize the vault in Settings → Credential protection before saving passwords or keys → optionally add a model in Settings → AI models → optionally sign in under Settings → Account sync to sync across devices. The protection password only unlocks this device; if you forget it, saved credentials cannot be recovered.

**AI privacy redaction**: off by default. Download the latest released `QuantumNous/astrlink-guard` model in Settings → 信息脱敏, select INT8, FP32, or FP16, then enable it. Model weights are not bundled and are downloaded from Hugging Face. Redaction is unavailable on platforms ONNX Runtime no longer publishes (currently Intel Macs); the rest of the app works. The local inference runtime currently requires Linux (the optional ONNX Runtime library needs glibc 2.27 or newer with libstdc++; musl is not supported), Windows (the installer bundles the VC++ runtime), macOS 13.4+, iOS/iPadOS 15.1+, or Android 7.0+; the application’s minimum OS versions are unchanged. Detection runs on the device running AI, or on the server in browser mode. Placeholders stay stable across turns in a conversation; responses and tool arguments are restored. When enabled, unavailable models or runtimes block requests instead of sending plaintext. Images and other non-text content are not redacted and are sent to the AI provider unchanged; the first image attached while it is enabled shows a notice. Detection can miss sensitive information. The implementation reuses [AstrLink](https://github.com/Calcium-Ion/AstrLink); see the [source and license notice](internal/ai/privacy/third_party/ASTRLINK_NOTICE.md).

## Deploy the server

On Linux with systemd (amd64 / arm64; the server runs on any glibc-based Linux with no minimum glibc version, and musl distributions such as Alpine are not supported; the optional local AI redaction library additionally needs glibc 2.27 or newer with libstdc++), run as root:

```bash
curl -fsSL https://nexterm.work/install.sh | sudo bash
```

The script downloads the latest server archive, verifies it against `SHA256SUMS`, and runs the bundled `install.sh`: it creates the `nexterm` user and directories, generates the master key, creates the `admin` super-admin and prints its initial password once in the installer output (write it down; it must be changed at first login, or preset it with `NEXTERM_ADMIN_PASSWORD`), then starts `nexterm-server`. Running it again upgrades in place with a brief service restart and keeps configuration and data; add `--rollback` to undo the last upgrade. Pin a version with `curl -fsSL https://nexterm.work/install.sh | sudo bash -s -- --version x.y.z`, or download `nexterm-server_linux_<arch>_x.y.z.tar.gz`, extract it, and run `sudo ./install.sh`.

- The service listens on `127.0.0.1:8080` and requires sign-in by default; from your computer, run `ssh -L 8080:127.0.0.1:8080 user@server` and open `http://localhost:8080`; settings live in `/etc/nexterm/nexterm.env`, then run `systemctl restart nexterm-server`.
- `--auth off` disables accounts, administration and device routes; anyone who can reach the listen address can operate the server, so never use it on a public network.
- For public access, put a TLS reverse proxy in front, have it overwrite `X-Forwarded-Proto`, and list its source ranges in `NEXTERM_TRUSTED_PROXY_CIDRS`.
- SQLite is the default; use `NEXTERM_DB=postgres|mysql|mariadb` with `NEXTERM_DB_DSN` for an external database, with one server instance per database.
- Back up `/var/lib/nexterm`, `/etc/nexterm/master.key`, and any external database. See the bundled `README.md` and `nexterm-server --help` for every option.

**LazyCat MicroServer**: install from the App Store (amd64 devices only for now); built-in port forwarding is unavailable, so use the platform's forwarding.

**Device enrollment**: a super-admin sets the enrollment address in Devices (in browser mode, or in the desktop app signed in to a server account) and issues a one-time enrollment code; run the one-line command shown there on the device (as a regular user on Linux/macOS, in PowerShell on Windows). The agent starts automatically at user login. Install records the then-current `SHELL` and `PATH` in the service definition (`nexterm-agent status` shows them; re-run the install command to refresh). PATH entries defined only in `~/.zshrc` are not visible to the agent process, which reads PATH through a non-interactive login shell: put them in `~/.zprofile`. Terminal tabs do read `.zshrc`.

## Development

Toolchain: Go 1.27, Node 24+, pnpm; the Linux server is a `CGO_ENABLED=0` build (ONNX Runtime is loaded through purego; it links glibc dynamically with no version requirement) that can be produced on any host, and the Windows desktop needs no C compiler either. Linux builds and tests in CI and releases run directly on GitHub-hosted Ubuntu runners (`ubuntu-latest`, and `ubuntu-24.04-arm` for arm64); building the Linux desktop needs the GTK 4.14+ and WebKitGTK 6.0 development packages (`libgtk-4-dev` and `libwebkitgtk-6.0-dev` on Debian and Ubuntu).

```bash
pnpm install && pnpm build && go build ./...
task check
```

Branch from the latest `master` and merge back through a PR; commits and PR titles follow Conventional Commits. See [AGENTS.md](AGENTS.md) for development, testing, and collaboration rules, and [deploy/dev/README.md](deploy/dev/README.md) for the local database environment.

## License

[MIT](LICENSE)

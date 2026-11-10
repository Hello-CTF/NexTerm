<div align="center">

<img src="website/public/brand/nexterm-mark-128.png" width="88" alt="NexTerm" />

# NexTerm

**One self-hostable workbench for every server you run.**

[![Website](https://img.shields.io/badge/website-nexterm.work-516cd6)](https://nexterm.work/)
[![Release](https://img.shields.io/github/v/release/Hello-CTF/NexTerm?color=516cd6)](https://github.com/Hello-CTF/NexTerm/releases/latest)
![License](https://img.shields.io/badge/license-MIT-green)

**English** | [中文](README.md) · [Website and live demos](https://nexterm.work/) · [Download](https://github.com/Hello-CTF/NexTerm/releases/latest) · [Deploy the server](#deploy-the-server)

</div>

<picture>
  <source media="(prefers-color-scheme: light)" srcset="website/public/images/shell-light.jpg" />
  <img src="website/public/images/shell-dark.jpg" alt="NexTerm terminal with command blocks (sample data)" />
</picture>

## Features

- **Persistent terminals**: keep running when the app closes or the network drops
- **AI assistant**: diagnoses from terminal, files, and containers; writes wait for you
- **Everyday ops**: remote files, Docker, MySQL / PostgreSQL / Redis, port forwarding
- **Every connection**: SSH, WinRM, local shells, managed devices, RDP / VNC
- **Self-hosted**: multi-user, two-factor auth, audit logs, encrypted sync
- **Nine interface languages**: Simplified and Traditional Chinese, English, Japanese, Korean, German, French, Spanish, Russian

Sync is not end-to-end encrypted: the password reaches the server at sign-in, so sign in only to servers you trust, over HTTPS (see [Security](docs/security.en.md)). See [Features](docs/features.en.md) for the full list.

## Install

| Platform | Package ([Releases](https://github.com/Hello-CTF/NexTerm/releases/latest)) |
| --- | --- |
| Windows (amd64 / arm64) | `nexterm-desktop_windows_<arch>_x.y.z.exe` |
| macOS (Apple Silicon / Intel) | `nexterm-desktop_macos_<arch>_x.y.z.dmg` |
| Linux desktop (amd64 / arm64) | `nexterm-desktop_linux_<arch>_x.y.z.deb` |
| Android (arm64, preview) | `nexterm-desktop_android_arm64_x.y.z.apk` |
| iOS / iPadOS (preview) | [TestFlight](https://testflight.apple.com/join/ds62pzPz) |
| Linux server / LazyCat MicroServer | See [Deploy the server](#deploy-the-server) / App Store (amd64 only) |

Mobile builds are previews and are not officially supported until real-device signing is verified. The desktop app checks for updates and verifies their Minisign signature before installing.

<details>
<summary>Install tips</summary>

- **Windows**: installers are not code-signed; if Windows reports an unknown publisher, choose "More info → Run anyway".
- **macOS**: if macOS cannot verify the developer, choose "Open Anyway" in System Settings → Privacy & Security; if it reports the app is damaged, run `xattr -dr com.apple.quarantine /Applications/NexTerm.app`.
- **Linux desktop**: install with `sudo apt install ./<package>.deb`; it needs GTK 4.14+ and WebKitGTK 6.0, and the `.deb` declares the minimum glibc it requires, so apt refuses older systems. Install `fonts-noto-color-emoji` for color emoji.

</details>

## Quick start

The app UI is in Chinese; Chinese labels are shown in parentheses.

1. Add an SSH or WinRM asset, or use the built-in "This device" (当前设备).
2. Initialize the vault in Settings → Credential protection (凭据保护), then save passwords or keys. The protection password only unlocks this device; if you forget it, saved credentials cannot be recovered.
3. Optionally add any OpenAI-compatible model in Settings → Models (模型).
4. Optionally sign in to a server in Settings → Account and sync (账号与同步) to sync across devices.

## Deploy the server

On a glibc-based Linux with systemd (amd64 / arm64), run:

```bash
curl -fsSL https://nexterm.work/install.sh | sudo bash
```

The installer prints the initial password of the `admin` super-admin once. The service listens on `127.0.0.1:8080` by default: run `ssh -L 8080:127.0.0.1:8080 user@server` and open `http://localhost:8080`. See the [server guide](deploy/systemd/README.md) for upgrades, rollback, backup and restore, reverse proxies, external databases, and multi-user limits.

## More documentation

[Features](docs/features.en.md) · [Security](docs/security.en.md) · [AI privacy redaction](docs/ai-privacy.en.md) · [Device enrollment](docs/device-agent.en.md) · [Server guide](deploy/systemd/README.md) · [Local database environment](deploy/dev/README.md)

## Development

```bash
pnpm install && pnpm build && go build ./...
task check
```

See [AGENTS.md](AGENTS.md) for the toolchain, testing, and collaboration rules.

## License

[MIT](LICENSE)

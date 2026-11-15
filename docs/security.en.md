# Security

**English** | [中文](security.md) · [Back to README](../README_EN.md)

## What sync encryption covers

Sync objects are encrypted on the client with the account's data key before upload, and the data key is stored on the server wrapped by a key derived from the account password. The server stores only ciphertext and never persists plaintext, so a leaked database or backup does not expose plaintext directly, though a weak password can still be guessed offline.

This is not end-to-end encryption: the password is sent to the server at sign-in, so the server operator, or anyone who can intercept a sign-in request, can use it to decrypt the synced data. Sign in only to servers you trust, over HTTPS.

## Stored credentials

- Desktop and mobile use a protection password or OS-level protection (DPAPI / Keychain), with biometric unlock on mobile and idle auto-lock. The protection password only unlocks this device; if you forget it, saved credentials cannot be recovered.
- In browser mode, credentials are encrypted by the server-side vault protected by its master key, and the server can decrypt them to connect. Back up `/etc/nexterm/master.key`.

## Update signatures

Before installing an update, the desktop app verifies the Minisign signature of `SHA256SUMS` and checks the installer against it. The device agent's self-upgrade verifies the Minisign signature too, and development builds without an embedded signing key refuse to self-upgrade. Windows installers carry no Authenticode signature.

## Server

- `--auth off` disables accounts, administration and device routes; anyone who can reach the listen address can operate the server, so never use it on a public network.
- A multi-user server does not run commands on the server machine or read its files on an account's behalf. See the [server guide](../deploy/systemd/README.md) for reverse proxy and `X-Forwarded-For` trust rules and the scope of login rate limits.
- A VNC connection is unencrypted when the server offers no X.509 TLS, so route it through an SSH host; RDP and VNC TLS certificates are pinned after you confirm the fingerprint once.
- RDP accepts only TLS 1.2 and newer by default; TLS 1.0/1.1 is accepted only when the asset turns on "Allow legacy TLS (1.0/1.1)", meant for old hosts that cannot be upgraded, and such connections are marked legacyTLS in the audit log.

## AI privacy redaction

Off by default. In Settings → AI privacy redaction (AI 信息脱敏), download the latest released `QuantumNous/astrlink-guard` model (downloaded from Hugging Face; weights are not bundled), select INT8, FP32, or FP16, then enable it.

- When enabled, an unavailable model or runtime blocks requests instead of sending plaintext; images and other non-text content are not redacted and go to the AI provider unchanged; detection can miss sensitive information.
- Placeholders stay stable within a conversation, and responses and tool arguments are restored automatically.
- Local inference needs Linux (glibc 2.27 or newer with libstdc++; musl is not supported), Windows, macOS 13.4+, iOS/iPadOS 15.1+, or Android 7.0+. Redaction is unavailable on platforms ONNX Runtime no longer publishes (currently Intel Macs); the rest of the app works.
- Detection runs on the device running AI, or on the server in browser mode.

The implementation reuses [AstrLink](https://github.com/Calcium-Ion/AstrLink); see the [source and license notice](../internal/ai/privacy/third_party/ASTRLINK_NOTICE.md).

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

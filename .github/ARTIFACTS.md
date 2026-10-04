# Go/Wails artifact and acceptance contract

## Entry points

- `node scripts/build.mjs frontend --repro-check [--base=...]`: typecheck and build the real Vite output twice; the complete tree hash must match.
- `node scripts/build.mjs bindings`: generate Wails alpha.98 bindings into a temporary directory and require byte-for-byte equality with `src/ipc/bindings`.
- `node scripts/build.mjs desktop --release --os=... --arch=... [--smoke] [--package]`: build the composed Go desktop with `-mod=readonly -trimpath -buildvcs=false -ldflags '-s -w'` and stamped version/commit metadata. These are Go-official size optimizations only: the pclntab and build info stay intact for stack symbolization, and UPX or manual runtime-metadata stripping is forbidden. Release desktops must contain the exact production HTML and hashed JavaScript bytes.
- `node scripts/build.mjs server --release --os=linux --arch=amd64|arm64`: build a `CGO_ENABLED=0` server with the same `-trimpath`/`-ldflags '-s -w'` flags and reject ELF `PT_DYNAMIC`.
- `bash scripts/pack-linux-server.sh --arch=...`: validate the same binary and produce one deterministic full archive containing the frontend, both systemd units and both env examples. Restricted `--sync-only` is a runtime choice in that archive, never a separate package.
- `python3 scripts/e2e-sync-local.py`: real two-process Go sync and sync-only acceptance. It has no Rust compatibility mode and no skip-as-pass mode.
- `node .github/scripts/browser-acceptance.mjs`: real-Chromium layout and WebSocket/RPC acceptance; its report separates automated passes from real-target gaps.

`wails.json.info.version` is the sole packaging version. Git tags must be `v` plus that value. `package.json`, release notes, artifact names, NSIS metadata and macOS plist metadata are never independent version inputs.

## Native cgo matrix

| Target | cgo | Native execution |
| --- | --- | --- |
| Windows/amd64 and arm64 desktop | disabled | WebView2 smoke required |
| macOS/amd64 and arm64 desktop | Wails platform layer only | WKWebView smoke required |
| Linux/amd64 and arm64 desktop | Wails platform layer only | GTK4/WebKitGTK 6.0 smoke under Xvfb required |
| Linux/amd64 and arm64 server | disabled; static ELF | two-process full and restricted HTTP smoke required |

Cross-compiled output is not native acceptance. A missing smoke implementation, browser, runtime or target produces a failed/not-run result, never a pass. The matrix contains no other architectures.

## Artifact IDs and reports

The eight pre-release packages are exactly: `NexTerm_<v>_x64-setup.exe`, `NexTerm_<v>_arm64-setup.exe`, `NexTerm_<v>_x86_64.dmg`, `NexTerm_<v>_aarch64.dmg`, `NexTerm-desktop_<v>_linux_amd64.tar.gz`, `NexTerm-desktop_<v>_linux_arm64.tar.gz`, `NexTerm-server_<v>_linux_amd64.tar.gz` and `NexTerm-server_<v>_linux_arm64.tar.gz`. Linux desktop tarballs contain the self-contained Wails binary, LICENSE and README; no AppImage/deb is produced.

Every raw binary and final installer/archive gets an adjacent `*.artifact.json`. IDs are `desktop-OS-ARCH` / `server-OS-ARCH`, `desktop-dmg-OS-ARCH` / `desktop-nsis-OS-ARCH` / `desktop-linux-archive-OS-ARCH`, and `server-archive-full-OS-ARCH` (the report command's explicit `--id` may be used by M47). `sync-archive` and `NexTerm-onlyServer-*` are invalid release artifacts; requesting them fails rather than creating a misleading report. Reports contain exact bytes, SHA256, format/architecture/cgo assertions, build provenance, an informational Rust comparison, an informational custom-Go/Eino comparison, and explicit non-claims for feature parity and real-target acceptance.

The full Linux archive report independently lists the tar and requires `nexterm-server`, `web/index.html`, `nexterm-server.service`, `nexterm-onlyserver.service`, `nexterm.env.example`, `onlyserver.env.example`, `LICENSE` and `README.md`. The restricted runtime's three-command RPC and `/rpc` 404 behavior remains covered by live server tests even though no restricted archive is published.

Rust references come from the official same-form release file `.github/baselines/rust-official-v0.2.1.json` (GitHub release v0.2.1 assets, SHA-256 verified by download); each comparison records its provenance. When the official file has no entry for an artifact, the comparison is reported as `unavailable`, never fabricated. Package types and architectures are never substituted for one another. The Go linux-server archive intentionally differs in content from the Rust single-binary archive (both runtime units and both env examples); the whole-archive comparison is the like-for-like reference and that content difference is stated in the baseline file and the artifact report. Every size comparison is informational only and never gates release.

`.github/baselines/custom-go.json` accepts only entries of this form:

```json
{
  "artifacts": {
    "desktop-darwin-arm64": {
      "size_bytes": 16000000,
      "like_for_like": true,
      "feature_set": "full-pre-eino",
      "source": "commit and measurement evidence"
    }
  }
}
```

A skeleton or feature-reduced binary is invalid. `full-pre-eino` is measured at commit 3e39138 (the parent of b4c5a2b): the complete production composition — desktop core, sync boundary, durable tmux recovery and vault — as it stood immediately before the full-Eino AI runtime adoption, which landed in the same commit series; the AI runtime did not exist yet at that tree, so no feature-removing build tag is used. The measurement reports the full-Eino size delta only; it is not a claim of strict like-for-like composition with the final release, and each entry's `source` records the commit and this ruling. Per the 2026-10-03 user decision, Rust and custom-Go size comparisons are informational only: the release is measured against both references for the record, a missing reference is reported as `unavailable` (never fabricated), and a larger Go artifact is recorded honestly with its delta and ratio — none of these ever fails a build. Release readiness is decided by real checks: artifact integrity (reported SHA-256/size match), target identity (artifact id and version), and the content/static/cgo assertions; `--require-evidence` enforces those per artifact. Final draft publication additionally requires every contracted package and all final and supporting raw reports to pass the same checks.

## M47 LazyCat and Pages inputs

M47 owns LazyCat/deploy/website/store files. It should consume the Linux static binary, the single full archive, artifact reports, `scripts/verify-manifest-injects.py`, the shared frontend command and `.github/workflows/pages.yml`; it must not rebuild a second Rust/Tauri delivery chain. LazyCat LPK measurements belong in the same report model, using the recorded Rust LPK anchors without treating them as new same-host measurements. Website/download surfaces must not offer `NexTerm-onlyServer-*`; they should point to the full archive and document the optional `nexterm-onlyserver.service` unit for restricted mode.

`.github/acceptance/real-target-gaps.json` is shipped as release evidence. Physical mobile/native soft keyboard, real NSIS/DMG installation, LazyCat box/store and deployed Pages acceptance remain open until their owning acceptance runs supply evidence. Local builds, jsdom and desktop/server CI do not close these gaps.

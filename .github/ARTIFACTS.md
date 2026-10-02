# Go/Wails artifact and acceptance contract

## Entry points

- `node scripts/build.mjs frontend --repro-check [--base=...]`: typecheck and build the real Vite output twice; the complete tree hash must match.
- `node scripts/build.mjs bindings`: generate Wails alpha.98 bindings into a temporary directory and require byte-for-byte equality with `src/ipc/bindings`.
- `node scripts/build.mjs desktop --release --os=... --arch=... [--smoke] [--package]`: build the composed Go desktop with `-mod=readonly -trimpath -buildvcs=false` and stripped version/commit metadata. Release desktops must contain the exact production HTML and hashed JavaScript bytes.
- `node scripts/build.mjs server --release --os=linux --arch=amd64|arm64`: build a stripped `CGO_ENABLED=0` server and reject ELF `PT_DYNAMIC`.
- `bash scripts/pack-linux-server.sh --arch=...`: validate the same binary and produce both full and sync-only archives with deterministic tar metadata.
- `python3 scripts/e2e-sync-local.py`: real two-process Go sync and sync-only acceptance. It has no Rust compatibility mode and no skip-as-pass mode.
- `node .github/scripts/browser-acceptance.mjs`: real-Chromium layout and WebSocket/RPC acceptance; its report separates automated passes from real-target gaps.

`wails.json.info.version` is the sole packaging version. Git tags must be `v` plus that value. `package.json`, release notes, artifact names, NSIS metadata and macOS plist metadata are never independent version inputs.

## Native cgo matrix

| Target | cgo | Native execution |
| --- | --- | --- |
| Windows/amd64 desktop | disabled | WebView2 smoke required |
| macOS/arm64 and amd64 desktop | Wails platform layer only | WKWebView smoke required |
| Linux/amd64 desktop | Wails platform layer only | WebKitGTK smoke under Xvfb required |
| Linux/amd64 and arm64 server | disabled; static ELF | two-process HTTP smoke required |

Cross-compiled output is not native acceptance. A missing smoke implementation, browser, runtime or target produces a failed/not-run result, never a pass.

## Artifact IDs and reports

Every raw binary and final installer/archive gets an adjacent `*.artifact.json`. IDs are `desktop-OS-ARCH` / `server-OS-ARCH`, `desktop-dmg-OS-ARCH` / `desktop-nsis-OS-ARCH`, and `server-archive-full-OS-ARCH` / `sync-archive-sync-OS-ARCH` (the report command's explicit `--id` may be used by M47). Reports contain exact bytes, SHA256, format/architecture/cgo assertions, build provenance, Rust comparison, custom-Go/Eino comparison, and explicit non-claims for feature parity and real-target acceptance.

Rust references come from the pinned `docs/acceptance-rwig/baseline/baseline.json` and the identified historical Linux server entry. Historical measurements are labelled, not silently described as same-host. Package types and architectures are never substituted for one another.

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

A skeleton or feature-reduced binary is invalid. The full-Eino release is measured against this custom-Go baseline and must independently be strictly smaller than every like-for-like Rust counterpart. Missing custom-Go or Rust evidence makes the report `evidence-gap`; `--require-size` turns either gap or a failed size comparison into a release failure. Final draft publication additionally requires all seven packages and all their reports to pass.

## M47 LazyCat and Pages inputs

M47 owns LazyCat/deploy/website/store files. It should consume the Linux static binary, full archive, artifact reports, `scripts/verify-manifest-injects.py`, the shared frontend command and `.github/workflows/pages.yml`; it must not rebuild a second Rust/Tauri delivery chain. LazyCat LPK measurements belong in the same report model, using the recorded Rust LPK anchors without treating them as new same-host measurements.

`.github/acceptance/real-target-gaps.json` is shipped as release evidence. Physical mobile/native soft keyboard, real NSIS/DMG installation, LazyCat box/store and deployed Pages acceptance remain open until their owning acceptance runs supply evidence. Local builds, jsdom and desktop/server CI do not close these gaps.

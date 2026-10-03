# RWIG feature, Go self-consistency, and size baseline

Captured on macOS 26.6.2 arm64. The pinned Rust feature/artifact baseline is `25c2fa003cc05f04347b1d33c4d4be25f820f870`; the Go skeleton was merged at `9fea2bb375c1e3fe24c2bb42c815cba4cdbd79f0`, and each Go report embeds the exact worktree HEAD used to generate it. The Go selfcheck/coverage reports were regenerated on 2026-10-03 against `feat/rwig-release-lazycat-r12` @ `4e3d491` (rwig `6f8847e` + delivery chain); `go-selfcheck.json`/`go-coverage.json` name their exact source.

This baseline follows the latest goal: **Go must be internally consistent; Rust backward compatibility and Rust↔Go wire/database differential work are not required**. Rust remains the feature checklist and the test/performance/artifact-size reference. No Rust IPC, vault, sync, or old-database golden is shipped here. Pending features, missing artifacts, skips, and environment-only checks are not passes.

## Reproduce

```bash
# Rust test/frontend/release/benchmark baseline; needs Rust >= 1.98 and pnpm dependencies
python3 scripts/parity/run_baseline.py

# Go-native race tests, builds, loopback smoke, feature coverage, and size gates
python3 scripts/parity/go_selfcheck.py --go-root /path/to/go/module

# Re-run Go checks, then validate every generated file and Python test
python3 scripts/parity/verify.py --go-root /path/to/go/module
```

`python3 scripts/parity/verify.py --static-only` validates only existing files/reports and deliberately prints that no Go runtime check was requested. Without a Go module root or `--static-only`, verification exits 2 instead of silently skipping Go checks.

The initial pre-merge check used the M9 worktree read-only; after rebasing onto the merged `rwig`, the final verification used this repository root directly. Root `go.mod` and `go.sum` were not modified. Go used `go1.26.8`; dependency, toolchain, and build caches were redirected to this worktree's ignored `target/`. Frontend packages used a store under `node_modules/`.

The host's default Rust 1.94.1 cannot build locked `winrm-rs 1.2.2` (requires 1.98). An official Rust 1.98.0 toolchain was installed under `target/parity-toolchain`, and its archive SHA256 was verified as `5395f380bd220890b89b7e8ce137f3965cf0625b6f561af11e97c8d5c8743c3b`; the system toolchain was not changed.

## Feature inventory: current accounting (regenerated 2026-10-03)

Machine-readable source: `testdata/parity/inventory.json`; regenerate/check with `python3 scripts/parity/inventory.py` / `--check`.

- Rust backend registry: **139 unique commands**.
- Frontend facade: **141 methods mapping to 140 unique commands**.
- `ai_presets` has two frontend wrappers (`aiApi.presets`, `modelApi.presets`), explaining 141 methods versus 140 unique names.
- `credential_save` is the one backend command without a frontend wrapper (rust_only).
- `frontend_only` = **`ai_answer`, `terminal_resize_flush`**: M20 established `frontend_only=[ai_answer]` at 140 methods / 139 unique; the terminal-grid line later added the `terminal_resize_flush` facade (current 141/140). Therefore **139 Rust = 138 frontend-unique overlap + `credential_save`; 141 frontend methods = 140 unique + duplicate `ai_presets` wrapper**.
- `onlyServer` is a separate three-command access-control subset: `sync_digest`, `sync_export`, `sync_import`; it does not explain the extra command.
- Named events: 10 Rust declarations including `layout://changed`, 9 frontend declarations, 10 in the union. `sync://status` is explicitly reserved.
- Stream features: five binary commands and two ordered AI JSON commands, with 14 AI event variant labels in the feature inventory.

⚠️ The checked-in `testdata/parity/inventory.json` still carries the M20 state (140 methods / 139 unique, `frontend_only=[ai_answer]`); `inventory.py --check` fails on current HEAD until it is regenerated. The counts above are what the generator produces from the current source tree.

These canonical IDs are checklist labels, not a requirement to preserve Rust wire names. `testdata/parity/go-aliases.json` allows a Go implementation to use different internal names while retaining feature coverage.

## Rust baseline results

Full command results, environment, hashes, skip evidence, and raw logs are in `baseline.json` and `logs/`.

All ten baseline commands passed: desktop Rust tests, server-mode Rust tests, frontend typecheck, frontend lint, frontend build, terminal benchmark, desktop release, macOS app/DMG bundle, server release, and the real local two-instance sync E2E.

| Measurement | Result |
| --- | ---: |
| Desktop tests | 279 runner-reported passes - 6 early returns = **273 executed passes** |
| Server-mode tests | 301 runner-reported passes - 6 early returns = **295 executed passes** |
| Ignored entries in each mode | 1 live-server product test + 2 documentation examples; not counted as executed |
| Sync E2E | **34/34 assertions** |
| Terminal UTF-8 throughput | **149.3 MB/s** |
| Terminal GBK throughput | **149.7 MB/s** |
| Scrollback after 100 MiB input | **33,554,432 bytes**, exact 32 MiB cap |
| Screen after input | **120x40, 40 lines**, no ANSI residue failure |

Same-host release artifacts (strict byte counts and SHA256 are in `baseline.json`):

| Artifact | Bytes |
| --- | ---: |
| macOS/arm64 desktop `target/parity/release/nexterm` | 26,095,616 |
| macOS/arm64 server `target/parity/release/nexterm-server` | 17,172,784 |
| macOS/arm64 DMG `NexTerm_0.2.1_aarch64.dmg` | 14,987,015 |

### Coverage not counted as passed

- SSH PTY E2E and 10-session RSS/throughput: no SSH target or real credentials were used.
- Four macOS Keychain tests per mode: `NEXTERM_TEST_KEYCHAIN=1` was deliberately not set; personal Keychain data was not touched.
- `local_pty_keeps_utf8_filenames`: host has `LANG=zh_CN.UTF-8`, so its empty-locale precondition is not met.
- Windows DPAPI tests: not compiled on this macOS host; require a native Windows run.
- `sync::client::tests::live_server_token_is_enough`: `#[ignore]` because it needs a real server/token.
- Native Windows/Linux artifacts and a new LazyCat LPK were not produced. Only macOS/arm64 was available; LPK requires the external LazyCat builder/box. Cross-compilation is not presented as native acceptance.

The local OpenSSH SFTP backup test did run and pass; it is not in the skip list. `account_tests.py` checks that every runner-reported ignored entry and early-return skip is individually accounted for.

## Official v0.2.1 same-form references (M26 survey)

Machine-readable source: `.github/baselines/rust-official-v0.2.1.json` (every entry re-downloaded from the public GitHub release and matched against the API SHA-256 digest). Release `v0.2.1`, commit `59889b28ab905809d148a642cadcaf46e12e7b08`, published 2026-10-02T07:55:33Z. These entries take priority over the same-host `baseline.json` and the documented historical anchors.

Of the eight final release artifacts, exactly **three** have an official same-form Rust reference:

| Final artifact | Official reference | Bytes | SHA-256 |
| --- | --- | ---: | --- |
| Windows desktop amd64 NSIS | `NexTerm_0.2.1_x64-setup.exe` | 6,967,558 | `52d43d74c8f0c36390d64313d8966aad94cf5016de0714be73d686ba0c1d8c62` |
| macOS desktop arm64 DMG | `NexTerm_0.2.1_aarch64.dmg` | 14,973,100 | `7c1f1ffee95930a33a924f05ebaeb36640decc3b63003728c1764bc9dea579df` |
| Linux server amd64 archive | `NexTerm-0.2.1-linux-amd64.tar.gz` | 7,858,105 | `81fe2495191a9bbf2d6ba92ebc90f6c18de96cd07e40b034b4ac3d88b831a394` |

The Linux server archive comparison is whole-archive like-for-like; its inner `nexterm-server` ELF is 17,360,408 B (`070d3b8289fdd186a96016444ab3fe9c4e35b126bcfd2242f9bfd166c7d67f12`). The Rust archive is a single-binary full server; the Go full archive intentionally carries both runtime units and both env examples (restricted `--sync-only` is a runtime mode, not a separate onlyServer package). Whole-archive bytes remain the comparison rule and the content difference is stated, never hidden.

The other **five** final artifacts are **genuinely unavailable** — no official Rust release (v0.1.0–v0.2.1) ever produced them: Windows desktop arm64 NSIS, macOS desktop x86_64 DMG, Linux desktop amd64/arm64 archives, Linux server arm64 archive. Per the release contract they stay evidence-gaps and must never be reported as passed.

LazyCat LPK: no official release artifact exists (no `.lpk` was ever attached to a GitHub release; the store page exposes no verifiable listing without credentials). The recorded anchors are repo-documented values only — v0.1.3 at 15.97 MiB (embedded layer 15,918,870 B, sha256 `d90395a843f255e427679eaf7e376ac3d3cf3f8c92f6e693fe15b079ccfab742`) and dev-v0.2.0 at 18,718,208 B — and re-verification needs `lzc-cli`/box access.

## Prior documented anchors

`historical-metrics.json` preserves prior reports with exact source lines and reproduction commands. They are explicitly not new same-host measurements:

- Windows terminal engine: 43.4 MB/s UTF-8, 39.0 MB/s GBK, 32 MiB scrollback cap (`docs/bench-m0.md`).
- Prior macOS desktop binary: 25,992,160 B.
- Prior Linux/amd64 server binary: 16,337,808 B.
- Prior LPK reports: v0.1.3 at 15.97 MiB with SHA256; dev-v0.2.0 at 18,718,208 B.
- Prior Windows idle process-tree RSS range: approximately 40–90 MB including WebView2.

## Go self-consistency evidence (regenerated 2026-10-03)

`go-selfcheck.json` is a Go-only report; its expected behavior comes from the Go API and live Go runtime, not Rust payload goldens. The 2026-10-03 run against `feat/rwig-release-lazycat-r12` @ `4e3d491` passed all eight steps: module resolution, tidy in a temporary harness copy, Go 1.26.8 selection, race-enabled external IPC tests, production command-surface probe, CGO_ENABLED=0 server build, live full/sync-only loopback server smoke, and macOS desktop build.

Live command surface (surfaceprobe composing `production.NewProduction`, cross-checked by `/healthz`): **full server 140 commands, sync-only 3 commands** (`sync_digest`, `sync_export`, `sync_import`). The 140 = 138 canonical-overlap + `ai_answer` + `terminal_resize_flush`; `credential_save` is not implemented in Go.

Feature acceptance is **not complete**: `go-coverage.json` records **2/139 commands verified** (`app_info`, `app_platform`), **136 implemented-unverified**, **1 not-implemented** (`credential_save`), **0/10 production events**, and **0/7 production streams**; `feature_complete=false`. Registration alone is implemented-unverified, never verified. Generic adapter tests do not count as production-feature passes.

Raw-binary size gates **fail** against the current composed production binaries (the strict smaller-than-Rust rule):

| Artifact | Rust bytes | Go bytes | Go/Rust ratio | Gate |
| --- | ---: | ---: | ---: | --- |
| macOS desktop | 26,095,616 | 40,808,562 | 1.563809 | **failed** |
| macOS server | 17,172,784 | 37,711,218 | 2.195987 | **failed** |

The pre-Eino custom-Go baseline (`.github/baselines/custom-go.json`, pinned at `3e39138`, full-feature like-for-like) puts the full-Eino delta at +7,270,400 B for `server-linux-amd64` (34,844,834 → 42,115,234 B); no pre-Eino same-form measurement exists for `server-darwin-arm64` (evidence-gap). These deltas report size impact only; they never waive the Rust gate. The skeleton-era pass table (0.50×/0.35×) is retained in git history and is superseded by the failed gates above. Go DMG/NSIS/LPK package sizes are not produced by this raw-binary selfcheck, so no installer-package size pass is claimed.

## Extend the harness

- Add feature-specific passing evidence to `runtime.verified_commands`, `runtime.verified_events`, and `runtime.verified_streams` in a Go selfcheck report, then run `python3 scripts/parity/coverage.py`. Registration alone becomes `implemented-unverified`, never `verified`.
- Use aliases only to map canonical feature IDs to Go-internal names; they do not weaken the requirement to supply real feature evidence.
- Enforce full feature coverage with `coverage.py --require-complete`; the current report intentionally fails that gate.
- Enforce a strict artifact comparison with `size_gate.py --require` or `go_selfcheck.py --require-size`. Equal/larger/missing artifacts do not pass.
- `go_selfcheck.py --require-features` is the final feature gate; it is not used to disguise the current 2/139 state.

Useful checks:

```bash
python3 scripts/parity/coverage.py --check
python3 scripts/parity/size_gate.py \
  --gate rust-server-macos-arm64=target/parity-go-artifacts/nexterm-server \
  --output docs/acceptance-rwig/baseline/go-server-size.json \
  --require
```

This is reusable baseline infrastructure, not final all-feature or release acceptance.

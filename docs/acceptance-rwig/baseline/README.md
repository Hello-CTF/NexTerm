# RWIG feature, Go self-consistency, and size baseline

Captured on macOS 26.6.2 arm64. The pinned Rust feature/artifact baseline is `25c2fa003cc05f04347b1d33c4d4be25f820f870`; the Go skeleton was merged at `9fea2bb375c1e3fe24c2bb42c815cba4cdbd79f0`, and each Go report embeds the exact worktree HEAD used to generate it.

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

## Feature inventory: exact 139/138 accounting

Machine-readable source: `testdata/parity/inventory.json`; regenerate/check with `python3 scripts/parity/inventory.py` / `--check`.

- Rust backend registry: **139 unique commands**.
- Frontend facade: **139 methods mapping to 138 unique commands**.
- `ai_presets` has two frontend wrappers (`aiApi.presets`, `modelApi.presets`), explaining 139 methods versus 138 unique names.
- `credential_save` is the one backend command without a frontend wrapper. Therefore **139 Rust = 138 frontend unique + `credential_save`**; no frontend-only command exists.
- `onlyServer` is a separate three-command access-control subset: `sync_digest`, `sync_export`, `sync_import`; it does not explain the extra command.
- Named events: 10 Rust declarations including `layout://changed`, 9 frontend declarations, 10 in the union. `sync://status` is explicitly reserved.
- Stream features: five binary commands and two ordered AI JSON commands, with 14 AI event variant labels in the feature inventory.

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

## Prior documented anchors

`historical-metrics.json` preserves prior reports with exact source lines and reproduction commands. They are explicitly not new same-host measurements:

- Windows terminal engine: 43.4 MB/s UTF-8, 39.0 MB/s GBK, 32 MiB scrollback cap (`docs/bench-m0.md`).
- Prior macOS desktop binary: 25,992,160 B.
- Prior Linux/amd64 server binary: 16,337,808 B.
- Prior LPK reports: v0.1.3 at 15.97 MiB with SHA256; dev-v0.2.0 at 18,718,208 B.
- Prior Windows idle process-tree RSS range: approximately 40–90 MB including WebView2.

## Go self-consistency evidence

`go-selfcheck.json` is a Go-only report; its expected behavior comes from the Go API and live Go runtime, not Rust payload goldens.

Seven steps passed against the merged Go skeleton: module resolution, tidy in a temporary harness copy, Go 1.26.8 selection, race-enabled external IPC tests, CGO_ENABLED=0 server build, live full/sync-only loopback server smoke, and macOS desktop build. Tests cover duplicate-safe registration, nested/raw dispatch, structured errors, explicit null results, HTTP method/body handling, ten unique event topics, ordered binary/typed-JSON streams, channel forms, health responses, `app_info`, `app_platform`, unknown-command errors, and sync-only `/rpc` 404 behavior.

Raw-binary size gates pass, but they are only preliminary skeleton measurements:

| Artifact | Rust bytes | Go bytes | Go/Rust ratio | Gate |
| --- | ---: | ---: | ---: | --- |
| macOS desktop | 26,095,616 | 13,171,650 | 0.504746 | pass |
| macOS server | 17,172,784 | 5,952,834 | 0.346644 | pass |

Feature acceptance is **not complete**: `go-coverage.json` records **2/139 commands**, **0/10 production events**, and **0/7 production streams** with passing feature-specific evidence. Only `app_info` and `app_platform` are verified. Generic adapter tests do not count as production-feature passes. Go DMG/NSIS/LPK packages were not produced by this raw-binary selfcheck, so no installer-package size pass is claimed.

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

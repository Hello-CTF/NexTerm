# R37 Go release size optimization: `-trimpath` + `-ldflags "-s -w"`

Measured 2026-10-03 on macOS 26.6.2 arm64 (Darwin 25.6.0), Go 1.26.8, Node v24.14.1, pnpm 10.33.0, Wails CLI v3.0.0-alpha.98, at worktree commit `aec0e9a7dc77ebd54c4f6d31925a0b3765691d07` (branch `feat/rwig-release-go-optimization-r37`). The R37 diff itself touches no Go source, so these measurements remain valid for the merged tree.

## Decision (user, verbatim)

> Rust 历史体积只作参考，不作为发布门槛；可以采用 Go 官方 -trimpath 和 -ldflags "-s -w" 等不删功能的常规优化，体积数据必须如实报告。

Consequences applied across the release pipeline:

- `-trimpath` and `-ldflags "-s -w"` are the only size optimizations. They are Go-official, feature-preserving flags. UPX, manual symbol-table surgery, and pclntab/build-info stripping are forbidden; the runtime metadata needed for stack symbolization stays intact (evidence below).
- Rust size references (official v0.2.1 assets, pinned same-host baseline, historical anchors) are informational only. A larger Go artifact is recorded honestly with delta and ratio; a missing reference is reported as `unavailable` and never fabricated. Neither ever fails a build.
- Release readiness is decided by real checks: artifact integrity (reported SHA-256/size match), target identity (artifact id and version), and the content/static/cgo assertions.

## Flag uniformity

Every desktop/server build path compiles with `-mod=readonly -trimpath -buildvcs=false -tags production[,smoke] -ldflags "-s -w -X …Version -X …Commit"` (plus `-H windowsgui` for Windows desktop), with the existing platform cgo rules unchanged (server and Windows desktop `CGO_ENABLED=0`; macOS/Linux desktop cgo enabled for the Wails platform layer only):

- `scripts/build.mjs` `buildBinary()` (all release desktop/server artifacts; verified by the logged build commands and the `-trimpath=true` build-setting below).
- `scripts/e2e-sync-local.py` and `scripts/parity/go_selfcheck.py` already carried the same flags; no change was needed there.
- `SOURCE_DATE_EPOCH` reproducibility (`--repro-check`) is unaffected: two consecutive builds produce byte-identical binaries (SHAs below).

## Before/after sizes

Method: "after" numbers come from the official `scripts/build.mjs … --release` path; "before" numbers come from direct `go build` invocations with byte-identical env/tags/version stamps minus `-trimpath` and `-s -w`. The methodology is validated: direct builds with the full flag set reproduce the official binaries byte-for-byte (e.g. desktop darwin/arm64 sha256 `b70d25514a4a26f98498dac210ac42a55d82fbb6670f6265065a0514113d78ce` both ways; server linux/amd64 42,225,826 B both ways).

Server (`-tags production`, `CGO_ENABLED=0`):

| Target | Before (no flags) | `-trimpath` only | `-s -w` only | Release (both) | Saved | Saved % |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| linux/amd64 | 60,200,607 | 60,057,285 | 42,340,514 | **42,225,826** | 17,974,781 | 29.9% |
| linux/arm64 | 53,065,674 | — | — | **36,634,786** | 16,430,888 | 31.0% |
| darwin/arm64 | 55,256,754 | — | — | **37,813,794** | 17,442,960 | 31.6% |

Desktop (`-tags production`, frontend embedded; darwin with cgo, windows with `CGO_ENABLED=0`):

| Target | Before (no flags) | `-trimpath` only | `-s -w` only | Release (both) | Saved | Saved % |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| darwin/arm64 | 59,586,706 | 59,454,562 | 40,875,634 | **40,760,050** | 18,826,656 | 31.6% |
| windows/amd64 | 66,152,960 | — | — | **47,453,696** | 18,699,264 | 28.3% |

Notes:

- The official darwin/arm64 desktop release binary is 40,760,066 B because the CI-parity smoke build adds `-tags smoke` (+16 B for the smoke entry point); the 40,760,050 B figure above uses production tags only, matching the shipped non-smoke artifact form.
- Attribution: `-s -w` (DWARF + symbol table removal) contributes ~17.9–18.8 MB; `-trimpath` (build-path strings) contributes ~0.13–0.15 MB. No functionality is removed by either flag.
- Linux desktop (cgo/GTK) was not built on this macOS host (no GTK cross-toolchain); it shares the identical `buildBinary` flag path and is built in CI.

## go version -m / static / cgo evidence

`go version -m target/go-build/nexterm-server-linux-amd64` (build settings):

```
build	-buildmode=exe
build	-compiler=gc
build	-tags=production
build	-trimpath=true
build	CGO_ENABLED=0
build	GOARCH=amd64
build	GOOS=linux
build	GOAMD64=v1
```

`file` + marker checks:

| Binary | `file` summary | cgo | static |
| --- | --- | --- | --- |
| server linux/amd64 (after) | ELF 64-bit LSB executable, x86-64, statically linked, **stripped** | `CGO_ENABLED=0` | no PT_DYNAMIC (`static-linux-server` assertion passed) |
| server linux/amd64 (before) | ELF 64-bit LSB executable, x86-64, statically linked, **with debug_info, not stripped** | `CGO_ENABLED=0` | — |
| server linux/arm64 (after) | ELF 64-bit LSB executable, ARM aarch64, statically linked, **stripped** | `CGO_ENABLED=0` | no PT_DYNAMIC |
| server darwin/arm64 (after) | Mach-O 64-bit executable arm64 | `CGO_ENABLED=0` | n/a |
| desktop darwin/arm64 (after) | Mach-O 64-bit executable arm64 | `CGO_ENABLED=1` (Wails platform layer) | n/a |
| desktop windows/amd64 (after) | PE32+ executable (GUI) x86-64 | `CGO_ENABLED=0` | n/a |

- No DWARF: neither `.debug_info` nor `.zdebug_info` markers exist in the release binaries (`stripped-release` assertion passed).
- `-trimpath` effect: the before binary embeds 2,291 absolute host-path strings (`/Users/huangzheng/…`); the after binary embeds 0 — paths appear only in module form (`github.com/ProbiusOfficial/NexTerm/…`).
- Reproducibility after the change (`--repro-check`, two consecutive builds, identical SHA-256): server linux/amd64 `97a27b77a3104f6b8a31d7bee9e0b80698718b9dfb4140dde93d593dd98c7455`, server linux/arm64 `5928ed59f49eee945333f00e92929a979484e485a898afce238e15f939ece35a`, server darwin/arm64 `a5de83a0674a32b2d965fd396cc4fa521bc98e0bfe704dbcd7406bb07a924f40`, desktop darwin/arm64 `b70d25514a4a26f98498dac210ac42a55d82fbb6670f6265065a0514113d78ce`.

## Stack metadata (pclntab) evidence

The stripped release binaries keep the runtime function-name table: `strings -a` finds both `main.main` and `runtime.main` in `nexterm-server-linux-amd64`, `nexterm-desktop-darwin-arm64`, and `nexterm-desktop-windows-amd64.exe`. End-to-end symbolization probe — a minimal program in this module built with the identical flags (`-trimpath -ldflags "-s -w"`) still produces a fully symbolicated panic:

```
panic: r37 stack metadata probe

goroutine 1 [running]:
main.r37Inner(...)
	github.com/ProbiusOfficial/NexTerm/target/size-r37/panicdemo/main.go:3
main.main()
	github.com/ProbiusOfficial/NexTerm/target/size-r37/panicdemo/main.go:4 +0x30
```

Function names and file:line resolve through the pclntab; paths are module-relative thanks to `-trimpath`. This is the representative stack metadata proof that `-s -w` removed only DWARF/symtab, not runtime metadata.

## Informational comparisons recorded by the new gate (examples, never gating)

- `server-linux-amd64`: 42,225,826 B vs the official Rust v0.2.1 inner ELF 17,360,408 B → ratio 2.432, delta +24,865,418 B, `smaller: false`, status `measured` (recorded, not failed).
- `server-linux-amd64` vs the pre-Eino custom-Go baseline 34,844,834 B → full-Eino delta +7,380,992 B, status `measured`.
- `server-linux-arm64`, `desktop-windows-*`, Linux desktop archives, etc. have no like-for-like Rust reference → status `unavailable` with the reason, never fabricated.

## Functional verification with the optimized flags

- Desktop native smoke (darwin/arm64, `-tags production,smoke`): `target/release-assets/desktop-smoke-darwin-arm64.json` → `ok: true` (`binaryOK`, `reopenOK`, `jsonOK`).
- Server two-process sync acceptance: `python3 scripts/e2e-sync-local.py` → **22/22 passed** (it builds with the same `-trimpath -ldflags "-s -w"` flags).
- `nexterm-server-darwin-arm64 --version` → `0.2.1` (version stamping intact under `-s -w`).
- Server archive report path (`report --kind=server-archive --flavor=full … --require-evidence`) on a minimal hand-made archive: exit 0, `status: passed`, both container/member assertions passed, Rust comparison `measured` and non-gating. (That archive only exercises the gate logic; it is not a release candidate.)

## Release gate validation (eight candidates)

`.github/scripts/release-evidence.mjs` now passes/fails candidates on integrity (reported SHA-256/size match), target identity (artifact id + version) and real content/static/cgo assertions. Synthetic candidate sets (version 0.2.1) verified locally:

- 8/8 artifacts with real checks passing — including the three ids whose Go artifacts are **larger** than the official Rust reference (`rust.status: measured`) and the five genuinely unavailable ones (`rust.status: unavailable`) → `passed-with-explicit-real-target-gaps`, exit 0. The pre-R37 gate failed exactly this case.
- One corrupted report hash → `failed`, exit 1. One failed assertion → `failed`, exit 1.

## Files changed by R37 (gate semantics)

- `scripts/build.mjs`: comparisons are `measured`/`unavailable` + `informational: true`; report `status` reflects assertions only; `--require-size` renamed to `--require-evidence` and enforces real checks; report schema v2 with an explicit `runtime_metadata` note.
- `.github/scripts/release-evidence.mjs`: `gatesPassed` drops the Rust/custom-Go conditions; evidence schema v2 with the `gate_rule` statement.
- `.github/workflows/release.yml`, `scripts/pack-linux-server.sh`: flag/step renames (`--require-evidence`).
- `.github/ARTIFACTS.md`, `.github/baselines/*.json`, `docs/acceptance-rwig/baseline/README.md`: contract wording updated to the informational-only rule.

## Honest limits

- Desktop binaries embed the frontend; local pnpm 10.33.0/Node 24 can produce slightly different frontend bytes than the CI-pinned pnpm 11/Node 22, so CI desktop sizes may differ marginally. Server binaries are unaffected by frontend drift.
- Package-level sizes (DMG/NSIS/tar.gz installers) are not re-measured here; they wrap the same optimized binaries, and per-artifact package assertions continue to run in CI.
- Rust comparisons above are recorded for the record. Go release artifacts are larger than their historical Rust counterparts on the measured targets; per the 2026-10-03 decision this is reported, not gated.

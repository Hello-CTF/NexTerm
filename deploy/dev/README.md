# Development environment

The supported development hosts are Linux, macOS, and Windows through WSL. Run the full quality gates in WSL rather than native Windows. Run `task doctor` after cloning to check Go, Node, pnpm, Task, the pinned Wails CLI, Docker and Docker Compose. Go and Wails versions come from `go.mod`, pnpm comes from `package.json`, and CI uses Node 24 and Task 3.45.5. Linux desktop builds also need the GTK 4 and WebKitGTK 6.0 development headers used by CI.

Desktop and server builds load ONNX Runtime at run time. `scripts/ai-privacy-runtime.mjs` resolves the latest stable ONNX Runtime 1.x release (>= 1.23) from GitHub releases, Maven Central (Android) and CocoaPods (iOS), verifies each download against the digest its publisher states, and stages only its library plus license notices in the binary-adjacent `ai-runtime/` directory; nothing is compiled there. The official `onnxruntime.dll` imports the Visual C++ runtime, so Windows builds also copy `msvcp140.dll`, `msvcp140_1.dll`, `vcruntime140.dll` (and `vcruntime140_1.dll` on x64) from the Visual Studio redistributable (`VCToolsRedistDir` or vswhere); the list comes from the resolved `onnxruntime.dll` import table, and Windows hosts also check each DLL's Authenticode signature. The resolved version, archive digest and file hashes are recorded in `runtime.json` as provenance. `ONNX-RUNTIME-LICENSE` and the third-party notices come from the downloaded release. Set `NEXTERM_ORT_VERSION=1.x.y` to pin a version (CI and release resolve it once per run); `GITHUB_TOKEN` raises the GitHub API rate limit. The iOS pod trails the GitHub release, so iOS uses the newest published pod when the resolved version has none, and CocoaPods publishes no checksum for it (HTTPS only; the computed sha256 is recorded). When the resolved release publishes no library for a target (currently macOS Intel), the build succeeds without the runtime: `ai-runtime/` holds only a `runtime.json` with an `unavailable` reason, the evidence records it, and the guard reports unsupported at run time. Keep that directory with the executable; model weights are downloaded separately from application settings. Release packaging checks the library's target architecture, size and SHA256 manifest. Agent and helper builds remain independent of this runtime.

The Linux server and the Windows desktop are `CGO_ENABLED=0` binaries built with the plain Go toolchain (ONNX Runtime is loaded through purego), so `task server:build GOARCH=arm64` works from any host. The Linux server links glibc dynamically (libc, libdl, libpthread; no GLIBC_ version requirement), runs on any glibc-based Linux and not on musl, and the build asserts this; the staged ONNX Runtime library additionally needs glibc 2.27 or newer with libstdc++. Only the Linux and macOS desktops use cgo (Wails).

Mobile builds stage the same pinned runtime: Android copies `libonnxruntime.so` into `jniLibs`, and iOS links the static ONNX Runtime pod into `onnxruntime.framework` with Xcode. Native inference currently requires macOS 13.4, iOS/iPadOS 15.1, or Android API 24; the app keeps its existing minimum OS and checks compatibility before loading the optional runtime.

The Compose file provides disposable, loopback-only test databases with the same major versions and test-only credentials as CI. No application data is mounted.

```sh
docker compose -f deploy/dev/compose.yml up -d --wait
```

Run a backend explicitly, or use the no-service SQLite leg:

```sh
task test:database:sqlite
task test:database:postgres
task test:database:mysql
task test:database:mariadb
```

The backend tasks start their selected Compose service and leave it running for subsequent test runs. They use these host ports, which are separate from other local integration instances:

- PostgreSQL 16: `55433`
- MySQL 8.4: `33308`
- MariaDB 11.4: `33309`

`task test:database` defaults to SQLite with an explicit warning when `NEXTERM_TEST_DB_BACKEND` is unset. Set `NEXTERM_TEST_DB_BACKEND` and `NEXTERM_TEST_DB_DSN` directly to use another instance. Stop the Compose services with:

```sh
docker compose -f deploy/dev/compose.yml down
```

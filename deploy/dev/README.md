# Development environment

The supported development hosts are Linux, macOS, and Windows through WSL. Run the full quality gates in WSL rather than native Windows. Run `task doctor` after cloning to check Go, Node, pnpm, Task, the pinned Wails CLI, Docker, Docker Compose and, on Linux, the pinned zig. Go and Wails versions come from `go.mod`, pnpm comes from `package.json`, and CI uses Node 24 and Task 3.45.5. Linux desktop builds also need the GTK 4 and WebKitGTK 6.0 development headers used by CI.

Desktop and server builds use CGO for local AI privacy inference, which loads ONNX Runtime at run time. `scripts/ai-privacy-runtime.mjs` downloads the checksum-pinned ONNX Runtime release (1.23.2 for desktop, server and Android; 1.23.0 for iOS) and stages only its library plus license notices in the binary-adjacent `ai-runtime/` directory; nothing is compiled there. The official `onnxruntime.dll` imports the Visual C++ runtime, so Windows builds also copy `msvcp140.dll`, `msvcp140_1.dll`, `vcruntime140.dll` (and `vcruntime140_1.dll` on x64) from the Visual Studio redistributable (`VCToolsRedistDir` or vswhere); release builds require their SHA-256 to be pinned per redist version in `scripts/msvc-crt.json`, other builds print the hashes to pin. Keep that directory with the executable; model weights are downloaded separately from application settings. Release packaging checks the library's target architecture, size and SHA256 manifest. Agent and helper builds remain independent of this runtime.

The Linux server links with zig against glibc 2.28 so the archive runs on RHEL 8, Debian 11 and Ubuntu 20.04: run `node scripts/install-zig.mjs ~/.cache/nexterm-zig` and set `NEXTERM_ZIG` to the printed path. Because zig cross-compiles, `task server:build GOARCH=arm64` also works from an amd64 Linux host and vice versa. The Windows desktop needs a MinGW-w64 C compiler for its target (CI uses pinned LLVM-MinGW); `task windows:desktop` from Linux or macOS works only with `CC` set to such a cross compiler and is not exercised by CI.

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

# Development environment

The supported development hosts are Linux, macOS, and Windows through WSL. Run the full quality gates in WSL rather than native Windows. Run `task doctor` after cloning to check Go, Node, pnpm, Task, the pinned Wails CLI, Rust/Cargo 1.95.0, Docker, and Docker Compose. Go and Wails versions come from `go.mod`, pnpm comes from `package.json`, and CI uses Node 24 and Task 3.45.5. Linux desktop builds also need the GTK 4 and WebKitGTK 6.0 development headers used by CI.

Desktop and server builds use CGO and a small native privacy inference library. Install Rust with `rustup toolchain install 1.95.0 --profile minimal` and `rustup default 1.95.0`, together with the host C/C++ compiler. `scripts/build.mjs` builds the AstrLink-derived native library and downloads checksum-pinned ONNX Runtime libraries into the binary-adjacent `ai-runtime/` directory. Keep that directory with the executable; model weights are downloaded separately from application settings. Release packaging carries the libraries and third-party licenses, and checks their target architecture and SHA256 manifest. Windows native CI also installs pinned LLVM-MinGW for Go CGO and uses MSVC for Rust. Agent and helper builds remain independent of this runtime.

Mobile native builds also need Rust target libraries: use `rustup target add aarch64-linux-android x86_64-linux-android` for Android, or `rustup target add aarch64-apple-ios aarch64-apple-ios-sim x86_64-apple-ios` on macOS for iOS. The Android runtime build reads `ANDROID_NDK_HOME`. Native inference currently requires macOS 13.4, iOS/iPadOS 15.1, or Android API 24; the app keeps its existing minimum OS and checks compatibility before loading the optional runtime.


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

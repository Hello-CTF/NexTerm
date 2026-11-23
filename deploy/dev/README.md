# Development environment

The supported development hosts are Linux, macOS, and Windows through WSL. Run the full quality gates in WSL rather than native Windows. Run `task doctor` after cloning to check Go, Node, pnpm, Task, the pinned Wails CLI, Docker and Docker Compose. Go and Wails versions come from `go.mod`, pnpm comes from `package.json`, and CI uses Node 24 and Task 3.45.5. Linux desktop builds also need the GTK 4 and WebKitGTK 6.0 development headers; Wails calls GTK 4.14 API, so distributions with an older GTK 4 (for example Debian 12 with GTK 4.8) cannot build it. CI builds and tests Linux targets directly on GitHub-hosted Ubuntu runners, and `.github/actions/linux-gui-deps` holds the package list.

The AI privacy engine is pure Go (onnx-gomlx on gomlx's Go backend) and is compiled into the desktop, server and mobile binaries; no native runtime library is staged, and model weights (fp32, about 147 MB) are downloaded from application settings. Builds use `GOEXPERIMENT=simd` (Go 1.27) so the engine gets gomlx's NEON/AVX2/AVX-512 kernels: `Taskfile.yml` and the CI workflows set it, `scripts/build.mjs` appends it to every build it runs, the Android and iOS Go builds add it themselves, and release evidence checks it in `go version -m` of the desktop and server binaries and the Android library. Linux deb packages and the Windows installer still ship `ai-runtime/runtime.json` declaring the runtime unavailable, only so updaters from 1.4.6-rc.3 and earlier accept the package.

The Linux server and the Windows desktop are `CGO_ENABLED=0` binaries built with the plain Go toolchain, so `task server:build GOARCH=arm64` works from any host. The build asserts that the Linux server is static or links only glibc (libc, libdl, libpthread; no GLIBC_ version requirement). Only the Linux and macOS desktops use cgo (Wails).

## Browser frontend against a local server

`nexterm-server` accepts browser requests only from its own origin. To run the Vite dev server (`pnpm dev`, port 1420) against a local server, allow that origin explicitly and point the page at the server:

```sh
NEXTERM_ALLOWED_ORIGINS=http://localhost:1420 go run ./cmd/nexterm-server --listen 127.0.0.1:8080
```

Then open `http://localhost:1420/?api=http://localhost:8080`. Listed origins receive credentialed CORS responses, so do not list origins you do not control.

## Test databases

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

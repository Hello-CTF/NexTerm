# NexTerm Agent Guide

Reply in the user's language. Keep this file limited to rules that prevent regressions; code and executable tasks are the source of truth.

## Project Map

- `cmd/nexterm-desktop`: Wails v3 desktop app; mobile projects live under `build/`.
- `cmd/nexterm-server`: browser-mode server with multi-user accounts and server-side workspaces.
- `cmd/nexterm-agent`: agent installed on managed hosts.
- `internal/session`, `internal/terminal`, `internal/pty`, `internal/supervisor`, `internal/transport`: terminal sessions and transports, including daemonized, re-attachable SSH sessions.
- `internal/server`, `internal/account`, `internal/sync`: HTTP server, accounts, and encrypted asset sync.
- `internal/ai`: AI assistant, tools, providers, memory, and scheduled jobs.
- `internal/fleet`, `internal/deviceproto`: device management and device bridge protocol.
- `internal/store`, `internal/db`, `internal/vault`, `internal/keys`, `internal/biometric`: persistence and credential protection.
- `internal/sharing`, `internal/update`, `internal/forward`, `internal/mount`, `internal/docker`, `internal/fs`: sharing, updates, forwarding, mounts, containers, and file services.
- `src`: React frontend; `src/ipc/bindings` contains generated Wails bindings.
- `scripts`, `.github/scripts`, `.github/workflows`, `Taskfile.yml`: builds, tests, CI, and releases.
- `website`: GitHub Pages site. `lazycat`: Lazycat package. `build`: native build configuration and mobile projects. `deploy/systemd`: server units.

Toolchain: Go 1.27, Node 24+, pnpm; the Linux server is a `CGO_ENABLED=0` glibc-portable dynamic binary (purego loads ONNX Runtime; agent and helpers stay fully static), and the privacy runtime uses the latest ONNX Runtime 1.x (>= 1.23) resolved by `scripts/ai-privacy-runtime.mjs`; `NEXTERM_ORT_VERSION` pins a version. Exact versions and commands live in `go.mod`, `package.json`, and the workflows.

## Working Rules

- Read relevant code before editing. Keep changes focused and prefer the smallest implementation that satisfies the current requirement.
- Keep user-facing claims in `README.md`, `README_EN.md`, and `website` consistent with the shipped behavior. Do not claim a platform, database, or feature is supported unless a build or test path verifies it.
- Use English comments only where they explain necessary intent. Preserve build tags, embed directives, shebangs, packaging directives, and test environment pragmas. Never treat string literals or heredocs as comments to clean up.
- Use file-level Go build constraints for platform-specific code. Test affected build tags; the `production` tag requires built desktop assets.
- Do not hand-edit `src/ipc/bindings/**`, `src/ipc/generated/**` or `internal/store/query/*.gen.go`. Regenerate bindings with `task bindings`, the incremental IPC contract with `task ipc:contract`, and GORM queries with `go run ./scripts/gen-gorm`; include generated changes with the source change.
- IPC contract generation is static and does not require desktop production assets. Keep `any`, `json.RawMessage`, and unresolved stream payloads as `unknown`; deferred memory/AI-owner scopes must not acquire invented command or DTO metadata.
- Application persistence uses GORM and GORM Gen with SQLite, MySQL/MariaDB, and PostgreSQL backends. Keep model and query behavior consistent across supported backends. Models live in `internal/store/model`, schema changes use startup AutoMigrate, and business queries use `query.Use(db)`. Check missing records with `errors.Is(err, gorm.ErrRecordNotFound)`. Do not introduce SQL migration files or legacy-database compatibility paths.
- Keep commands and paths safe to copy. Use half-width hyphens and straight quotes in output. "Mobile" includes Android, iOS, and iPadOS unless a distinction affects behavior.
- Keep scratch work in gitignored `.tmp/`. Never commit credentials, personal information, or agent attribution.

## Validation

- During implementation, run focused tests for affected behavior. Assert observable results and contracts; avoid source-text checks and assertions tied to incidental ordering, counts, or platform error wording.
- Tests must be deterministic. Fix flaky behavior rather than relying on repeated runs to obtain a pass. Race detection and integration tests should cover concurrency, protocol, persistence, and security changes where relevant; label such PRs `full-ci` so CI runs the race suite.
- `task check` is the regular quality entry point: lockfile integrity, formatting, vet, Go tests, frontend checks/tests, generated IPC contract, Wails bindings and GORM queries, reproducible frontend assets, and build/release script checks. Use `task gorm:gen` for intentional query regeneration and `task gorm:check` for drift and orphan detection.
- `task verify` includes `check`, race and production-tagged Go tests, and a reproducible host desktop production build. Run it once on the final integrated tree before committing; running `check` immediately beforehand duplicates work.
- Database integration tests must run against real supported servers; a skipped integration test is not verification. `task test:database` discovers tests by dbtest fixture use; use `task test:database:sqlite` for the no-service leg and the PostgreSQL/MySQL/MariaDB variants with `deploy/dev/compose.yml` for real backends.
- A backend-specific fixture test declares `//dbtest:backend <backends>` (comma-separated backend names, e.g. `postgres,mysql`) above the test and keeps a runtime guard that skips it on other backends for direct `go test` runs. Discovery lists declared tests under `go run ./scripts/test-database --check`, legs for other backends exclude exactly the declared tests and report the exclusions, and undeclared tests still run on every backend. A selected test that skips fails the leg on any backend, so the directive classifies backend-specific tests and must not mask an ordinary quality skip.
- Linux, macOS and WSL are supported development hosts; use WSL rather than native Windows for the full quality gates. `task doctor` checks required tool versions, and `deploy/dev/README.md` documents the local database environment.
- CI runs on PRs with change-based scopes and through release `workflow_call` jobs, not on every master push. Documentation and website-only PRs skip application checks; frontend PRs run lint/coverage and desktop production builds; Go or tooling changes retain shared static/generated checks, the full Go test suite, real database contracts and mobile compilation. Native Windows/macOS test compilation and Linux production-tag tests share the native build jobs. The `full-ci` label selects all gates, the Go race suite, all native targets and cold-cache reproducibility on PR open, update or reopen; label-only changes do not restart CI. Ordinary PRs use platform smoke targets. Release tags build every versioned artifact with reproducibility and artifact checks, without repeating PR test/static/database gates. The Go test job owns PR unit tests: it runs the suite without the race detector by default and with it under `full-ci`, where terminal workloads that skip race instrumentation run separately. Do not duplicate the full suite. Group builds by OS/architecture while keeping per-kind artifact names and matching-host helper evidence. Windows installers are distributed without Authenticode; Minisign updater verification remains required. Pages uses the same commands as `website:check` without installing Go/Wails, validates PRs and deploys master pushes. Run `task verify` for the complete local gate. Preserve release artifact validation.
- Reviewers independently inspect the change and run relevant checks. Reuse observed results for unchanged work; do not rerun the entire suite for every small fix.

## Branches and Releases

- Branch from the latest `master` and merge back through a reviewed PR with passing gates. No force pushes or unreviewed pushes to `master`.
- Release `v*` tags come from `master`. Keep `wails.json`, `package.json`, and `lazycat/package.yml` at `0.0.0`; release builds derive the version from the tag through `NEXTERM_RELEASE_VERSION`.
- Reused CI evidence must match the intended commit and ref. Versioned frontend, native, and mobile release artifacts must be built for that release; do not publish placeholder-version artifacts from master builds.
- Release notes are generated from Conventional Commits plus `.github/release-template.md`. Do not add per-release notes commits or standing gap lists. Record verification limits in the change's PR or review report.
- Before publishing, validate the complete release artifact set with `node .github/scripts/release-evidence.mjs <artifact dir>`.

## Review and Collaboration

- Preserve supported inputs, defaults, fallbacks, and existing data behavior unless the PR explicitly changes them. Identify consumers and replacement behavior when removing a path or changing a contract, including AI prompts and tool descriptions.
- Report findings with evidence, location, impact, and a concrete fix. If the cause is unknown, say "cause not identified". Skipped operations and ignored inputs must remain visible to users.
- Use Conventional Commits for commits and PR titles. PR descriptions explain the problem, resulting behavior, and verification, including intentional compatibility changes and checks that could not run.
- When work is delegated, use non-overlapping file ownership and keep implementation and review separate. Do not launch child AI processes through shell commands.
- Integrate against the latest `master`, resolve changes as one coherent batch, and validate the final tree before merging. Inspect `git status` and `git diff --staged --stat` before committing.

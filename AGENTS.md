# AGENTS.md

只记录本仓库现行、可直接执行的约定。

## 注释策略

- 默认不写解释性注释，代码自解释；评审追溯、来源说明类注释一律不留。
- 仅保留功能性指令，且必须原样保留：
  - Go：`//go:build`（一律在文件第 1 行）与 `//go:embed`（`migrations/migrations.go`、`cmd/nexterm-desktop/assets_production.go`）。
  - 脚本首行 shebang（`#!...`）。
  - 打包指令：`lazycat/lzc-manifest.yml` 的 `#@build if/else/end`（被 lzc-cli 与 `scripts/verify-manifest-injects.py` 解析）。
  - 前端测试首行 pragma：`/** @vitest-environment jsdom */`。
- 字符串字面量、模板与 heredoc 内的内容（含伪注释）是数据，任何清理不得触碰。

## 生成 bindings

- `src/ipc/bindings/**` 是 wails3 生成物，禁止手改；漂移会被门禁直接拒绝。
- 有意更新：`wails3 generate bindings -ts -i -d src/ipc/bindings ./cmd/nexterm-desktop`（等同 `task bindings`），随 IPC 变更一起提交。
- 校验：`node scripts/build.mjs bindings`（重新生成到临时目录并逐字节比对）。

## 质量命令（提交前须全绿，与 CI 一致）

- Go：`gofmt -l ./cmd ./internal ./migrations` 必须无输出；`go vet -mod=readonly ./...`；`go test -mod=readonly ./...`（并发改动加 `-race`；动了 production 标签代码再跑 `go test -mod=readonly -tags production ./...`）。
- 前端：`pnpm typecheck`、`pnpm lint`、`pnpm test`。
- 产物可复现：`node scripts/build.mjs frontend --repro-check`。
- 已安装 task 时可用 `task check` 一次跑完上述门禁。

## 平台 build tags

- 平台分支一律用文件级 `//go:build` 配合文件名后缀：`*_unix.go`、`*_windows.go`、`*_darwin.go`（约束形如 `unix`、`windows`、`!windows`、`darwin || linux`）。
- 非平台标签：`production`（内嵌桌面资源，需先备好 `cmd/nexterm-desktop/dist`）、`smoke`（WebView 冒烟）、`race`（竞态测试开关）；改动后必须跑对应 `-tags` 测试。

## migrations（append-only）

- `migrations/NNNN_name.sql` 按版本号升序应用，经 `//go:embed` 打包进二进制。
- 已发布的迁移文件禁止修改：`schema_migrations` 表校验 SHA-256，改动即启动失败。
- 新 schema 变更一律追加序号更大的新文件。

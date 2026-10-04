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

- Go：`gofmt -l ./cmd ./internal ./migrations` 必须无输出；`go vet -mod=readonly ./...`；`go test -mod=readonly ./...`。
- 前端：`pnpm typecheck`、`pnpm lint`、`pnpm test`。
- 产物可复现：`node scripts/build.mjs frontend --repro-check`。
- 已安装 task 时 `task check` 一次跑完上述常规项；`-race`（并发改动）与 `-tags production`（production 标签改动）不在其中，须按需另跑对应 `go test`。

## 分层测试节奏

- 实现期只跑聚焦测试：与改动直接相关的 Go 包 / 前端测试，外加 `pnpm typecheck`、`pnpm lint`；不为每个小改动重复全仓套件。
- 高风险改动（并发、协议、持久化、安全、数据库）一旦可运行，立即补跑相关集成测试与 `go test -race ./...`；涉及 build tag 的按需加 `-tags`。
- 大块任务集成并 rebase 后，对最终树跑一次全量验证：`task check`、`task verify`；产出发布产物时附 release evidence（`node .github/scripts/release-evidence.mjs <产物目录>`）。
- 分层不削弱门禁：CI 保留全部质量检查，上文"质量命令"全绿要求不变；评审须实际运行并核对行为，不得凭汇报放行。

## 平台 build tags

- 平台分支一律用文件级 `//go:build`（在文件第 1 行）；常见约束为 `windows`、`unix`、`darwin || linux`、`!windows`，文件名多用 `*_unix.go`、`*_windows.go`、`*_darwin.go` 后缀，但按约束语义命名即可——如 `winsize_signed.go`（`aix || solaris`）、`protector_unsupported.go`（`!windows && !darwin`）及多处 `darwin || linux` 测试均无平台后缀。
- 非平台标签：`production`（内嵌桌面资源，需先备好 `cmd/nexterm-desktop/dist`）、`smoke`（WebView 冒烟）、`race`（竞态测试开关）；改动后必须跑对应 `-tags` 测试。

## migrations（append-only）

- `migrations/NNNN_name.sql` 按版本号升序应用，经 `//go:embed` 打包进二进制。
- 已发布迁移的 schema 语义与顺序不可改（同版本 Go 代码依赖其产物）；仅注释清理可改动已发布文件，checksum 随之改变。
- 旧库兼容不做要求：`schema_migrations` 校验 SHA-256 不匹配即启动失败；新 schema 变更一律追加序号更大的新文件，新库迁移必须通过。

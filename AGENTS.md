# AGENTS.md

只记录本仓库现行、可直接执行的约定。

## 分支基线

- `master` 是唯一工作基线与发布基线：新分支一律从 `master` 最新提交切出，完成后合回 `master`。Pages 部署随 `master` push 触发，属既有流程。
- 发布从 `master` 打 `v*` 标签触发 release workflow；CI 在 push/PR 到 `master` 以及 `v*` tag push 时运行，`quality` job 除 Go/前端门禁外还包含 manifest injects 与 workflow 复用接线检查（命令见"质量命令"）。
- 版本号唯一来源是发布 tag：`wails.json`、`package.json`、`lazycat/package.yml` 恒为 `0.0.0` 占位，tag push 触发的 CI 从 ref 解析 `NEXTERM_RELEASE_VERSION` 烙进二进制与 dist manifest（master/PR 构建用占位版本）；release workflow 复用同 tag 的 CI run（`CI_REUSE_EXPECTED_REF` 锁定），本地/懒猫构建可经 `NEXTERM_RELEASE_VERSION` 或恰好落在 tag 上获得真实版本。
- macOS 签名可选增强：仓库 variables 设 `NEXTERM_MACOS_SIGNING=true` 后，desktop job 自动导入 `NEXTERM_MACOS_P12_BASE64`/`NEXTERM_MACOS_P12_PASSWORD`(secret)并用 `NEXTERM_MACOS_SIGN_IDENTITY`(variable)签名（hardened runtime + timestamp）；再配 `NEXTERM_MACOS_NOTARY_KEY_BASE64`(secret)与 `NEXTERM_MACOS_NOTARY_KEY_ID`/`NEXTERM_MACOS_NOTARY_ISSUER`(variable)即自动公证+ stapler。未配置时保持 ad-hoc 分发；自签证书无法通过公证，公证必须 Developer ID。
- 推送 `master` 仅限评审通过且门禁全绿后的 fast-forward；禁止未评审直推与 force push。

## 编码标准

- 代码先行：先给能工作的代码，解释只保留必要部分。
- 最简实现：不过度工程，不做单次使用的抽象，不写投机特性，不实现没有当前调用方的通用层。
- 每次编辑前先读代码，不凭记忆或猜测盲改。
- 不加无关 docstring 与类型注解；不写不可能分支的错误处理（调用方已保证、类型系统已排除的状态不防御）。
- 小重复优于过早抽象：相似代码第三次出现前保持重复。

## 评审与调试

- 评审只陈述问题与修复：指出 bug 即给修法，不恭维、不扩展范围。
- 调试先读代码再下结论：一次说清证据、位置与修复。
- 原因不明就写"原因未查明"，不猜原因、不编造解释。

## 输出格式

- 标点一律用半角连字符 `-` 与直引号 `"` `'`：不用破折号、弯引号与装饰性 Unicode。
- 需要时使用中日韩文字；代码、命令、路径输出必须可安全复制粘贴，不被智能标点污染。

## 注释策略

- 默认不写解释性注释，代码自解释；评审追溯、来源说明类注释一律不留。
- 仅保留功能性指令，且必须原样保留：
  - Go：`//go:build`（一律在文件第 1 行）与 `//go:embed`（`migrations/migrations.go`、`cmd/nexterm-desktop/assets_production.go`）。
  - 脚本首行 shebang（`#!...`）。
  - 打包指令：`lazycat/lzc-manifest.yml` 的 `#@build if/else/end`（被 lzc-cli 与 `scripts/verify-manifest-injects` 解析）。
  - 前端测试首行 pragma：`/** @vitest-environment jsdom */`。
- 字符串字面量、模板与 heredoc 内的内容（含伪注释）是数据，任何清理不得触碰。

## 生成 bindings

- `src/ipc/bindings/**` 是 wails3 生成物，禁止手改；漂移会被门禁直接拒绝。
- 有意更新：`wails3 generate bindings -ts -i -d src/ipc/bindings ./cmd/nexterm-desktop`（等同 `task bindings`），随 IPC 变更一起提交。
- 校验：`node scripts/build.mjs bindings`（重新生成到临时目录并逐字节比对）。

## 质量命令（提交前须全绿，与 CI 一致）

- Go：`gofmt -l ./cmd ./internal ./migrations ./scripts` 必须无输出；`go vet -mod=readonly ./...`；`go test -mod=readonly ./...`。
- 前端：`pnpm typecheck`、`pnpm lint`、`pnpm test`。
- 产物可复现：`node scripts/build.mjs frontend --repro-check`。
- 静态契约：`go run ./scripts/verify-manifest-injects`；workflow 复用接线 `go run ./scripts/check-reuse-wiring`（CI 设 `SOURCE_DATE_EPOCH=1`）。
- 已安装 task 时 `task check` 一次跑完上述常规项（`verify-manifest-injects` 在其中，复用接线检查不在其中，改动相关文件时以 `task verify:wiring` 单独执行）；`-race`（并发改动）与 `-tags production`（production 标签改动）不在其中，须按需另跑对应 `go test`。
- CI 质量门禁只跑常规项：全仓 `go test -mod=readonly ./...` 即发布门禁；`-race`、`-tags production` 全量复跑与独立 Windows supervisor/SSH jobs 都不是 CI 步骤，不阻塞发布。并发改动仍按上条本地补跑对应 `-tags` / `-race`。
- 真实 Chromium 验收缺口：原 `task acceptance:browser`（`.github/scripts/browser-acceptance.mjs`）随演示模式一并移除——它完全依赖 `?demo=1` 启动应用。真实浏览器行为（焦点与纯键盘导航、窄宽溢出、触摸命中、WebSocket 早期帧/重连、真实服务端 reload 恢复等）当前没有任何自动化验收，属未覆盖缺口，如实记录在 `.github/acceptance/real-target-gaps.json` 与 release notes，不得当作已通过。

## 分层测试节奏

- 实现期只跑聚焦测试：与改动直接相关的 Go 包 / 前端测试，外加 `pnpm typecheck`、`pnpm lint`；不为每个小改动重复全仓套件。
- 高风险改动（并发、协议、持久化、安全、数据库）一旦可运行，立即补跑相关集成测试与 `go test -race ./...`；涉及 build tag 的按需加 `-tags`。
- 大块任务集成并 rebase 后，对最终树跑一次全量验证：`task check`、`task verify`（verify = check 全部门禁 + 宿主 desktop production 二进制可复现构建，消费 check 已验证的 dist 与 manifest，不再运行原生 WebView 冒烟）；产出发布产物时跑 `node .github/scripts/release-evidence.mjs <产物目录>` 校验 8 个最终产物并生成 `SHA256SUMS`。release 资产只有 8 个目标包与 `SHA256SUMS`，不生成也不上传 `release-evidence.json` 与 `real-target-gaps.json`；真实环境验证缺口只在 release notes 与 Pages 文档中如实说明。
- 分层不削弱门禁：CI 保留全部质量检查，上文"质量命令"全绿要求不变；评审须实际运行并核对行为，不得凭汇报放行。

## 平台 build tags

- 平台分支一律用文件级 `//go:build`（在文件第 1 行）；常见约束为 `windows`、`unix`、`darwin || linux`、`!windows`，文件名多用 `*_unix.go`、`*_windows.go`、`*_darwin.go` 后缀，但按约束语义命名即可，如 `winsize_signed.go`（`aix || solaris`）、`protector_unsupported.go`（`!windows && !darwin`）及多处 `darwin || linux` 测试均无平台后缀。
- 非平台标签：`production`（内嵌桌面资源，需先备好 `cmd/nexterm-desktop/dist`）、`race`（竞态测试开关）；改动后必须跑对应 `-tags` 测试。

## migrations（breaking 版本可重写）

- `migrations/NNNN_name.sql` 按版本号升序应用，经 `//go:embed` 打包进二进制。
- `migrations/postgres/` 是根 `migrations/*.sql` 的 PostgreSQL 镜像：版本号与描述一一对应，仅方言不同（如 `BIGINT`/`BYTEA`、无 `PRAGMA`）。schema 变更必须在两处同步修改同版本号文件；`TestPostgresMigrationsMatchRootVersions` 守住版本与描述对齐。
- 下一版本是 breaking-change 全新版本：允许直接修改、合并重写已发布迁移文件，不保留旧库兼容；`schema_migrations` 校验 SHA-256 不匹配即启动失败，旧库作废属预期。新库迁移必须通过。

## PostgreSQL 真实测试

- PG 门控测试位于 `internal/dbtest`、`internal/store`、`internal/account`、`internal/sync`、`internal/ai/cron`，统一经 `internal/dbtest` harness 读取 `NEXTERM_TEST_PG_DSN`；未设置时逐测试 SKIP，SKIP 不算已验证。
- 本地聚焦复跑：`NEXTERM_TEST_PG_DSN='<dsn>' task test:pg`（等价于对同五个包 `go test -count=1`）；该 task 在 DSN 缺失时直接失败，不把 SKIP 当通过。
- 远程 CI 的 `postgres` job 真实启动 PostgreSQL 16 服务并携带 DSN 跑上述五个包，日志出现任何 harness SKIP 即判 job 失败；DSN 经 `add-mask` 脱敏，不写入日志。

## 多 agent 协作

- 并行最多 50 个 agent，超出分批。
- 每个 agent 的作用域互不重叠；同一文件不分配给两个 agent。
- 依赖显式排序：有依赖的任务写明先后顺序，无依赖才并行。
- 实现与评审分离：改动由独立 agent 评审，作者不自审。
- 实现前先 rebase 到 `master` 最新提交；最终集成前再 rebase 一次，最终集成只做一次。
- 测试节奏与"分层测试节奏"一致：实现期只跑聚焦测试，最终集成后跑一次全量验证（`task check`、`task verify`）；不为每个小改动重复全仓套件，不重复跑已移除的 CI job。
- 并行不绕过既有约定：CI 门禁（见"质量命令"）与 bindings 生成物禁手改均不变；提交不得引入真实密钥或个人信息，密钥安全靠评审与按需扫描把关（无固定 CI 扫描 job）；`internal/ai/memory/redact_test.go` 的合成 Slack token 为已批准测试夹具，其值不得打印进文档或日志。

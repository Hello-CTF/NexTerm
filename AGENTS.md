# AGENTS.md

只记录本仓库现行、可直接执行的约定。

## 分支基线

- `rwig` 是唯一工作基线与发布基线：新分支一律从 `rwig` 最新提交切出，完成后合回 `rwig`；不面向 `master`/`main` 开发（Pages 部署随 `master` push 触发，属既有流程，不改变基线约定）。
- 发布从 `rwig` 打 `v*` 标签触发 release workflow；CI 在 push/PR 到 `rwig`、`master`、`main` 时运行。
- 推送 `rwig` 仅限评审通过且门禁全绿后的 fast-forward；禁止未评审直推与 force push。

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
- CI 质量门禁只跑常规项：全仓 `go test -mod=readonly ./...` 即发布门禁；`-race`、`-tags production` 全量复跑、build harness 单测、Real Chromium 验收与独立 Windows supervisor/SSH jobs 都不是 CI 步骤，不阻塞发布。并发改动仍按上条本地补跑对应 `-tags` / `-race`。

## 分层测试节奏

- 实现期只跑聚焦测试：与改动直接相关的 Go 包 / 前端测试，外加 `pnpm typecheck`、`pnpm lint`；不为每个小改动重复全仓套件。
- 高风险改动（并发、协议、持久化、安全、数据库）一旦可运行，立即补跑相关集成测试与 `go test -race ./...`；涉及 build tag 的按需加 `-tags`。
- 大块任务集成并 rebase 后，对最终树跑一次全量验证：`task check`、`task verify`（verify = check 全部门禁 + 宿主 desktop production 二进制可复现构建，消费 check 已验证的 dist 与 manifest，不再运行原生 WebView 冒烟）；产出发布产物时附 release evidence（`node .github/scripts/release-evidence.mjs <产物目录>`）。
- 分层不削弱门禁：CI 保留全部质量检查，上文"质量命令"全绿要求不变；评审须实际运行并核对行为，不得凭汇报放行。

## 平台 build tags

- 平台分支一律用文件级 `//go:build`（在文件第 1 行）；常见约束为 `windows`、`unix`、`darwin || linux`、`!windows`，文件名多用 `*_unix.go`、`*_windows.go`、`*_darwin.go` 后缀，但按约束语义命名即可，如 `winsize_signed.go`（`aix || solaris`）、`protector_unsupported.go`（`!windows && !darwin`）及多处 `darwin || linux` 测试均无平台后缀。
- 非平台标签：`production`（内嵌桌面资源，需先备好 `cmd/nexterm-desktop/dist`）、`race`（竞态测试开关）；改动后必须跑对应 `-tags` 测试。

## migrations（append-only）

- `migrations/NNNN_name.sql` 按版本号升序应用，经 `//go:embed` 打包进二进制。
- 已发布迁移的 schema 语义与顺序不可改（同版本 Go 代码依赖其产物）；仅注释清理可改动已发布文件，checksum 随之改变。
- 旧库兼容不做要求：`schema_migrations` 校验 SHA-256 不匹配即启动失败；新 schema 变更一律追加序号更大的新文件，新库迁移必须通过。

## 多 agent 协作

- 并行最多 50 个 agent，超出分批。
- 每个 agent 的作用域互不重叠；同一文件不分配给两个 agent。
- 依赖显式排序：有依赖的任务写明先后顺序，无依赖才并行。
- 实现与评审分离：改动由独立 agent 评审，作者不自审。
- 实现前先 rebase 到 `rwig` 最新提交；最终集成前再 rebase 一次，最终集成只做一次。
- 测试节奏与"分层测试节奏"一致：实现期只跑聚焦测试，最终集成后跑一次全量验证（`task check`、`task verify`）；不为每个小改动重复全仓套件，不重复跑已移除的 CI job。
- 并行不绕过既有约定：CI 门禁（见"质量命令"）、bindings 生成物禁手改、migrations append-only 均不变；提交不得引入真实密钥或个人信息，密钥安全靠评审与按需扫描把关（无固定 CI 扫描 job）；`internal/ai/memory/redact_test.go` 的合成 Slack token 为已批准测试夹具，其值不得打印进文档或日志。

# Go 分支 rc.4 优化理念 → Rust 侧可落地清单

- **评审时间**：2026-10-06
- **上游**：`origin/rwig` @ `f853cf6`（tag `v0.2.2-rc.4`），基线 `b4df788`（tag `v0.2.2-rc.2`）
- **我们的基线**：`rust` 分支（Rust/Tauri 版），本文只读，不改任何源码
- **方法**：全部结论来自 `git log/diff b4df788..f853cf6`；需要读文件时用 `git show f853cf6:<path>`，未改动主工作区
- **配套文档**：`docs/UPSTREAM-RWIG-GO-BRANCH-REVIEW.md`（下称「主评审」）、`docs/UPSTREAM-RWIG-ISSUE-migration-checksum.md`、`docs/UPSTREAM-RWIG-LAZYCAT-PATCH-AUDIT.md`

> **一句话总结**：rc.2→rc.4 这 100 个提交里，**几乎没有新的「性能/构建」优化**（CI workflow、`scripts/build.mjs` 都一字未改）。这一轮真正的「优化理念」集中在三处：
> ① **安全默认值翻转 + 显式降级**（`--auth on`、`--master-key-file`、`--require-vault`）；
> ② **把「数据的语义」用脚本钉成不变量**（体积对比 informational-only、parity fixture 的 provenance 分级、CI 门禁的负向对照）；
> ③ **一次架构级去依赖重构**：删掉整个 `internal/durable`（tmux 后端），改由内置 supervisor 承担持久会话。
>
> 对 Rust 侧而言，真正能「照抄」的是 ①②里的**工程纪律**与主评审已列出的**编译/门禁**项，不是任何 Go 独有的数字。

---

## 一、rc.4 相对 rc.2 的变化（有 diff/log 证据）

### 1.0 规模账（`git rev-list --count` / `git diff --numstat`）

| 指标 | 数值 |
|---|---:|
| 提交数 | **100** |
| 变更文件 | **252** |
| 总增删 | **+22,703 / −5,730** |
| Go 生产代码 | +4,345 / −1,738 |
| Go 测试代码 | **+8,131 / −3,472** |
| 前端代码（72 文件） | +9,907 / −441 |
| 前端测试（40 文件，`src/test/**`） | **+6,651 / −12** |
| `internal/durable/` | **净 −4,019 行**（只剩 `errors.go`） |

**读法**：这一轮 Go 测试新增行数（8,131）**约为生产代码新增（4,345）的 1.9×**；前端测试新增占前端总新增的 ~67%。测试投入显著大于功能投入。

### 1.1 安全默认值全面翻转（M9 / M13 / SEC-5）

**证据：`internal/app/cli.go` / `internal/server/config.go` / `deploy/systemd/*.service` / `README.md` / `.github/release-template.md`**

| 变化 | diff 证据 |
|---|---|
| 访问控制默认收紧 | `cli.go`：`Auth: valueOr(getenv("NEXTERM_AUTH"), AuthOn)` —— 默认从「仅非回环监听强制」翻转为**任何监听地址都要求同步令牌** |
| 放宽必须显式 | 新增 `AuthOn/AuthLoopback/AuthOff` 三态；`--auth loopback` 才豁免，且**豁免自带 Host 白名单** |
| 防 DNS 重绑定 | `transport_security.go` 新增 `loopbackHost()`：`loopback` 模式下 `r.Host` 不属于 `localhost`/`127.0.0.1`/`::1` 直接 **421 Misdirected Request** |
| 下发配置显式声明 | 两个 systemd 单元都加了一行 `--auth on`，即**仓库自带的部署配置默认就是最严** |
| 密钥来源分级 | 新增 `--master-key-file`（`NEXTERM_MASTER_KEY_FILE`）；`--master-key`/env 标注 **deprecated**（理由写在 README：进程环境对同机用户可见）；两者互斥；`ResolveMasterKey` 读文件并 `TrimSpace`，空文件报错 |
| 启动即失败 | 新增 `--require-vault`（`RequireVault bool`），凭据库未解锁则非零退出 |
| 令牌身份分级 | `internal/server/auth.go` 新增 `TokenIdentityVerifier` 接口 + `syncservice.WithTokenIdentity`，区分管理员令牌与普通客户端令牌；用 `if verifier, ok := s.tokens.(TokenIdentityVerifier); ok` 做**接口断言式向后兼容**，不打断既有实现 |
| 资源入口默认封顶 | `internal/server/blobs.go`：`DefaultBlobMaxBytes=256MB` + `http.MaxBytesReader`、`DefaultBlobPersistQuota=1GB`、`DefaultBlobDiskMaxPercent=90`；三个都可 env 覆盖，且**覆盖值非法时只 `Warn` 并回落默认，不崩启动** |

**理念（可直接命名）**：默认最严；每一处放宽都要显式声明并自带边界校验；每一个资源入口都有默认上限；覆盖开关坏掉时降级而非失败。

> ⚠️ **注意（diff 证据）**：rc.4 **仍然没有**在 `transport_security.go` 的 `requestOriginAllowed` 里读取 `X-Forwarded-Host` —— 同源豁免仍只有 `strings.EqualFold(parsed.Host, r.Host)` 一条。主评审 §5.2⑤ 的反代白屏根因**在 rc.4 上未被上游修掉**（rc.4 新增的只是 `loopback` 模式下的 Host 守卫，两者不同）。⇒ 我方 `local/lazycat-go-dev` 的补丁（`ca7a713`）**仍然是必要的**。

### 1.2 持久会话去外部依赖：删掉 tmux 后端（M36 / M37）

**证据：`internal/durable/` 净删 4,019 行（仅余 `errors.go`）+ `internal/supervisor/*` 新增 + `internal/session/durable_remote.go` 新增**

- 提交 `695e763 refactor(durable): strip the tmux backend, keep the shared sentinels (M37)`、`1812410 refactor(session): drop the tmux durable provider and its integration tests (M37)`、`bbd2a91 feat(production): wire supervisor durable recovery and drop the tmux fallback (M36)`。
- 替代物：自建 supervisor —— 新增 `internal/supervisor/helper_bridge.go`（Unix socket 双向转发）、`internal/supervisor/shellintegr.go`、`internal/ssh/{ensure,daemon}.go`；远端会话经 `session/durable_remote.go` 的 `durableProviderFor` 按 session generation 缓存 provider，重连时对新 transport 重新解析。
- **理念**：不依赖宿主外部二进制（tmux），把「会话保活」从「借壳 tmux」变成「自己的 supervisor 进程」。这直接消除了主评审 §5.4 里「容器镜像必须装 tmux，否则终端全死」这一类部署硬依赖。

> **Rust 侧对照（已由 term-pane 查证，只读）**：本仓**不依赖 tmux，也不依赖任何外部 supervisor**。持久会话是**纯进程内**的——PTY + 内核 VT 状态机 + 环形缓冲全活在 Tauri 进程里（`src-tauri/src/terminal/mod.rs`；环形缓冲 32MB/10 万行），底层连接就是一个 `Session` 直接持有（`src-tauri/src/session/mod.rs`），没有 `internal/durable` 对应物。全仓唯一的 tmux 出现在 `src-tauri/src/transport/local.rs:68`，是一条**注释**（说明宿主自己跑在 tmux 里时要覆盖 `TERM`），不是运行时依赖。
> ⇒ 上游「删 durable 去 tmux 依赖」这条动机**对本仓不适用**；但两者的「持久」语义有实质差异：上游是**跨进程** supervisor 保活，本仓是**进程内**保活 —— **Server 进程一重启，所有标签即丢失**（这正是前端 `attachDead` / attach 拿到 `not_found` 兜底路径的由来）。这是一个可讨论的差距点，见 §二.⑪。

### 1.3 shell 集成：旁路注入、对数据面零副作用（M23）

**证据：新增 `internal/terminal/shellintegr/{shellintegr,tracker}.go`（+242 生产行）+ `shell_e2e_test.go`（+158）**

- 设计声明写在包注释里：*"The wrapper only adds escape-sequence output on top of what the shell already writes; it never rewrites or suppresses shell output. Tracker reads the stream without consuming or mutating it."*
- 支持 zsh/bash/fish：zsh 走 `ZDOTDIR` 四文件透传；bash 走 `--rcfile`，且 `internal/supervisor/shellintegr.go` 的 `preserveLoginMode` 专门处理 **bash 登录壳**（composite rc → `/etc/profile` → `~/.bash_profile/.bash_login/.profile` → wrapper）。
- **理念**：可观测性注入（cwd 上报走 OSC 7）必须是**旁路**的，录制/回放拿到的仍是 shell 原始字节。

### 1.4 把「数据的语义」固化成 CI 不变量（M26）

**证据：`.github/scripts/check-reuse-wiring.py`（+90 / −51）、`scripts/parity/manifest.py`、`testdata/parity/manifest.json`**

| 新增不变量 | 证据 |
|---|---|
| 体积基线**永远只是参考、不是门禁** | 新增检查：`.github/baselines/{custom-go.json,rust-official-v0.2.1.json}` 内部必须同时含 `"informational"`、`"never"`、`"gate"` 三个词，否则 CI 失败 |
| `build.mjs` 必须保留「informational-only」标记 | 新增检查比对 5 个 marker：`rust.informational = true`、`rust.rule = "historical reference only; never a pass/fail gate"`、`custom.informational = true`、`custom.rule = "full-Eino size impact reporting only; never a pass/fail gate"`、`stay informational and never gate release` |
| parity fixture 分级 | `manifest.json` 每条加 `status`：`source.json`/`rust-registry.json` = `frozen-historical`，`inventory.json` = `generated`，`go-aliases.json` = `configuration` |
| 门禁脚本自身去重 | 抽出 `CheckCollector` / `run_timed` / `pop_job_if` / `pop_step_if` / `run_negative_controls`，四段检查各自计时 |

这正对着主评审 §4.6 的结论：**「体积对比谁大谁小」这件事已经被上游用脚本钉死为「只报告、不设卡」**，防止有人日后把它变成 release gate。

### 1.5 CI 与构建脚本：这一轮**没有**变化（重要反证）

- `.github/workflows/{ci,release,pages}.yml`：`git diff b4df788..f853cf6 -- '.github/workflows/'` **无输出**。
- `scripts/build.mjs`：**未变**（`--stat` 无输出）。上面 §1.4 的 marker 早已存在，rc.4 只是**新增了强制检查**。
- 无新增性能基准：全 diff 只加了 1 个 `func BenchmarkObserve`；`testing.Short` 新增 6 处、`t.Skip` 新增 47 处（多为「依赖缺失/平台不适用就跳过」，注意 `scripts/e2e-sync-local.py` 明确写着 *"no skip-as-pass paths"*，即**跳过与通过被区分**）。
- 依赖侧只有一笔：`github.com/coder/websocket v1.8.14 → v1.8.15`（`go.mod`）。

⇒ **凡是「rc.4 在性能/CI 上做了 X」的说法，本轮都没有 diff 支撑。** 真正的工程产出是安全默认值、测试/门禁语义、架构去依赖。

### 1.6 协作与工程标准写进仓库（M228 / M26）

**证据：`AGENTS.md`（+37）**

- 新增「分支基线」（`rwig` 唯一基线，禁 force push，只 fast-forward）、「编码标准」（最简实现、先读代码、不加投机抽象）、「评审与调试」（指出 bug 即给修法；原因不明就写「未查明」，**不猜、不编**）、「输出格式」（半角标点，保证命令可安全复制）。
- 新增「多 agent 协作」：作用域互不重叠、同文件不派两 agent、实现与评审分离（作者不自审）、**分层测试节奏**——实现期只跑聚焦测试，最终集成后跑一次全量（`task check` / `task verify`），不重复跑已移除的 CI job。

---

## 二、可移植到 Rust 侧的优化项（按性价比排序）

> 标注约定：**【实测·评】** = 数字出自主评审同机实测（非本轮新测）；**【未实测】** = 需要我方自行验证；【零风险】= 不改语义、纯配置/脚本层。

### ① `build.rs` 补 `cargo:rerun-if-changed`（性价比最高｜零风险）

- **收益**：当前 `src-tauri/build.rs`（27 行）**没有任何 `cargo:rerun-if-changed`**，Cargo 退回默认行为——包内任何文件变动都重跑构建脚本。加一行可避免无谓重跑。**收益【未实测】**（主评审 §4.7.4 同样只列为「值得先实测」）。
- **风险**：极低。唯一要注意的是：一旦写了 `rerun-if-changed`，就只在这些路径变动时重跑，别把需要的输入漏掉（这里 build.rs 只依赖自身 + desktop feature）。
- **改动面**：`src-tauri/build.rs`（`fn main` 开头加 `println!("cargo:rerun-if-changed=build.rs");`）。
- **验证**：改一个无关文件（如 `src/` 下某 tsx）后 `cargo build -p nexterm --release`，看是否还触发 build script 重跑。

### ② release profile 换 `lto="fat"`，再评估 `opt-level="z"` + `codegen-units=1`

- **收益【实测·评】**（主评审 §4.6.3，darwin/arm64 server 模式、各自完整重建）：
  - 现状 `opt-level="s"` + `lto="thin"` + `codegen-units=16` = 17,108,880 B；
  - 仅 `lto="fat"` → 12,352,128 B（**−27.8%**，单项最大）；
  - 三者叠加 → 9,141,664 B（**−46.6%**）；`__TEXT` 14,106,624 → 8,159,232（−42%），二进制可正常 `--version`。
- **风险**：⚠️ `opt-level="z"` 会牺牲向量化，**对终端吞吐的影响主评审未测**（§4.6.3 明确标注）。构建耗时实测 2m28s，可接受。
- **改动面**：`Cargo.toml:8-12`（`[profile.release]`）。
- **验证**：改前先跑 `cargo bench -p nexterm --bench terminal_throughput`（Rust 侧已有 `src-tauri/benches/terminal_throughput.rs`）存基线；改后重跑对比吞吐，确认无回退再合。**不要跳过这一步。**

### ③ 拆 `nexterm_lib` crate，压低 release 增量

- **收益【实测·评】**（主评审 §4.7）：现状改一行 → `--release` 增量 **41.4 s**（`cargo build -v` 显示只重编 `nexterm_lib` + `nexterm_server` 两个编译单元）；`cargo check --release` 单独 30.6 s ⇒ 成本压在「一个超大编译单元」，不在链接。目标：按「改动频率 × 下游大小」拆 crate，把常改模块放到依赖树**最下游**，41s 打到个位数。
- **风险**：中。拆分点设计错（把大块拆了但仍被 lib 依赖）收益为零；crate 边界会引入 `pub` 暴露面调整。
- **改动面**：`Cargo.toml`（workspace members）、`src-tauri/src/**`（模块 → crate 迁移）。
- **验证**：每次拆完 `cargo build --release -p nexterm --no-default-features --features server --bin nexterm-server -v > log`，`grep -o "crate-name [a-z_]*"` 看重编单元数是否真的变多、单次增量耗时是否下降。
- **备注**：这条与「迁不迁 Go」无关，主评审 §6.6 已单列。

### ④ 命令面「冻结基线 + 实时扫描 + 差额逐条解释」对账脚本

- **收益**：把我们「手写 curl 打 `/rpc` 判断参数名是不是 `args`」这种易错点，变成机械对账；主评审 §3.3 实测 `inventory.py --check` exit 0。可防止 `/healthz` 命令数在 121→132→133→139 之间继续漂移。
- **风险**：低–中。需要先冻结一份 Rust 命令清单（Rust 侧唯一清单在 `src-tauri/src/commands/mod.rs` 的 `nexterm_commands!` 宏，桌面 `desktop_commands` / 服务端 `server_commands` 两处生成）。
- **改动面**：新增 `scripts/` 下检查脚本 + 冻结 JSON；CI 里跑。**不一定要改生产代码**——宏已提供唯一清单。
- **验证**：脚本对「故意漏注册一条命令」必须报错（负向对照）。

### ⑤ 安全默认值：secure-by-default + 显式降级 + 边界校验（Rust 服务端）

- **收益**：把「默认不安全、要靠文档提醒」改成「默认最严」；主评审 §5.2⑤ 的反代白屏正是因为上游默认值与反代场景没对齐。可移植三条：
  1. 访问控制**默认开启**，放宽（loopback / off）必须显式声明；
  2. 放宽项自带边界校验（`loopback` 模式下 Host 白名单 `localhost`/`127.0.0.1`/`::1`，防 DNS 重绑定 → 421）；
  3. 根密钥来源分级：优先「从文件读」，env 作为 deprecated 后备，二者互斥；提供 `--require-vault` 启动即失败。
- **风险**：中。默认值翻转是**行为变更**，会打断既有部署——必须像 rc.4 的 README/release-template 那样**显式写升级说明**（本仓 systemd 单元、`/etc/nexterm/*.env` 权限 0600）。
- **改动面**：Rust 服务端 CLI 解析（`src-tauri/src/server/cli.rs` 附近）+ 鉴权中间件；文档。
- **验证**：新增回归测试——「默认必须要求令牌」「`loopback` 模式带非回环 Host 必须 421」「`--master-key` 与 `--master-key-file` 同给必须报错」。
- **前置核查**：**Rust 侧当前是否已有等价的访问控制/根密钥机制，本文未查证**（见 §四）。

### ⑥ 资源入口默认封顶 + env 覆盖 + 非法覆盖只警告

- **收益**：给上传/blob、持久化配额、磁盘水位三类入口各加默认上限（上游取 256MB / 1GB / 90%），并 `http.MaxBytesReader` 式硬截断。防「一个超大上传把盘写满」。
- **风险**：低。默认值需按本方场景定，不要直接抄 256MB。
- **改动面**：服务端文件/blob 上传处、磁盘使用检查。
- **验证**：构造超限请求断言 413；env 设非法值断言服务仍能启动且打 Warn。

### ⑦ 把「对比数据的语义」固化为可验证不变量

- **收益**：主评审 §4.6 的教训——「Go 比 Rust 小/大」这类对比极易被误读成门禁。上游的解法是**在 CI 里强制 `build` 脚本必须保留 `informational-only` marker**，并让基线文档必须自述 never-gate。若我方也做体积/性能对比报告，应照抄这个不变量。
- **风险**：极低，纯脚本。
- **改动面**：新增门禁脚本 + 文档 marker；挂到 CI。
- **验证**：把 marker 删掉，CI 必须变红（负向对照）。

### ⑧ 真实现 e2e + 门禁负向对照 + 分层测试节奏

- **收益**：三件可搬：
  1. **真实现 e2e**：`shell_e2e_test.go` 起真实 bash on PTY，断言 OSC 7 与用户 `PROMPT_COMMAND` **都**执行且我们那条在前（并处理 `testing.Short()` 跳过）。对 Rust 侧凡「我的代码 ↔ 外部实现（shell/ssh/协议）」的接口都适用。
  2. **门禁自测**：`check-reuse-wiring.py` 的 `NEGATIVE_CONTROLS` 对 CI YAML 做变异，断言检查脚本能抓到——**验证门禁本身**。
  3. **分层节奏**：rc.4 的 `Taskfile.yml` 把门禁分成 `task check`（fmt/vet/test/类型/生成物/可复现构建）与 `task verify`（再叠 native smoke）；`AGENTS.md` 明示「实现期只跑聚焦测试，集成后跑一次全量」。
- **风险**：低。Rust 仓库当前**没有** `Taskfile.yml`/`Makefile`（`ls` 为空）⇒ 引入一个任务运行器是净新增，需要团队认同。
- **改动面**：新增 `Taskfile.yml` 等价物；`.github/workflows/ci.yml` 增负向对照 job；测试文件。
- **验证**：故意把某条 CI 门禁删掉，负向对照必须失败。

### ⑨ 前端长列表虚拟化（若 Rust 版前端存在长会话/长列表）

- **收益**：上游 `src/features/ai/conversationVirtual.ts` 用一个**纯函数** `virtualRange(count, scrollTop, viewportHeight, estimate=88, overscan=6)`，`count<=60`（`VIRTUAL_THRESHOLD`）直接不启用，避免小列表白白增加复杂度。Rust 版前端 `src/features/**` 里**没有**任何 `virtual` 使用（grep 0 命中）。
- **风险**：低。但 Rust 版前端是否共用同一份 React 产物、是否有对应长列表，**未对照**（见 §四）。
- **改动面**：`src/features/**` 的 AI/文件树/会话列表（若有）。
- **验证**：长列表（>60 项）滚动不掉帧；小列表（≤60 项）行为与改造前一致。

### ⑩ 依赖/产物 provenance 分级

- **收益**：parity fixture 加 `status`（`frozen-historical`/`generated`/`configuration`），体积基线文档记录 commit、方法、`like_for_like` 定义、以及 `genuinely_unavailable`（「报为不可用，绝不编造，也不因此 pass/fail」）。让「这份数据是哪来的、能不能当依据」一眼可查。
- **风险**：极低。
- **改动面**：若有脚本生成的清单/基线文件，加字段 + 生成器同步。
- **验证**：`--check` 能发现手工改动导致的不一致。

### ⑪ 会话持久化语义：从「进程内保活」到「跨进程保活」（可讨论，未实测）

- **背景（已由 term-pane 查证）**：上游删除 tmux、改为**跨进程** supervisor 保活；本仓持久会话是**纯进程内**的（PTY/VT/环形缓冲都在 Tauri 进程里），因此**Server 进程一重启，所有标签全丢**，前端只能走 `attachDead`/`not_found` 兜底。
- **收益**：跨进程保活能让「升级/重启不丢会话」，与主评审 §5.4 里「关掉网页不会结束会话、换设备可接管」的产品承诺是同一类需求。这是上游本轮**真正想解决的问题**，值得我们评估自己是否需要。
- **风险**：高。这会引入一个常驻守护进程 + IPC 协议，是**新架构面**，不是优化项。本仓当前架构（单进程内存态）反而更简单、内存更省（主评审 §4 实测空载 7.3 MB）。
- **改动面**：`src-tauri/src/terminal/**`、`src-tauri/src/session/**`（若要做）。
- **验证**：先明确产品需求（"重启后是否要求会话存活"）再决定；**不建议**仅为对齐上游而做。

### ⑫ shell 集成（cwd 追踪）若要做：只注入、不改数据面

- **背景**：rc.4 的 `shellintegr` 给 zsh/bash/fish 注入 OSC 7 上报 cwd，原则是**只追加转义序列、绝不重写/抑制 shell 输出**，Tracker 只读不消费字节流。**本仓目前没有 shellintegr/OSC 7**，只有建 shell 时设一次的 `initial_cwd` 和 transport 级 `cwd()`。
- **收益**：给终端加上随 `cd` 变化的 cwd 追踪能力（供 UI / AI 读屏）。
- **风险**：⚠️ **本仓的录制/回放（`terminal/scrollback.rs` 的 32MB 环形缓冲 + 录制）使这个约束比上游更硬** —— wrapper 一旦重写字节流，会污染录制与 AI 读屏。因此若做，必须严格遵循「注入字节只增不减」。
- **改动面**：新增 shell 包装层 + 字节流解析器；`src-tauri/src/session/**` 建 shell 处。
- **验证**：真实现 e2e（起真实 bash/zsh/fish on PTY），断言「原始输出字节 + OSC 7 序列」并存，且回放/录制内容不含被改写字节。

---

## 三、不建议移植的 + 理由

1. **UPX 压缩产物** —— 主评审 §4.5/§4.6 已论证：macOS 直接拒绝 Mach-O 且断 codesign；Windows 有真实 AV 误报风险（主打 Windows 的运维工具被标红等同产品死亡）；`upx -9` 在 gzip 分发通道里几乎白干，只有 `--lzma` 有意义且仅在 Linux 服务端。**理由充分，不搬。**
2. **整体迁 Go** —— 主评审 §6.7：体积不是迁移理由（同口径 Rust 仍小 1.9×–4×），而**内存翻 2.1×** 是常驻场景的真实成本；构建快是 Go 的真实优势，但不足以抵迁移代价。**不搬。**
3. **把体积/性能对比做成 release gate** —— 上游自己刚用脚本把这条**钉成禁止**（§1.4）。Rust 侧若做对比报告，也只能 informational-only。
4. **Go 特有的编译/体积技巧** —— `-gcflags=all=-l`（禁内联 −10.4%）、`go tool nm -size` 依赖体积归因、BSS/`crypto/internal/fips140/drbg` 32MB 保留：都是 Go runtime 特有，Rust 无对应物，**不适用**。
5. **照搬「tmux → supervisor」重构** —— 这是 Go 侧为解决自身部署硬依赖做的架构变更。**本仓已查证不依赖 tmux / 外部 supervisor**（见 §1.2 对照），故这条**动机对本仓不存在**，照搬只会平白引入常驻守护进程。可借鉴的是**理念**（不让部署形态依赖宿主二进制），以及由此暴露出的一个真问题：本仓「持久」仅限进程内，重启即丢（见 §二.⑪）。

---

## 四、未确认事项（明确标注「未实测 / 未查证」）

1. **【未实测】** release profile 三件套（`lto="fat"`+`opt-level="z"`+`codegen-units=1`）对**终端吞吐**的影响——主评审 §4.6.3 自陈未测；进 CI/流水线前必须先跑 `terminal_throughput`。
2. **【未实测】** `src-tauri/build.rs` 补 `cargo:rerun-if-changed` 的实际收益（只知其「值得先实测」）。
3. **【未实测】** 拆 `nexterm_lib` 后 release 增量确实能降到「个位数秒」——目标是，不是已验结论。
4. **【未实测】** 引入 Rust 并发检测手段（`loom` / `miri` / TSAN）的成本与覆盖面——主评审 §6.5 只提出「值得加」，本轮未评估落地方式。
5. **【未查证】** Rust 侧当前是否已有访问控制/根密钥分级/`require-vault` 等价机制（`grep MASTER_KEY src-tauri/src` 0 命中，说明命名不同或能力不存在，但**未逐一核对**）。
6. **【未对照】** Rust 版前端与 rc.4 前端是否共用 `conversationVirtual` 一类长列表；§二.⑨ 的前提需先确认。
7. **【未实测】** rc.4 的 `--auth on` 默认值翻转**是否真的能避免反代白屏**。diff 证据只表明 `requestOriginAllowed` 仍不比 `X-Forwarded-Host`（§1.1⚠️），因此我方 `ca7a713` 补丁仍必要；但「rc.4 部署到懒猫后是否仍白屏」本轮**没有实机复现**。

### 已查证（移出未确认项）

- **Rust 侧持久会话后端**：term-pane 只读查证 —— 本仓**不依赖 tmux / 任何外部 supervisor**，持久会话为**纯进程内** PTY（`src-tauri/src/terminal/mod.rs`、`src-tauri/src/session/mod.rs`），进程重启即丢标签。因此 §三.5「照搬 tmux→supervisor」的动机不成立，相关讨论改为 §二.⑪。
  - 附带查证：`src-tauri/src/session/reconnect.rs` 的指数退避重连（1/2/4/8/16/30s，最多 10 次）**成功不重建 PTY**，只插横幅并保留 scrollback；仅 ssh/docker/winrm 可重连，local 不可 —— 与上游「重连后按新 transport 重解析 provider」思路不同，本仓是复用同一 Session。
  - cwd 追踪：本仓**无** shellintegr/OSC 7，仅 `initial_cwd`（一次性）与 transport 级 `cwd()`（WinRM 非交互维护）。

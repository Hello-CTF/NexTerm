# 内置「当前设备」资产 + 同机功能兼容性

- 日期：2026-09-30
- 起因：需求「资产默认会有一个 **当前设备** 的本地终端和工作区；做好同机的各项功能兼容性」。
- 结论：装好即有一台本机（终端 / 文件树 / 容器面板都落在它上面），并且**把「同机」这条路径上
  原本会失败或假装在工作的位置全部对齐**（清单见 §3）。

## 一、改动前的真实状态

内核里 `kind = "local"` 早就存在，但它是**半个功能**：

| 位置 | 改动前 | 后果 |
|---|---|---|
| 资产列表 | 空。要用户自己去「新建资产」里建一个本地终端 | 「资产默认会有一台本机」不成立 |
| `connectAsset`（前端） | `kind === "local"` 绕开资产，调 `session_connect_local` | 会话绑在一个**库里并不存在**的合成资产上 |
| `connect_local_quick`（内核） | 现造 `AssetRow { id: new_id(), name: "本地终端" }` | ① 用户给资产改的名字、配的 options 全丢；② 审计 `asset_id` 挂着一个查不到的归属；③ 重连时 `asset_get` 直接报错（看着像"重连失败"，其实是身份不存在） |
| 会话复用 | 每次 `connect_asset` 都新建 Session | 双击两下 → 两个同名工作区、两条互不相干的会话（`§7` 写的「一资产一连接」从未实现） |
| 重连 | `local` 落到 `_ => Err(Unsupported)` | UI 切「重连中」→ 按 1/2/4/8/16/30s 退避试十次 → 半分钟后报「重连次数用尽」= **假装在工作** |
| Docker 日志 / 进容器 | 只 `downcast_ref::<SshTransport>()` | 本机会话一律「该通道需要 SSH 会话」——而本机跑 Docker 是最常见的用法 |
| 资产 `options` | `asset_list` 直接回 `AssetRow` | 序列化出的是 `optionsJson`（**字符串**），前端读 `options`（**对象**）→ 永远 `undefined`（同 `ai_message` / `audit_log` 那个坑） |
| `docker` 类型资产 | `connect_asset` 无此分支 | 建得出、点连接必报「资产类型 docker 不支持会话」 |

## 二、做了什么

- **schema**：`0002_asset_builtin.sql` 给 `asset` 加 `builtin INTEGER NOT NULL DEFAULT 0`。
  为什么是一列而不是前端按 ID/名字猜：内置资产要**不可删**（它是「本机」的锚点），
  这个事实必须后端与前端读同一份；ID 常量写两处、名字用户可改，都会失效。
- **seed**：`asset_ensure_builtin_local()` 幂等（固定 ID `01J0NEXTERMLOCALDEVICE0001`，
  `sort = -1` 排最前），启动时调一次；曾被软删除过则**复活**（内置资产的"不存在"
  没有第三种解释，留墓碑只会让它永远消失又占着 ID）。
- **不可删**：`asset_delete` 对 `builtin` 直接 `bad_param`。UI 也不给删除按钮 ——
  但命令层能被脚本/AI 直接调，所以两边都拦。
- **会话复用**：`connect_asset` 先找同资产上仍活着的会话（Connected / Connecting /
  Reconnecting）复用。这条同时修好了 SSH：以前双击两次 SSH 资产会开出两个工作区。
- **本机 options 生效**：`local` 分支按 `options.shell` / `options.cwd` 建传输；
  资产编辑页对本机资产开放这两个输入（空值不写入 = 回到系统默认）。
- **`docker` 类型可用**：按 SSH 处理（`docker` 主机就是一台跑着 Docker 的 SSH 机器），
  连上后前端照旧直接开容器面板。
- **AssetDto 接通**：`asset_list / get / create / update / search` 改回 `AssetDto`
  （`options` 才真的能被前端读到），并补 `builtin` 字段。

## 三、同机兼容性矩阵

| 功能 | 本机会话 | 依据 |
|---|---|---|
| 终端（真 PTY） | ✅ | `LocalTransport::open_pty`；`tests/local_pty.rs` 真 PTY 端到端 |
| 文件树 / 编辑器 / 传输 | ✅ | `LocalFs`（Unix 有 `chmod`，Windows 返回 `Unsupported` + 可读原因） |
| 容器列表 / 启停 / 镜像 | ✅ | 走 `Transport::exec` → 本机 shell |
| 容器日志 follow / 进容器 | ✅ **新** | 本机走**本地命令 PTY**；SSH 走 exec channel |
| AI 工具（exec / 读写文件 / docker） | ✅ | 全部走 `Transport`，AI 侧不按会话类型分支 |
| 端口转发 | ❌ **按设计拒绝** | 面板禁用 + 说明「出口必须是另一台机器」；内核 `Unsupported` 带同样的话 |
| 磁盘挂载 | ❌ **按设计拒绝** | 本机文件直接看文件树；面板禁用 + 说明 |
| 自动重连 | ❌ **不适用** | 本机没有"断线重连"语义，不会再假装重连 30 秒 |

## 四、证据

```
cargo fmt --all --check                                    → 通过
cargo clippy --all-targets --all-features -- -D warnings   → Finished（无告警）
CARGO_TARGET_DIR=/tmp/nx-wingnu cargo clippy \
  --target x86_64-pc-windows-gnu --all-targets --all-features -- -D warnings
                                                           → Finished in 1m 19s（Windows 分支真编过）
cargo test --all-features                                  → 212 + 2 + 1 + 1 passed / 0 failed
pnpm typecheck / pnpm lint                                 → 通过
```

新增单测（每条都在守一个具体失效）：

| 用例 | 守什么 |
|---|---|
| `builtin_id_is_a_valid_ulid_shape` | 固定 ID 形态写错（25 位/带连字符）不会编译报错，只会在用户点「新建终端」时变成「非法 ID」 |
| `ensure_builtin_is_idempotent` | 重复调用不能重建（比 `created_at`） |
| `builtin_sorts_first` | 内置资产排在自建资产前面（`sort = -1`） |
| `builtin_cannot_be_deleted` | 命令层拒绝删除 |
| `ensure_builtin_revives_tombstone` | 墓碑残留能复活，且墓碑不出现在列表里 |
| `asset_dto_unwraps_options_json` | `options` 是对象不是字符串；`builtin` 透传；坏 JSON 兜底成空对象 |
| `local_sessions_are_not_reconnectable` / `network_sessions_are_reconnectable` | 重连门控（local 不再进退避循环） |
| `local_options_become_transport_settings` / `blank_local_options_keep_defaults` | options 真落到传输上；空串视为未配置（否则清空输入框会让终端起不来） |
| `command_pty_runs_and_produces_output` | 命令 PTY 真能跑命令并回吐输出（`#[cfg(unix)]`，夹具 `printf` 不跨平台） |

**迁移在"已有旧库"上真的跑过**：把本机真实 `data.db`（只有 `_sqlx_migrations` v1、
1 条资产 `LinuxCore`）**拷贝**到临时目录，用新代码 `Store::open` 打开：

```
迁移前资产数 = 1
  - LinuxCore (ssh) builtin=false
迁移后资产数 = 2      ← 0002 已应用、builtin 列可读、内置资产已建
```

真实库**未被改动**（验证后核对：仍是 v1 + 1 条资产）。

## 五、未验证 / 边界（不要当成已完成）

- **真机 GUI 未跑**：本轮只跑了 Rust 测试、交叉 clippy、前端 typecheck/lint。
  「首启动自动连『当前设备』」「资产树里的『本机』徽标」「面板禁用态」目前只有
  代码级保证，**没有截图证据**。要补就跑 `pnpm tauri dev` 或演示模式（`?demo=1`）。
- **Windows 只有交叉编译**：`-gnu` 目标验的是类型/借用/lint，验不了 ConPTY 运行期行为。
  `open_command_pty` 在 Windows 上会走 `powershell -Command`，未实测。
- **容器日志 / 进容器**：命令 PTY 这个**底座**有测试，但「本机会话 + 真容器」的
  组合没端到端跑过（需要本机 Docker 与一个跑着的容器）。
- 内置资产可**改名**（只影响显示）；类型锁定。若将来要支持"多个本机 shell 变体"，
  应该是**再加一个普通 local 资产**，而不是放开内置资产的类型。

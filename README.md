# NexTerm — 一体化开发运维终端

> 一个轻量的、AI 原生的开发运维终端工作台：SSH（真 PTY）+ WinRM + 文件 + 挂载 + Docker + MySQL/Redis + AI（工具调用 + 终端接管）。
> 基于《产品开发文档-NexTerm》（实现规格 v1.0）落地，技术基底 **Tauri 2 + Rust（Tokio）+ React 19 + TypeScript + xterm.js**。
>
> ⚠️ **接手 UI 相关改动之前，先读 [`../UI实装引导-NexTerm.md`](../UI实装引导-NexTerm.md)。**
> 那份交接文档写清了当前 UI 的完成度（哪些已实现、哪些没做）、设计令牌与架构契约、
> 施工清单、硬规则、以及踩过的坑。本 README 只讲代码怎么跑。

## 快速开始

```bash
# 依赖：Rust stable (MSVC) + Node 22+ + pnpm 9+
pnpm install
pnpm tauri dev      # 开发窗口
pnpm tauri build    # 打包（NSIS 安装器）
```

## 浏览器演示模式（不用编译 Rust 也能看全部界面）

前后端接口只有一处：`src/ipc/commands.ts` 的 `call()`。当检测到**不在 Tauri 容器里**
（纯浏览器跑 `pnpm dev`）或 URL 带 `?demo=1` 时，它改走 `src/demo/` 的内存实现，
于是一个命令都不用改，全部面板都有内容可看可点。

```bash
pnpm dev            # 打开 http://localhost:1420/
```

演示模式包含：

- **一台会回显的虚拟 shell**（web-01）：`ls / cd / cat / free -h / df -h / ps / ss /
  systemctl / journalctl / nginx -t / docker ps|logs|stats|restart …`，Tab 补全与 ↑↓ 历史都可用。
  终端里的命令与输出是真实字节流，所以**命令块面板、搜索、录制全都真的能用**。
- 8 台资产 / 6 个容器 / 7 个镜像、MySQL（库表 + 字段 + 索引 + 假查询结果）、Redis（SCAN + 值查看 + 命令台）、
  文件树（家目录 `~` = `/home/deploy`，外加 `/etc/nginx`、`/data/app`、`/data/logs` 等，
  双击 `console.log`、`.bashrc`、`reset_snapshot.py` 都能真的打开编辑）、挂载、审计日志、MCP 工具权限、凭据库。
- AI 对话会按提问内容**流式**返回工具调用卡片与结论；说"重启 mysql-prod"会走**确认卡片**；
  点「AI 接管此终端」能看接管横幅、读屏事件与夺屏按钮。

想临时关掉：URL 加 `?demo=0`（这时需要有真实的 Tauri 后端）。

> 假数据与虚拟 shell 近 60KB，是**动态加载**的独立 chunk，Tauri 生产包不会请求到它们
> （`pnpm build` 产出的 `assets/mock-*.js`）。

## 质量门（§0.2）

```bash
cargo fmt --all --check
cargo clippy --all-targets --all-features -- -D warnings
cargo test --workspace
pnpm typecheck && pnpm lint
```

## 两级标签：工作区 → 面板 → 标签

界面是三层结构，因为实际用起来一定是"同时开几台机器，每台上各有一堆终端和文件"，
扁平的一排标签很快就没法用了。

```
工作区（一级标签，= 一台机器 / 一个数据库连接）
 └─ 面板（分屏格，1 个或上下 2 个）
     └─ 标签（终端 / 编辑器 / 容器 / 数据库面板）
```

- **一级：工作区**（`Workspace`）—— 顶栏每个工作区带类型图标、连接状态点和关闭按钮，
  末尾的 `+` 提示"去左侧资产树双击一台机器"。
- **二级：面板**（`Pane`）—— 上下分屏的两个格子，各自有一排标签和自己的激活项。
  标签条右侧有分屏按钮（`Ctrl+\` 同效），取消分屏固定收掉下面那一栏。
  分屏时激活栏的标签条有一条强调线、另一栏的标签压暗。
- **三级：标签**（`AppTab`）—— 每栏末尾同样有 `+`（在当前栏再开一个终端）。

分屏的几条规则：

- **分割条可拖拽**，比例存在工作区上（`splitRatio`，夹在 0.15~0.85），
  切走再回来还记得。
- **给会话工作区分屏时会自动开一个新终端** —— 分屏的动机基本都是"一边敲命令、
  一边看日志"，空面板等于还要多点一次。
- **关掉某栏的最后一个标签 = 取消分屏**，不留下空死格子。
- 终端编号跨栏连续（`终端 1` / `终端 2` …），不按栏各自从 1 开始。

几个刻意的设计：

- **标签按身份归属，不按"当前窗口"归属**。`addTab` 会先用 `sessionId` / `connId`
  找到对应工作区；找不到才补建。容器面板这类页面背后的异步回调可能在用户已经
  切走之后才开标签，按身份归属才不会被开到错误的机器上。
  落到哪一栏则默认跟"正在用的那一栏"，只有编辑器标签会聚到已有编辑器的那一栏。
- **所有工作区、所有面板、所有标签都保持挂载**（用 `hidden` 藏起来，不是卸载）。
  只挂载当前可见部分的话，切一次工作区或收一次分屏就要重新 `terminal_attach`，
  内核会 `open_pty` 开一个新 shell，旧 shell 泄漏在远端。
  同时 `visible` 必须同时满足"标签激活"与"所在栏激活"，否则隐藏面板里的
  终端会 `fit()` 到 0 高度并把远端 PTY 改小。
- **关闭工作区不断开连接**：断连是一个明确动作，关工作区只是回收它里面的终端。

## 左栏：资产 ↔ 文件树

左栏是常驻的，有两种形态，由图标栏最上面那两个按钮切换：

- **资产**（`IconServer`）—— 资产管理：分组、新建、搜索、双击连接，也就是"管理面"。
- **文件树**（`IconFolderOpen`）—— 当前会话的文件树，也就是"干活面"。

**连上任意一台机器（SSH / WinRM / 本地 / Docker 主机）后会自动切到文件树**，
因为实际节奏是"连一台机器，然后一直在这台机器上干活"，连完还要再点一次「文件」是多余的一步。

文件树（`src/features/files/FileTree.tsx`）：

- 根目录先试 `~`；**真机实测后端并不展开 `~`**（`fs_list("~")` 报
  `系统找不到指定的路径`，`session_cwd` 对本地 / SSH 会话返回 `null`），
  所以实际会退回 `/`，同时把「回到家目录」按钮置灰并弹一条说明，
  免得点了没反应看着像坏了。面包屑可逐段点击跳转。
- 目录按需加载，用 react-query 按目录缓存，**与宽幅文件浏览器共用同一个 queryKey**，
  所以两边看到的是同一份数据。
- 工具栏：新建文件 / 新建文件夹 / 刷新 / 折叠全部 / 上传 / 下载 / 删除；行内 hover 还有下载与删除。
- 文件类型 → 图标与配色统一在 `src/features/files/fileTypes.ts`，左栏树、宽幅浏览器、
  编辑器标签三处共用，避免同一个文件在不同位置长得不一样。
- **双击可编辑文件 → 开编辑器标签**；二进制扩展名会被挡下并提示走下载。

编辑器（`src/features/files/FileEditor.tsx`）工具条：保存 / 撤销 / 重做 / 查找替换 /
缩放 / 换行符 LF⇄CRLF / 编码（UTF-8 / GBK / 自动，切换会重新读盘）。更宽的入口
（含体积与修改时间列）在命令面板里的「打开宽幅文件浏览器」。

## 视觉系统

界面风格对标 同类工具：微冷深灰分层、用明度差而不是边框做层次、单一强调色、圆角与字号都收敛成一套。

- **设计令牌**集中在 `src/styles.css` 的 `@theme` 里。重定义整套 `--color-neutral-*` 即可全局换肤 ——
  因为所有面板用的都是 `neutral-*` 原子类，改一处、全站生效。
  **换强调色只动 7 行**：`--color-accent` + `--color-blue-50/300/400/500/600/700/900`；
  终端光标是运行期读 `--color-accent` 的，别再把它硬编码回 `XtermView`。
- **强调色是靛蓝 `#516cd6`**，色相取自应用图标（靛蓝 H228/H240），**不是**图标上那支
  荧光青 `#00F0FF` —— 那只是光标与发光边缘的点缀，铺满选中态会非常刺眼。
  但**终端 ANSI 的 `blue`/`brightBlue` 与 `editorTheme.ts` 的 token 色不跟强调色走**：
  它们跟的是「终端 ANSI 语义色板」这条独立的轴（`ls` 的目录、git 输出都靠它），
  要比强调色亮一档才读得清。两个文件里都写了注释，换色时别顺手一起同步。
- **语义类**（`.nx-pane / .nx-btn / .nx-input / .nx-tab / .nx-badge / .nx-table / .nx-rail-btn / .nx-modal` …）
  也定义在 `styles.css`，新面板优先用它们，避免再散写长串原子类。
- **图标**统一在 `src/ui/icons.tsx`（内联 SVG，零依赖），资产类型映射见 `assetIcon()`。
- **编辑器配色**见 `src/ui/editorTheme.ts`：复用 CodeMirror 内置样式的 `specs` 重新着色，
  所以既不用新增 `@lezer/highlight` 依赖，也不会被版本改名影响。
- **品牌标识**：应用图标与界面内 Logo 来自同一套图（方案 A 终端少女）。
  界面内引用 `public/brand/nexterm-mark-128.png` —— 它是**头部特写裁切版**，不是整幅原图
  （26px 图标栏里整幅插画会糊）。favicon 是 `public/icon.png`（交付原图 48px），
  各平台图标由 `pnpm tauri icon` 从 `src-tauri/icons/source.png` 生成。
  换方案/换变体的命令见 `../图标交付规格-NexTerm.md` §0。

## 架构总览

```
┌──────────── WebView2（前端 React 19 + xterm.js WebGL）────────────┐
│  资产树 · 标签/分屏 · 终端 · 文件 · Docker · DB · AI 侧栏           │
└──────────────────────── Tauri IPC ────────────────────────────────┘
          结构化事件(events.rs) + 二进制通道(Channel<Vec<u8>>)
┌──────────────────────── Rust 内核（Tokio）────────────────────────┐
│ session/ 会话池·重连    terminal/ PTY+vt100状态机+环形缓冲+背压     │
│ transport/ ssh·winrm·local·forward    fs/ 传输·挂载                │
│ docker/ CLI通道         db/ mysql·redis       ai/ agent·guard·    │
│ vault/ 两级密钥加密      store/ SQLite(WAL)    provider·takeover   │
│ mcp/ 对外(stdio+HTTP，token+权限门)                                │
└────────────────────────────────────────────────────────────────────┘
```

关键设计（对应开发文档铁律）：

- **L1 终端字节流不走 JSON**：PTY 输出经 `tauri::ipc::Channel<Vec<u8>>` 直送 xterm.js；输入走 invoke。
- **L2 内核侧终端状态机**：每条 PTY 字节流同时喂给内核 `vt100::Parser`（AI 读屏/空闲判定/接管的唯一数据源）与前端。
- **L3 异步运行时不阻塞**：本地 PTY 读取走「阻塞线程 + 有界通道」桥接，端到端背压（§4.4：4MB 暂停 + throttled 事件 + 隐藏标签 50ms 批量）。
- **L4 命令层薄**：`commands/` 只做反序列化 → 校验 → 调 service → 包装错误。
- **凭据两级密钥**（§9）：主密码 -Argon2id(m=64MB,t=3,p=4)→ KEK →(ChaCha20-Poly1305)→ DEK 信封；凭据用 XChaCha20-Poly1305(DEK)；另有 Windows DPAPI 免主密码模式；日志经 `vault::redact` 脱敏。
- **AI 护栏**（§8.3）：Safe 白名单直执行 / NeedsConfirm 弹确认卡片（可"本会话允许此类"）/ Forbidden 直接拒绝；接管模式下用户任意按键立即夺回。

## v1 功能清单（§8 技术选型）

| 模块 | 说明 |
|---|---|
| 终端 | SSH 真 PTY（russh）、本地 ConPTY（portable-pty）、WinRM 非交互行模式；多标签、搜索、录制、编码切换（UTF-8/GBK/GB18030/Big5） |
| 会话 | 一资产一连接多标签复用；关闭标签不断连（空闲 30 分钟自动断）；指数退避自动重连 + 横幅 |
| 文件 | SFTP 浏览（虚拟滚动）、上传/下载（进度事件 + 断点续传）、CodeMirror 编辑器（保存即备份）、MD5/SHA256 |
| 挂载 | Windows `net use` 映射盘 / Linux sshfs；列表以本机真实状态为准 |
| Docker | CLI 通道：容器列表/日志 follow/容器终端/启停删/镜像管理/容器文件浏览 |
| 数据库 | MySQL（库表浏览 + SQL 工作台 + 结果表格）、Redis（SCAN 分页 + 类型感知查看 + TTL + 命令台） |
| AI | OpenAI 兼容（流式 + 降级整块 + 两步连通性测试 + 预设自动填参）；工具调用（命令实时汇入终端 + 审计）；终端接管（读屏/空闲判定/发键/横幅/一键夺回/只读接管） |
| MCP | stdio + streamable HTTP；token 校验；默认只读、写工具逐个开闸 |
| 端口转发 | SSH 本地转发（复用会话，打通内网 DB） |

## 明确不做（v1）

同步 Server（M5 预留事件位）· PostgreSQL/MongoDB/国产库 · RDP/VNC/Telnet/串口 · 命令广播 · 跳板机 · 移动端

## 目录

见《产品开发文档-NexTerm》§2；`docs/bench-m0.md` 为 M0-T6 吞吐基准记录。

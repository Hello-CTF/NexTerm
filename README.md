<div align="center">

<img src="src-tauri/icons/128x128.png" width="96" alt="NexTerm">

# NexTerm

**一体化开发运维终端 —— SSH · WinRM · 文件 · Docker · 数据库 · AI，装进同一个窗口**

本地优先、AI 原生的桌面终端工作台。凭据加密存储，AI 全程在权限护栏内执行。

[![Website](https://img.shields.io/badge/%E5%AE%98%E7%BD%91-online-516cd6)](https://probiusofficial.github.io/NexTerm/)
![Platform](https://img.shields.io/badge/platform-Windows%20%7C%20macOS-blue)
![Rust](https://img.shields.io/badge/Rust-stable-orange)
![Tauri](https://img.shields.io/badge/Tauri-2-24C8D8)
![React](https://img.shields.io/badge/React-19-61DAFB)
![License](https://img.shields.io/badge/license-MIT-green)

</div>

---

![NexTerm 主界面](docs/images/hero-ai-terminal.png)

运维工具链历来分散：SSH 客户端、SFTP 工具、Docker 面板、数据库客户端各占一个窗口，AI 助手还要切出去粘贴报错。NexTerm 将它们收进同一个三层工作区——工作区、分屏面板、标签，并为 AI 提供一条**看得见、管得住**的执行通道：每条命令、每次文件修改实时可见，危险操作先经确认。

## 功能总览

| 模块 | 能力 |
|---|---|
| 终端 | SSH 真 PTY、本地 ConPTY、WinRM；多标签与分屏、搜索、会话录制，支持 UTF-8 / GBK / GB18030 / Big5 编码切换 |
| 会话 | 一台资产一条连接复用，关标签不断连；SSH / WinRM 指数退避自动重连（本机会话没有"重连"这件事，不会假装在重连） |
| 资产 | 内置「当前设备」本地资产 —— 装好即有一台机器（本机终端 / 文件树 / 容器面板都落在它上面），不可删除、可改名、可配默认 Shell 与起始目录；另有 SSH / WinRM / Docker / MySQL / Redis 资产，支持分组、搜索、拖拽归类 |
| 文件 | SFTP 浏览与虚拟滚动、带进度与断点续传的传输；内置编辑器支持查找替换、LF/CRLF 转换与编码切换；MD5 / SHA256 校验 |
| 挂载 | Windows `net use` 映射盘、Linux sshfs（本机文件直接看文件树，不需要挂载） |
| Docker | 容器列表、日志 follow、容器终端、启停删、镜像管理、容器文件浏览；SSH 主机与本机都可用 |
| 数据库 | MySQL 库表浏览与 SQL 工作台；Redis SCAN 分页、类型感知查看与命令台 |
| 端口转发 | SSH 本地转发，复用已有会话打通内网数据库 |
| AI 助手 | OpenAI 兼容多模型、工具调用、权限护栏、终端接管、文件变更 diff、计划模式、上下文用量与缓存命中统计 |
| 凭据库 | 两级密钥信封加密、集中管理面板、引用关系与删除保护、日志自动脱敏 |

## AI 助手

AI 不是贴在旁边的聊天框，而是接进了内核。

- **工具调用透明** —— AI 执行的每条命令以卡片进入对话流，展开可见完整输出与退出码；命令运行在独立执行通道，不伪装成向终端打字。
- **权限护栏** —— 操作按风险分级：安全操作直接执行；敏感操作弹确认卡片，可对本会话放行同类；格式化、批量删除等危险命令一律拒绝。只读、读写、静默三档模式随时切换。

![AI 权限面板](docs/images/ai-permission.png)

- **文件变更可审** —— `write_file` / `edit_file` 在**确认卡片上先给出逐行 diff**（新建文件整份标为新增），执行后再落一张「变更记录」卡片。改动一目了然，而且是**批准之前**就一目了然。

![文件变更 diff](docs/images/ai-file-changes.png)

- **终端接管 · 实验性** —— 基于内核侧终端状态机读屏、判定空闲、发送按键；按任意键立即夺回，全程显示接管横幅。
- **多模型与成本可见** —— 任意 OpenAI 兼容端点，两步连接测试；上下文用量环与缓存命中率常驻侧栏底部。

![模型配置](docs/images/model-config.png)

## 工作区

三层结构：工作区对应一台机器或一个连接，其下分屏，屏内开标签。标签始终挂载，切换不重连、不丢终端状态；分屏比例持久化，为一边敲命令、一边看日志的场景而设计。

![分屏与编辑器](docs/images/split-pane-editor.png)

连接资产后左栏切换为文件树，双击进入内置编辑器；命令面板提供宽幅文件浏览器。

![端口转发](docs/images/port-forward.png)

## 下载安装

到 [Releases](https://github.com/ProbiusOfficial/NexTerm/releases/latest) 下载：

| 平台 | 文件 |
|---|---|
| Windows 10/11 x64 | `NexTerm_x.y.z_x64-setup.exe`（NSIS 安装器，双击即装） |
| macOS（Apple Silicon） | `NexTerm_x.y.z_aarch64.dmg`（拖入「应用程序」） |

### macOS 首次打开被拦下怎么办

安装包是 **ad-hoc 签名、未公证**的（没有 Apple Developer 证书），所以首次打开会被 Gatekeeper 拦一次。
这是预期行为，不是文件损坏：

1. 双击应用，看到「无法验证开发者 / 无法检查是否包含恶意软件」的提示 → 点**完成**
2. 打开 **系统设置 → 隐私与安全性**，下拉到「安全性」，点 **「仍要打开」**
3. 之后正常双击即可

若提示的是**「已损坏，无法打开」**（而不是"无法验证开发者"），那是签名问题，用命令行一次修掉：

```bash
xattr -dr com.apple.quarantine /Applications/NexTerm.app
```

> 只有 Intel Mac？目前只发 Apple Silicon 包，可用 Rosetta 或从源码构建
> （`pnpm tauri build --target x86_64-apple-darwin`）。

## 快速开始（从源码）

支持 Windows 与 macOS，从源码构建：

```bash
# 依赖：Rust stable（≥1.98）、Node 22+、pnpm 11+
#   Windows：MSVC 工具链；macOS：Xcode Command Line Tools
git clone https://github.com/ProbiusOfficial/NexTerm.git
cd NexTerm
pnpm install
pnpm tauri dev      # 开发窗口
pnpm tauri build    # Windows 出 NSIS 安装器；macOS 出 .app + .dmg
```

双平台 CI（Windows + macOS 各跑 fmt / clippy / test / typecheck / lint）在每次 push 时把关；
打 `v*` tag 由 `release.yml` 自动出两个平台的安装包并挂到 Release。

### 浏览器演示

免编译 Rust 预览全部界面，在线演示直接访问 [probiusofficial.github.io/NexTerm/demo](https://probiusofficial.github.io/NexTerm/demo/)。
前后端仅 `src/ipc/commands.ts` 的 `call()` 一个接口，纯浏览器运行或 URL 带 `?demo=1` 时自动切换内存 mock：

```bash
pnpm dev            # http://localhost:1420/
```

演示包含一台回显虚拟 shell，支持 `ls`、`cat`、`systemctl`、`docker ps` 与 Tab 补全、历史记录；另有 8 台资产、MySQL / Redis 面板、文件树与编辑器、AI 流式对话与确认卡片，命令块、搜索、录制全部可用。mock 为独立 chunk，生产包不加载；URL 加 `?demo=0` 回到真实后端。

## 架构

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
└────────────────────────────────────────────────────────────────────┘
```

关键设计：

- **终端字节流不走 JSON** —— PTY 输出经 `Channel<Vec<u8>>` 直送 xterm.js，输入走 invoke。
- **内核侧终端状态机** —— PTY 字节流同步喂给内核 `vt100::Parser`，是 AI 读屏、空闲判定与终端接管的唯一数据源。
- **端到端背压** —— 本地 PTY 以阻塞线程加有界通道桥接；4MB 暂停、节流事件、隐藏标签批量刷新。
- **凭据两级密钥** —— 主密码经 Argon2id 派生 KEK，信封加密 DEK；凭据以 XChaCha20-Poly1305 加密；支持 Windows DPAPI 免主密码模式；日志统一脱敏。
- **AI 护栏独立成层** —— 风险分级与放行决策单一入口，不散落在调用点。

## 质量门

```bash
cargo fmt --all --check
cargo clippy --all-targets --all-features -- -D warnings
cargo test --workspace
pnpm typecheck && pnpm lint
```

## Roadmap

其他系统正在构建中。

## 致谢

终端模拟基于 [xterm.js](https://github.com/xtermjs/xterm.js)，SSH 基于 [russh](https://github.com/Eugeny/russh)，桌面框架为 [Tauri](https://tauri.app)。

## License

[MIT](LICENSE)

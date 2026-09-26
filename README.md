# NexTerm — 一体化开发运维终端

> 一个轻量的、AI 原生的开发运维终端工作台：SSH（真 PTY）+ WinRM + 文件 + 挂载 + Docker + MySQL/Redis + AI（工具调用 + 终端接管）。
> 基于《产品开发文档-NexTerm》（实现规格 v1.0）落地，技术基底 **Tauri 2 + Rust（Tokio）+ React 19 + TypeScript + xterm.js**。

## 快速开始

```bash
# 依赖：Rust stable (MSVC) + Node 22+ + pnpm 9+
pnpm install
pnpm tauri dev      # 开发窗口
pnpm tauri build    # 打包（NSIS 安装器）
```

## 质量门（§0.2）

```bash
cargo fmt --all --check
cargo clippy --all-targets --all-features -- -D warnings
cargo test --workspace
pnpm typecheck && pnpm lint
```

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

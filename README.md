<div align="center">

<img src="website/public/brand/nexterm-mark-128.png" width="88" alt="NexTerm" />

# NexTerm

**一个可自托管的工作台，管好每一台服务器。**

永不掉线的终端 · 会动手的 AI 助手 · 文件、Docker、数据库、设备管理与终端分享

[![Website](https://img.shields.io/badge/%E5%AE%98%E7%BD%91-nexterm.work-516cd6)](https://nexterm.work/)
[![Release](https://img.shields.io/github/v/release/Hello-CTF/NexTerm?color=516cd6)](https://github.com/Hello-CTF/NexTerm/releases/latest)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8)
![Wails](https://img.shields.io/badge/Wails-v3-CC0000)
![React](https://img.shields.io/badge/React-19-61DAFB)
![License](https://img.shields.io/badge/license-MIT-green)

**中文** | [English](README_EN.md)

[官网](https://nexterm.work/) · [下载](https://github.com/Hello-CTF/NexTerm/releases/latest) · [部署服务端](#服务端与设备接入)

</div>

<picture>
  <source media="(prefers-color-scheme: light)" srcset="website/public/images/shell-light.jpg" />
  <img src="website/public/images/shell-dark.jpg" alt="NexTerm 的 Shell 终端与命令块界面" />
</picture>

图片使用示例数据。可在[官网](https://nexterm.work/)点击体验终端、AI、文件和回放演示。

NexTerm 把远程运维的完整流程放进一个窗口：通过 SSH、WinRM 或 NexTerm 设备 agent 连接主机，让 AI 助手在你确认后排查和修复问题，再处理文件、容器、数据库、设备监控和终端分享，全程不用切换工具。shell 在后台持续运行，关闭窗口或换一台设备都不会丢失进度。

<table>
<tr>
<td width="33%" valign="top">

**🤖 会动手的 AI**<br/>
20+ 工具覆盖终端、文件、Docker 和数据库，支持计划模式、diff 预览和按主机授权。

</td>
<td width="33%" valign="top">

**🖥️ 不掉线的终端**<br/>
SSH 和设备会话在 App 重启、网络中断后继续运行，可从任意已登录设备接管。

</td>
<td width="33%" valign="top">

**🧰 内置日常运维**<br/>
SFTP 与在线编辑、Docker、MySQL、PostgreSQL、Redis、端口转发和 SSH 配置导入。

</td>
</tr>
<tr>
<td valign="top">

**🛰️ 设备管理**<br/>
一行命令安装 agent，设备主动外连，查看 CPU、内存、磁盘指标并打开远程终端。

</td>
<td valign="top">

**🔗 终端分享**<br/>
限时、可吊销的终端链接，默认只读，无需交出密码或私钥。

</td>
<td valign="top">

**🔐 自托管**<br/>
多用户服务端与双因素认证、端到端加密同步、凭据保险库和审计日志。

</td>
</tr>
</table>

## 亮点

### 🤖 会动手的 AI 助手

- **20+ 内置工具**：执行命令；读写和编辑文件；查看 Docker 容器与日志；查询 MySQL、PostgreSQL 和 Redis；派生子任务。
- **控制权在你手里**：只读或可执行模式、计划模式；执行前展示命令或文件 diff，可「允许一次」「本轮允许」或「拒绝」，高危操作始终询问。
- **按主机授权**：按资产授予执行命令、写文件、发送按键等权限，可限定路径（如 `/etc/nginx/**`），有效期 1 小时到 30 天。
- **终端接管**：AI 可以在你正在使用的终端里直接输入，按 Esc 随时收回控制。
- **定时任务与长期记忆**：按 cron 表达式定时运行 AI 任务；长期记忆拒绝写入密钥类内容。
- **任意模型**：OpenAI 兼容接口，内置 DeepSeek、Kimi、GLM、Ollama 预设，支持回退模型、代理和用量统计。

### 🖥️ 不会掉线的终端

- **后台会话**：SSH 和受管设备会话在关闭标签、App 重启或网络中断后继续运行，可从任意已登录设备接管并补回错过的输出。本机、WinRM 非交互与容器 exec 终端随关闭结束。
- **多设备协同**：多台设备可同时观看同一终端，同一时间一台输入。
- **命令块、历史与回放**：每条命令折叠成块；自动录制输出、键盘输入和窗口尺寸变化，支持搜索和时间轴回放。记录默认仅本地保存、不参与同步；可逐条开启端到端加密同步，也可逐条删除。
- **效率**：多标签、分屏、命令广播、快速连接、命令面板、片段和可自定义快捷键。

> [!WARNING]
> 终端历史会原样记录键盘输入，包括密码和令牌。

### 🧰 日常运维，一个窗口搞定

- **文件**：SFTP、WinRM 与设备文件浏览，上传下载，在线编辑（保存前自动备份），大日志分页查看，跟随终端目录。
- **Docker**：容器、日志、资源统计、镜像管理，一键 exec 进入容器。
- **数据库**：MySQL/MariaDB 与 PostgreSQL 的 SQL 编辑器和表结构浏览，Redis 键浏览。
- **端口转发**：本地转发与 SOCKS5 代理，断线自动重连。
- **导入**：从 `~/.ssh/config` 或 Termius 导入主机和密钥，支持分组、标签、跳板机和 SSH 证书。

PostgreSQL 与普通 SSH/SFTP 无需远端 agent，agent 仅用于设备管理。

### 🛰️ 设备管理与分享

- **设备 agent**：一行命令接入主机，设备主动连回服务端，上报 CPU、内存和磁盘指标，无需暴露 SSH 即可打开远程终端和文件。
- **分享**：接入的设备可分享给注册用户，也可创建公开链接。获得链接的人在有效期内无需登录即可访问；链接默认只读，只有显式授权才允许读写，且可随时吊销。分享无需提供主机密码或私钥。

> [!CAUTION]
> 读写权限允许对方在终端中输入，请仅授予可信对象。

### 🔐 自托管，安全默认

- **账号与同步**：服务端内置多用户账号与 TOTP 双因素认证，超管可创建用户或开放注册；同步数据按账号隔离并端到端加密。桌面端登录后自动同步资产、分组、片段、凭据及可选配置。
- **凭据保险库**：主密码或系统级保护（DPAPI / 钥匙串），移动端支持生物识别解锁，可闲置自动锁定。
- **审计日志**：用户与 AI 的命令、文件写入和设备事件都有记录。
- **选择你的数据库**：默认 SQLite，服务端也支持 PostgreSQL、MySQL 和 MariaDB。
- **桌面与浏览器**：浏览器模式下，终端进程和工作区保存在服务器上，关闭网页后仍可继续。

## 截图

<table>
<tr>
<th width="33%">AI 助手</th>
<th width="33%">远程文件</th>
<th width="33%">终端回放</th>
</tr>
<tr>
<td><picture><source media="(prefers-color-scheme: light)" srcset="website/public/images/ai-light.jpg" /><img src="website/public/images/ai-dark.jpg" alt="AI 助手对话与命令确认" /></picture></td>
<td><picture><source media="(prefers-color-scheme: light)" srcset="website/public/images/files-light.jpg" /><img src="website/public/images/files-dark.jpg" alt="SFTP 文件树与远程编辑" /></picture></td>
<td><picture><source media="(prefers-color-scheme: light)" srcset="website/public/images/replay-light.jpg" /><img src="website/public/images/replay-dark.jpg" alt="终端历史时间轴回放" /></picture></td>
</tr>
</table>

## 下载与安装

从 [Releases](https://github.com/Hello-CTF/NexTerm/releases/latest) 下载最新版本。

| 平台 | 安装包 | 安装方式 |
| --- | --- | --- |
| Windows 10/11（x64 / ARM64） | `NexTerm_x.y.z_<架构>-setup.exe` | 运行安装器。 |
| macOS（Apple Silicon / Intel） | `NexTerm_x.y.z_<架构>.dmg` | 打开后拖入「应用程序」。 |
| Linux 桌面（amd64 / arm64） | `NexTerm-desktop_x.y.z_linux_<架构>.deb` | `apt install ./<包名>.deb` |
| Linux 服务器 | `NexTerm-server_x.y.z_linux_<架构>.tar.gz` | 见[服务端与设备接入](#服务端与设备接入)。 |
| Android | `NexTerm_x.y.z_android.apk` | arm64 设备直接安装。 |
| iOS / iPadOS | `NexTerm_x.y.z_ios.ipa` | ad-hoc 签名。 |
| 懒猫微服 | 应用中心 | 在应用中心安装 NexTerm。 |

> [!TIP]
> macOS 首次打开提示无法验证开发者时，在「系统设置 → 隐私与安全性」中点「仍要打开」；提示应用已损坏则执行 `xattr -dr com.apple.quarantine /Applications/NexTerm.app`。

### 快速开始

1. **添加主机**：新建 SSH 或 WinRM 资产，也可以直接使用内置的「当前设备」。
2. **初始化凭据库**：保存密码或私钥前，先在 **设置 → 凭据保护** 初始化凭据库，否则无法保存。Windows/macOS 可使用系统级密钥免密保护（DPAPI/钥匙串）；Linux 桌面端请设置至少 8 位保护密码。设置保护密码后，每次启动都要输入该密码解锁；也可按需开启闲置自动锁定。
3. **接入模型（可选）**：在 **设置 → AI 模型** 新增模型档案，填写 OpenAI 兼容接口地址和模型名称，按端点要求填写 API Key（无鉴权的本地端点可留空），然后保存。
4. **同步（可选）**：桌面端不登录也可本地使用；在 **设置 → 账号同步** 登录后自动在多设备间同步。

> [!IMPORTANT]
> 保护密码仅用于本机解锁，不会上传或随账号同步；忘记后已保存的凭据无法恢复。

<details>
<summary><strong>自动更新</strong></summary>

桌面版启动后自动检查一次更新，发现新版本时显示顶部横幅；在「设置 → 软件更新」查看当前版本、新版本和安装包大小，并下载安装。更新说明见对应 Release；预发布版接收预发布更新，正式版仅接收正式版；安装前按 Release 附带的 `SHA256SUMS` 校验。服务端不自动更新，请按部署包内说明升级。

</details>

## 服务端与设备接入

### Linux 服务器

1. 解压 server 包后按包内 `README.md` 安装。
2. 首次启动会在控制台打印一次性初始化码（systemd 下用 `journalctl -u nexterm-server` 查看）。
3. 在浏览器输入初始化码，并设置超管用户名和密码。

- 访问控制默认开启（`--auth on`），无论监听地址如何均要求登录。
- 自身数据默认存入 SQLite，也可通过 `--db postgres|mysql|mariadb` 和 `--db-dsn` 使用 PostgreSQL、MySQL 或 MariaDB；同一个数据库仅运行一个服务端实例。
- 备份时保留完整数据目录；使用外部数据库时还需备份该数据库。
- 完整选项见 `nexterm-server --help`。

<details>
<summary><strong>公网部署与反向代理</strong></summary>

公网部署请只监听回环地址，并前置带 TLS 的反向代理。代理应覆盖设置 `X-Forwarded-Proto`；将代理的来源 IP 段写入 `NEXTERM_TRUSTED_PROXY_CIDRS`（逗号分隔，例如 `127.0.0.1/32`），登录 Cookie 才会在代理 HTTPS 请求上设置 `Secure`，其他来源的转发头不会被信任。

</details>

### 懒猫微服

应用中心安装后直接使用，终端进程和数据保存在微服上；NexTerm 内置端口转发不可用，对外提供远端端口请使用平台转发。

### 设备接入

1. 接入地址由超管在「设备管理」配置。
2. 接入码也在此处（浏览器模式，或桌面端登录服务端账号后）签发，一次性、限时有效。
3. Linux/macOS 用普通用户执行面板的一行命令，Windows 执行 PowerShell 命令，自动下载并安装 nexterm-agent，并配置随用户登录启动（systemd --user / launchd / Windows 计划任务）。

设备凭证原文仅保存在设备本地，服务端只存散列；接入后，设备在线时可远程打开终端，吊销后凭证立即失效且不可恢复。

## 从源码构建与测试

工具链：Go 1.26、Node 24+、pnpm。

```bash
pnpm install
pnpm build
go build ./...
```

常规质量检查（需安装 task）：

```bash
task check
```

<details>
<summary><strong>数据库集成测试</strong></summary>

外部数据库使用同一套集成测试，测试库必须独立于业务库：

```bash
NEXTERM_TEST_DB_BACKEND=postgres NEXTERM_TEST_DB_DSN='postgres://nexterm:password@127.0.0.1:5432/nexterm_test?sslmode=disable' task test:database
```

`NEXTERM_TEST_DB_BACKEND` 也可设为 `mysql` 或 `mariadb`，DSN 使用对应驱动格式。未设置时运行 SQLite；显式选择外部数据库必须提供可用 DSN。

</details>

## 贡献与提交规范

从最新 `master` 切分支，经 PR 合回；PR 标题和提交消息遵循 Conventional Commits。PR 说明要解决的问题、具体改动和验证方式，用户可感知的变化及行为变化都要写清楚。提交前完成全部质量检查；开发、评审和协作约定见 [AGENTS.md](AGENTS.md)。

## License

[MIT](LICENSE)

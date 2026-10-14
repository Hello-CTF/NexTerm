<div align="center">

# NexTerm

**SSH、WinRM、文件、Docker、数据库和 AI，集中在一个工作台。**

[![Website](https://img.shields.io/badge/%E5%AE%98%E7%BD%91-online-516cd6)](https://nexterm.work/)
![Go](https://img.shields.io/badge/Go-1.26-00ADD8)
![Wails](https://img.shields.io/badge/Wails-v3-CC0000)
![React](https://img.shields.io/badge/React-19-61DAFB)
![License](https://img.shields.io/badge/license-MIT-green)

**中文** | [English](README_EN.md)

</div>

## 功能

- **终端与文件**：SSH、WinRM 和本机终端，支持多标签、分屏、SFTP 文件管理与在线编辑。SSH 会话关闭标签或断开后转入「后台会话」继续运行，可重新接管；本机、WinRM 非交互与容器 exec 终端随关闭结束。
- **终端历史**：自动录制每条终端会话的输出、键盘输入和窗口尺寸变化，支持文本查看、搜索和时间轴回放。**注意：键盘输入会原样记录，包括密码和令牌。**记录默认仅本地保存、不参与同步；可逐条开启端到端加密同步，也可逐条删除。
- **日常运维**：管理 Docker 容器与镜像，使用 PostgreSQL、MySQL、Redis 和 SSH 端口转发；PostgreSQL 与普通 SSH/SFTP 无需远端 agent，agent 仅用于设备管理。
- **AI 助手**：支持 OpenAI 兼容接口、终端接管和定时任务；命令输出与文件变更可见，敏感操作需要确认。
- **账号与同步**：服务端内置多用户账号，超管可创建用户或开放注册；同步数据按账号隔离并端到端加密。桌面端登录后自动同步资产、分组、片段、凭据及可选配置。
- **设备管理与分享**：安装设备 agent 后可远程打开设备终端；接入的设备可分享给注册用户，也可创建公开链接。获得链接的人在有效期内无需登录即可访问；链接默认只读，只有显式授权才允许读写，且可随时吊销。读写权限允许对方在终端中输入，请仅授予可信对象。分享无需提供主机密码或私钥。
- **桌面与浏览器**：桌面端用于本机；服务端的终端进程和工作区保存在服务器上，关闭网页后仍可继续。

## 下载与安装

从 [Releases](https://github.com/Hello-CTF/NexTerm/releases/latest) 下载最新版本。

| 平台 | 安装包 |
| --- | --- |
| Windows 10/11（x64 / ARM64） | `NexTerm_x.y.z_<架构>-setup.exe`，运行安装器。 |
| macOS（Apple Silicon / Intel） | `NexTerm_x.y.z_<架构>.dmg`，打开后拖入「应用程序」。 |
| Linux 桌面（amd64 / arm64） | `NexTerm-desktop_x.y.z_linux_<架构>.deb`，用 `apt install ./<包名>.deb` 安装。 |
| Linux 服务器 | `NexTerm-server_x.y.z_linux_<架构>.tar.gz`。 |
| Android | `NexTerm_x.y.z_android.apk`，arm64 设备直接安装。 |
| iOS / iPadOS | `NexTerm_x.y.z_ios.ipa`（ad-hoc 签名）。 |
| 懒猫微服 | 在应用中心安装 NexTerm。 |

macOS 首次打开提示无法验证开发者时，在「系统设置 → 隐私与安全性」中点「仍要打开」；提示应用已损坏则执行 `xattr -dr com.apple.quarantine /Applications/NexTerm.app`。

首次使用：

1. 添加 SSH 或 WinRM 资产，也可以直接使用内置的「当前设备」。
2. 保存密码或私钥前，先在「设置 → 凭据保护」初始化凭据库，否则无法保存。Windows/macOS 可使用系统级密钥免密保护（DPAPI/钥匙串）；Linux 桌面端请设置至少 8 位保护密码。保护密码仅用于本机解锁，不会上传或随账号同步；忘记后已保存的凭据无法恢复。设置保护密码后，每次启动都要输入该密码解锁；也可按需开启闲置自动锁定。
3. 使用 AI 时，在「设置 → AI 模型」新增模型档案，填写 OpenAI 兼容接口地址和模型名称，按端点要求填写 API Key（无鉴权的本地端点可留空），然后保存。
4. 桌面端不登录也可本地使用；在「设置 → 账号同步」登录后自动在多设备间同步。

### 自动更新

桌面版启动后自动检查一次更新，发现新版本时显示顶部横幅；在「设置 → 软件更新」查看当前版本、新版本和安装包大小，并下载安装。更新说明见对应 Release；预发布版接收预发布更新，正式版仅接收正式版；安装前按 Release 附带的 `SHA256SUMS` 校验。服务端不自动更新，请按部署包内说明升级。

## 服务端与设备接入

- **Linux 服务器**：解压 server 包后按包内 `README.md` 安装。首次启动会在控制台打印一次性初始化码（systemd 下用 `journalctl -u nexterm-server` 查看）；在浏览器输入初始化码并设置超管用户名和密码。访问控制默认开启（`--auth on`），无论监听地址如何均要求登录；公网部署请只监听回环地址，并前置带 TLS 的反向代理。完整选项见 `nexterm-server --help`。
- **懒猫微服**：应用中心安装后直接使用，终端进程和数据保存在微服上；NexTerm 内置端口转发不可用，对外提供远端端口请使用平台转发。
- **设备接入**：接入地址由超管在「设备管理」配置；接入码也在此处（浏览器模式，或桌面端登录服务端账号后）签发，一次性、限时有效。Linux/macOS 用普通用户执行面板的一行命令，Windows 执行 PowerShell 命令，自动下载并安装 nexterm-agent，并配置随用户登录启动（systemd --user / launchd / Windows 计划任务）。设备凭证原文仅保存在设备本地，服务端只存散列；接入后，设备在线时可远程打开终端，吊销后凭证立即失效且不可恢复。

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

## 贡献与提交规范

从最新 `master` 切分支，经 PR 合回；PR 标题和提交消息遵循 Conventional Commits。PR 说明要解决的问题、具体改动和验证方式，用户可感知的变化及行为变化都要写清楚。提交前完成全部质量检查；开发、评审和协作约定见 [AGENTS.md](AGENTS.md)。

## License

[MIT](LICENSE)

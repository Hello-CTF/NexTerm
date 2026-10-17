<!-- The changes section is generated at release time by .github/scripts/release-notes.mjs from conventional commits between the previous and current release tags; this template keeps only the hand-written sections below. -->

### 亮点功能

- **多用户账号**：服务端首次启动在控制台打印一次性初始化码，在浏览器完成超管初始化；注册默认关闭，超管可以在「设置 → 账号同步」的「用户管理」卡里开放注册或直接创建用户；支持登录设备管理，忘记密码可用恢复密钥找回。
- **账号同步**：桌面端「设置 → 账号同步」填服务端地址、用户名和密码登录；资产、分组、片段和凭据在设备之间端到端加密同步，服务端只存密文；known_host 与 AI 模型档案可按开关同步，会话记录（终端录像）默认不同步，可在「终端历史」里对单条打开同步开关。
- **设备管理与分享**：主机安装设备 agent 后进入设备列表（提供一行安装脚本），可远程打开设备终端；主机可分享给其他注册用户，或生成公开链接（默认只读，可勾选允许读写），全程不暴露主机密码与私钥。
- **AI**：接入 OpenAI 兼容接口；可按资产授权 AI「终端写入 / 命令执行」，敏感操作需要确认；可查看 AI 用量，模型档案随账号同步。
- **数据库**：支持连接和管理远端 PostgreSQL、MySQL、Redis，内置 Docker 管理。

### 下载

**桌面客户端**

| 平台 | 文件 |
|---|---|
| Windows 10/11 x64 | `*_x64-setup.exe`（NSIS 安装器，双击即装） |
| Windows 10/11 ARM64 | `*_arm64-setup.exe`（NSIS 安装器，双击即装） |
| macOS（Apple Silicon） | `*_aarch64.dmg`（拖入「应用程序」） |
| macOS（Intel） | `*_x86_64.dmg`（拖入「应用程序」） |
| Linux amd64 | `NexTerm-desktop_*_linux_amd64.deb`（`apt install ./NexTerm-desktop_*_linux_amd64.deb`，自动装 GTK4 / WebKitGTK 6.0 依赖） |
| Linux ARM64 | `NexTerm-desktop_*_linux_arm64.deb`（`apt install ./NexTerm-desktop_*_linux_arm64.deb`，自动装 GTK4 / WebKitGTK 6.0 依赖） |

**移动端**

| 平台 | 文件 |
|---|---|
| Android | `NexTerm_*_android.apk`（arm64 设备直接安装） |
| iOS | `NexTerm_*_ios.ipa`（ad-hoc 签名，TestFlight 需使用 Apple Distribution 签名重新构建） |

**服务端（浏览器访问）**

| 用途 | 文件 |
|---|---|
| LinuxServer（自建机器 / 内网 VPS，含完整浏览器界面） | `NexTerm-server_*_linux_amd64.tar.gz` / `NexTerm-server_*_linux_arm64.tar.gz` |

服务端每个架构只发布一个 full 包，里面有 Go 静态二进制、前端、systemd unit 与 env 示例。部署步骤见 [README 的「服务端与设备接入」](https://github.com/Hello-CTF/NexTerm#服务端与设备接入)。

**设备被控端（agent）**

| 平台 | 文件 |
|---|---|
| Linux amd64 / ARM64 | `NexTerm-agent_*_linux_amd64.tar.gz` / `NexTerm-agent_*_linux_arm64.tar.gz` |
| macOS（Apple Silicon / Intel） | `NexTerm-agent_*_darwin_arm64.tar.gz` / `NexTerm-agent_*_darwin_amd64.tar.gz` |
| Windows 10/11 x64 | `NexTerm-agent_*_windows_amd64.zip` |

tar.gz 内只有 `nexterm-agent`、zip 内只有 `nexterm-agent.exe` 单个可执行文件；主机一键接入用 `install-device.sh`（Linux/macOS）或 `install-device.ps1`（Windows PowerShell），脚本自动下载对应资产并完成设备接入。

> **访问控制默认开启（`--auth on`）**：完整版服务端的 `/rpc`、`/ws` 与 `/files/blob` 在任何监听地址都要求登录账号（`/healthz` 与页面静态资源保持公开），浏览器首次打开会进入初始化或登录页。公网部署请只监听回环地址，前面配置带 TLS 的反向代理并用防火墙限制来源地址；`--auth off` 会关闭账号、管理与设备路由，任何能连到监听地址的人都能操作服务端，不能用于公网。NexTerm 自身数据默认使用 SQLite，也可配置 PostgreSQL、MySQL 或 MariaDB；同一个数据库仅运行一个服务端实例。备份时保留完整数据目录，使用外部数据库时同时备份该数据库。

**懒猫微服**

在微服的应用中心安装，无需从本 Release 下载：构建过程需要访问私有镜像仓库，凭证仅保存在微服侧，因此无法由这条 Release 流水线构建。

### macOS 未签名或未公证构建的首次打开说明

如果安装包未使用 Apple Developer 签名或未经 Apple 公证，首次打开可能被系统拦截（预期行为，不是文件损坏）；已完成签名和公证的构建不适用以下步骤：

1. 双击应用，看到「无法验证开发者 / 无法检查是否包含恶意软件」→ 点「完成」
2. 系统设置 → 隐私与安全性，下拉到「安全性」，点「仍要打开」
3. 之后正常双击即可

如果提示的是「已损坏」而不是「无法验证开发者」，那是早期版本的签名缺陷，请改用最新版本；或通过命令行移除隔离属性：`xattr -dr com.apple.quarantine /Applications/NexTerm.app`

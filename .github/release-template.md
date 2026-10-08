### 亮点功能

- **多用户账号**：服务端首次启动在控制台打印一次性初始化码，在浏览器完成超管初始化；注册默认关闭，超管可以在「设置 → 账号同步」的「用户管理」卡里开放注册或直接创建用户；支持登录设备管理、配对码添加设备，忘记密码可用恢复密钥找回。
- **账号同步**：桌面端「设置 → 账号同步」填服务端地址、用户名和密码登录；资产、分组、片段和凭据在设备之间端到端加密同步，服务端只存密文；known_host 与 AI 模型档案可按开关同步，会话记录（终端录像）默认不同步，可在「终端历史」里对单条打开同步开关。
- **设备管理与分享**：主机安装设备 agent 后进入设备列表（提供一行安装脚本），可远程打开设备终端；主机可分享给其他注册用户，或生成公开链接（默认只读，可勾选允许读写），全程不暴露主机密码与私钥。
- **AI**：接入 OpenAI 兼容接口；可按资产授权 AI「终端写入 / 命令执行」，敏感操作需要确认；可查看 AI 用量，模型档案随账号同步。
- **数据库**：服务端可选 PostgreSQL 后端（`--db postgres`，当前只支持单写入实例）；MySQL、Redis 与 Docker 管理继续内置。

### 下载

**桌面客户端**

| 平台 | 文件 |
|---|---|
| Windows 10/11 x64 | `*_x64-setup.exe`（NSIS 安装器，双击即装） |
| Windows 10/11 ARM64 | `*_arm64-setup.exe`（NSIS 安装器，双击即装） |
| macOS（Apple Silicon） | `*_aarch64.dmg`（拖入「应用程序」） |
| macOS（Intel） | `*_x86_64.dmg`（拖入「应用程序」） |
| Linux amd64 | `NexTerm-desktop_*_linux_amd64.tar.gz`（自包含 Wails 二进制，非 AppImage/deb） |
| Linux ARM64 | `NexTerm-desktop_*_linux_arm64.tar.gz`（自包含 Wails 二进制，非 AppImage/deb） |

**服务端（浏览器访问）**

| 用途 | 文件 |
|---|---|
| LinuxServer（自建机器 / 内网 VPS，含完整浏览器界面与可选 sync-only 模式） | `NexTerm-server_*_linux_amd64.tar.gz` / `NexTerm-server_*_linux_arm64.tar.gz` |

服务端每个架构只发布一个 full 包，里面有 Go 静态二进制、前端、`nexterm-server.service` 与 `nexterm-onlyserver.service` 两种 unit，以及对应的 env 示例。只做资产同步时，安装后一种 unit，让同一二进制以 `--sync-only` 运行；没有独立的 onlyServer 下载包。两个 unit 不要同时启用。部署步骤见 [README 的「服务端与懒猫微服」](https://github.com/ProbiusOfficial/NexTerm#服务端与懒猫微服)。

> **访问控制默认开启（`--auth on`）**：完整版服务端的 `/rpc`、`/ws` 与 `/files/blob` 在任何监听地址都要求登录账号（`/healthz` 与页面静态资源保持公开），浏览器首次打开会进入初始化或登录页。`--sync-only` 模式只开放账号、超管与同步路由（`/auth/*`、`/admin/*`、`/sync/v2/*`）和 `/healthz`；其中 `/healthz` 无需登录即可访问，`/auth/status`、`/auth/init`、`/auth/login`、`/auth/register`、`/auth/recovery/reset`、`/auth/devices/enroll` 是匿名可用的公共端点，其余都要求登录会话（`/admin/*` 要求超管身份）。公网部署请只监听回环地址，前面配置带 TLS 的反向代理并用防火墙限制来源地址；`--auth off` 会关闭账号、管理与设备路由，任何能连到监听地址的人都能操作服务端，不能用于公网。使用默认 SQLite 后端时，备份数据目录即备份全部工作区数据与密文同步内容；`--db postgres` 部署的工作区与同步数据存放在 PostgreSQL 中，必须另行备份数据库。

**懒猫微服**

在微服的应用中心里安装，不经这里下载：构建要连私有镜像仓库，凭证只在微服侧，出不了这条流水线。

### macOS 首次打开会被拦一次（预期行为，不是文件损坏）

安装包是 ad-hoc 签名、未公证的（没有 Apple Developer 证书）：

1. 双击应用，看到「无法验证开发者 / 无法检查是否包含恶意软件」→ 点「完成」
2. 系统设置 → 隐私与安全性，下拉到「安全性」，点「仍要打开」
3. 之后正常双击即可

如果提示的是「已损坏」而不是「无法验证开发者」，那是早期版本的签名缺陷，请改用最新版本；或用命令行修掉：`xattr -dr com.apple.quarantine /Applications/NexTerm.app`

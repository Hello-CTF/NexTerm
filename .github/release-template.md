### v1.0.9 变化

- **iOS/Android 真机可用**：修复 iOS 27 启动闪退（UIScene 生命周期适配）与 iOS/Android 启动秒退（移动端数据目录）；新增 FaceID 解锁凭据库（材料存 Keychain，先验证后读取）；Android 沉浸式边到边布局、iOS 全屏 WebView；移动端系统交互适配（长按菜单、底部动作面板、边缘手势开侧栏、横屏全屏终端、safe-area、终端选中文本动作条）；会话 cookie 与绝对 TTL 对齐，App 杀进程不再掉线。
- **设备纳入资产树**：设备分区显示在资产树最前，单击/双击/Enter 直接打开远程终端；指标在设备行内展开（CPU 24h 趋势图 + CPU/内存/磁盘/运行时长）；吊销为红色二次确认，吊销后设备即从资产中移除。macOS 设备 agent 新增指标上报（CPU/内存/磁盘/运行时长，纯 Go 无新增依赖）。
- **设备接入简化**：移除批准/拒绝流程，接入码接入即上线；设备管理只保留吊销一个回收手段。
- **断连重连一次完成**：重启应用或会话掉线后，重连从原来的两次点击并为一次；原终端在服务端已不存在时自动打开新终端并提示。
- **CI 加速**：per-arch Go 构建缓存、Android NDK/Gradle 缓存、文档类变更跳过全量 CI 等，master CI 墙钟明显缩短。

### 已知缺口（如实说明）

- FaceID 材料未加 SecAccessControl 生物识别约束（v1 靠"先验证后读取"编排保护，换新面容后旧材料仍有效），加固另开 PR。
- Android 冷启动需重新输入主密码（DEK 仅存 sessionStorage，Android 暂无生物识别兜底，计划 Android Keystore 方案）。
- Android 上 durable（守护）会话的 supervisor socket 落在 TempDir，预计不可用；自更新、本地终端 shell 集成、Termius 导入在 Android 同样受 TempDir 限制。
- 屏幕旋转后系统栏 insets 不刷新（wails Screen 缓存仅启动时刷新），横屏刘海侧可能出现避让偏差。
- iPad 桌面模式（UA 为 Macintosh）拿不到移动布局与 insets 注入，粗指针媒体查询兜底。
- iOS 打包的 wails3 生成工程不接资源编译（图标/启动屏），本地产物经手动 actool/ibtool 注入；CI 的 ios:package:ipa 路径不受影响。
- 真实浏览器与真机的自动化验收仍未覆盖（真实拖拽、移动端手势、真机安装），上述移动端改动经真机人工验证（iPhone 17 Pro / Pixel 4a 5G）。

### 亮点功能

- **多用户账号**：服务端首次启动在控制台打印一次性初始化码，在浏览器完成超管初始化；注册默认关闭，超管可以在「设置 → 账号同步」的「用户管理」卡里开放注册或直接创建用户；支持登录设备管理，忘记密码可用恢复密钥找回。
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
| Linux amd64 | `NexTerm-desktop_*_linux_amd64.deb`（`apt install ./NexTerm-desktop_*_linux_amd64.deb`，自动装 GTK4 / WebKitGTK 6.0 依赖） |
| Linux ARM64 | `NexTerm-desktop_*_linux_arm64.deb`（`apt install ./NexTerm-desktop_*_linux_arm64.deb`，自动装 GTK4 / WebKitGTK 6.0 依赖） |

**移动端**

| 平台 | 文件 |
|---|---|
| Android | `NexTerm_*_android.apk`（ arm64 设备直接安装） |
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

> **访问控制默认开启（`--auth on`）**：完整版服务端的 `/rpc`、`/ws` 与 `/files/blob` 在任何监听地址都要求登录账号（`/healthz` 与页面静态资源保持公开），浏览器首次打开会进入初始化或登录页。公网部署请只监听回环地址，前面配置带 TLS 的反向代理并用防火墙限制来源地址；`--auth off` 会关闭账号、管理与设备路由，任何能连到监听地址的人都能操作服务端，不能用于公网。使用默认 SQLite 后端时，备份数据目录即备份全部工作区数据与密文同步内容；`--db postgres` 部署的工作区与同步数据存放在 PostgreSQL 中，必须另行备份数据库。

**懒猫微服**

在微服的应用中心里安装，不经这里下载：构建要连私有镜像仓库，凭证只在微服侧，出不了这条流水线。

### macOS 未签名或未公证构建的首次打开说明

如果安装包未使用 Apple Developer 签名或未经 Apple 公证，首次打开可能被系统拦截（预期行为，不是文件损坏）；已完成签名和公证的构建不适用以下步骤：

1. 双击应用，看到「无法验证开发者 / 无法检查是否包含恶意软件」→ 点「完成」
2. 系统设置 → 隐私与安全性，下拉到「安全性」，点「仍要打开」
3. 之后正常双击即可

如果提示的是「已损坏」而不是「无法验证开发者」，那是早期版本的签名缺陷，请改用最新版本；或用命令行修掉：`xattr -dr com.apple.quarantine /Applications/NexTerm.app`

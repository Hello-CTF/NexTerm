<!-- The changes section is generated at release time by .github/scripts/release-notes.mjs from conventional commits between the previous and current release tags; this template keeps only the hand-written sections below. -->

### 下载

**桌面客户端**

| 平台 | 文件 |
|---|---|
| Windows 10/11 x64 | `nexterm-desktop_windows_amd64_*.exe`（NSIS 安装器，双击即装） |
| Windows 10/11 ARM64 | `nexterm-desktop_windows_arm64_*.exe`（NSIS 安装器，双击即装） |
| macOS（Apple Silicon） | `nexterm-desktop_macos_arm64_*.dmg`（拖入「应用程序」） |
| macOS（Intel） | `nexterm-desktop_macos_amd64_*.dmg`（拖入「应用程序」） |
| Linux amd64 | `nexterm-desktop_linux_amd64_*.deb`（`apt install ./nexterm-desktop_linux_amd64_*.deb`，自动装 GTK4 / WebKitGTK 6.0 依赖） |
| Linux ARM64 | `nexterm-desktop_linux_arm64_*.deb`（`apt install ./nexterm-desktop_linux_arm64_*.deb`，自动装 GTK4 / WebKitGTK 6.0 依赖） |

Linux 桌面端从 1.2.x 升级到 1.3.x 及以后：1.2.x 的应用内更新无法安装新增的 `ai-runtime` 运行时，请先手动重装一次 deb（`apt install ./nexterm-desktop_linux_*.deb`），此后应用内更新可正常使用。

Windows 安装器不含 Authenticode 签名，首次运行可能显示未知发布者；应用内更新仍使用 Minisign 校验。

**移动端**

| 平台 | 获取方式 |
|---|---|
| Android | `nexterm-desktop_android_arm64_*.apk`（arm64 设备直接安装） |
| iOS / iPadOS | 使用 NexTerm 的 TestFlight 测试邀请安装；本 Release 不提供 IPA 资产。 |

**服务端（浏览器访问）**

| 用途 | 文件 |
|---|---|
| LinuxServer（自建机器 / 内网 VPS，含完整浏览器界面） | `nexterm-server_linux_amd64_*.tar.gz` / `nexterm-server_linux_arm64_*.tar.gz` |

服务端每个架构只发布一个 full 包，里面有 Go 服务端二进制（动态链接 glibc，无最低版本要求，不支持 musl/Alpine）、前端、systemd unit、env 示例与 `install.sh`。一键安装或升级：`curl -fsSL https://nexterm.work/install.sh | sudo bash`，详见 [README 的「部署服务端」](https://github.com/Hello-CTF/NexTerm#部署服务端)。

**设备被控端（agent）**

| 平台 | 文件 |
|---|---|
| Linux amd64 / ARM64 | `nexterm-agent_linux_amd64_*.tar.gz` / `nexterm-agent_linux_arm64_*.tar.gz` |
| macOS（Apple Silicon / Intel） | `nexterm-agent_macos_arm64_*.tar.gz` / `nexterm-agent_macos_amd64_*.tar.gz` |
| Windows 10/11 x64 | `nexterm-agent_windows_amd64_*.zip` |

tar.gz 内只有 `nexterm-agent`、zip 内只有 `nexterm-agent.exe` 单个可执行文件；主机一键接入用 `install-device.sh`（Linux/macOS）或 `install-device.ps1`（Windows PowerShell），脚本自动下载对应资产并完成设备接入。已接入且版本 >= 1.3.0 的 agent 可在设备上执行 `nexterm-agent update`（别名 `upgrade`；`--check` 只检查，`--yes` 跳过确认，`--force` 允许重装或降级）从官方 release 升级；1.2.x 的 agent 没有该命令，需再执行一次 install-device 脚本。

服务端默认要求登录且只监听回环地址；`--auth off` 会关闭账号与访问控制，不能用于公网。公网部署、备份与数据库选项见 [README 的「部署服务端」](https://github.com/Hello-CTF/NexTerm#部署服务端)。

**懒猫微服**

在微服的应用中心安装，无需从本 Release 下载：构建过程需要访问私有镜像仓库，凭证仅保存在微服侧，因此无法由这条 Release 流水线构建。

macOS 首次打开被系统拦截时的处理方法见 [README 的「下载」](https://github.com/Hello-CTF/NexTerm#下载)。

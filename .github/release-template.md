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
| **LinuxServer** —— 自建机器 / 内网 VPS，含完整浏览器界面与可选 restricted 运行时 | `NexTerm-server_*_linux_amd64.tar.gz` / `NexTerm-server_*_linux_arm64.tar.gz` |

服务端每个架构只发布**一个 full 包**，里面有 Go 静态二进制、前端、`nexterm-server.service` 与
`nexterm-onlyserver.service` 两种 unit，以及对应的 env 示例。只做资产同步时，安装后一种
unit，让同一二进制以 `--sync-only` 运行；它不是独立的 onlyServer 下载包。两个 unit 不要同时启用。
部署步骤见 [README 的「部署服务端」](https://github.com/ProbiusOfficial/NexTerm#部署服务端)。

> ⛔ 完整版服务端**自身没有登录鉴权**，不建议直接暴露公网；公网请使用包内可选的 `--sync-only` restricted 运行时并终止 TLS。

**懒猫微服**

在微服的**应用中心**里安装，不经这里下载 —— 构建它要连私有镜像仓库（凭证只在微服侧），
出不了这条流水线。

### macOS 首次打开会被拦一次（预期行为，不是文件损坏）

安装包是 **ad-hoc 签名、未公证**的（没有 Apple Developer 证书）：

1. 双击应用，看到「无法验证开发者 / 无法检查是否包含恶意软件」→ 点**完成**
2. **系统设置 → 隐私与安全性**，下拉到「安全性」，点 **「仍要打开」**
3. 之后正常双击即可

如果提示的是**「已损坏」**而不是「无法验证开发者」，那是早期版本的签名缺陷，
请改用最新版本；或用命令行修掉：`xattr -dr com.apple.quarantine /Applications/NexTerm.app`

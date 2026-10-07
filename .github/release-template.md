### 下载

**桌面客户端**

| 平台 | 文件 |
|---|---|
| Windows 10/11 x64 | `*_x64-setup.exe`（NSIS 安装器，双击即装） |
| macOS（Apple Silicon） | `*_aarch64.dmg`（拖入「应用程序」） |

**服务端（浏览器访问）**

| 用途 | 文件 |
|---|---|
| **LinuxServer** —— 自建机器 / 内网 VPS，**带完整浏览器界面** | `NexTerm-*-linux-amd64.tar.gz` |
| **onlyServer** —— 公网只做资产同步中转，无界面、命令行管理 | `NexTerm-onlyServer-*-linux-amd64.tar.gz` |

两者是**同一个二进制**，只差一个 `--sync-only` —— 分成两个包是为了让你拿到手就是对的形态。
解压后有二进制、前端产物、systemd 单元与一份 `README.md` / `.env.example`。
部署步骤见 [README 的「部署服务端」](https://github.com/ProbiusOfficial/NexTerm#部署服务端)。

> ⛔ 完整版服务端**自身没有登录鉴权**，不建议直接暴露公网；公网请用 onlyServer。

**懒猫微服**

在微服的**应用中心**里安装。本 Release 同时也附了一份可手动安装的 `.lpk` ——
它出不了这条流水线（构建要连私有镜像仓库，凭证只在微服侧），是维护者本机出的。

### macOS 首次打开会被拦一次（预期行为，不是文件损坏）

安装包是 **ad-hoc 签名、未公证**的（没有 Apple Developer 证书）：

1. 双击应用，看到「无法验证开发者 / 无法检查是否包含恶意软件」→ 点**完成**
2. **系统设置 → 隐私与安全性**，下拉到「安全性」，点 **「仍要打开」**
3. 之后正常双击即可

如果提示的是**「已损坏」**而不是「无法验证开发者」，那是早期版本的签名缺陷，
请改用最新版本；或用命令行修掉：`xattr -dr com.apple.quarantine /Applications/NexTerm.app`

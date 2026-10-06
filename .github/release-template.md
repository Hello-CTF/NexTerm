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
部署步骤见 [README 的「服务端与懒猫微服」](https://github.com/ProbiusOfficial/NexTerm#服务端与懒猫微服)。

> ⛔ 访问控制默认开启（`--auth on`，rc4 起）：完整版服务端的 `/rpc`、`/ws` 与 `/files/blob` 在任何监听地址都要求同步令牌（`/healthz` 与页面静态资源保持公开），浏览器首次打开会提示输入令牌。`--sync-only` restricted 运行时的 `/sync/rpc` 同样始终要求同步令牌，令牌泄漏的影响限于资产库；公网请终止 TLS 并用防火墙限制来源地址。`--auth off` 不是公网方案：关闭后任何能连到监听地址的人都能操作服务端，启动日志只会给出一条警告。

**从 rc2 及更早版本升级（访问控制默认变化）**

- 包内 `nexterm-server.service` 与 `nexterm-onlyserver.service` 都已显式 `--auth on`：替换二进制后按实际启用的 unit 执行 `systemctl restart nexterm-server` 或 `systemctl restart nexterm-onlyserver` 即可（两个 unit 不要同时启用）；升级后浏览器首次打开会提示输入同步令牌，这是预期行为，不是故障。手动跑二进制的部署请注意默认值已翻转为 `--auth on`（此前仅非回环监听强制），监听回环地址的现有部署升级后同样要输令牌。
- 取回或轮换同步令牌：`nexterm-server token --data-dir /var/lib/nexterm` 打印当前令牌，`nexterm-server rotate-token --data-dir /var/lib/nexterm` 轮换（旧令牌立即失效，桌面端需同步更新）。令牌只应出现在你自己的终端里，不要贴进 issue 或聊天记录。
- 回环豁免仅限显式声明：`--auth loopback`（或 `NEXTERM_AUTH=loopback`）只适合本机直接访问，该模式把 Host 限定为 `localhost`、`127.0.0.1`、`[::1]`（防 DNS 重绑定），经反向代理用域名访问会收到 421；反向代理部署请保持默认 `--auth on`。
- 公网部署：只监听回环地址，前面套 TLS 反向代理；`/rpc`、`/ws`、`/files/blob` 与 `/sync/rpc` 都要同步令牌。同步令牌以明文保存在数据目录的数据库中，备份数据目录等于备份令牌，请像保管密钥一样保管备份。

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

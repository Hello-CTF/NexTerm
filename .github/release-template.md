### 下载

| 平台 | 文件 |
|---|---|
| Windows 10/11 x64 | `*_x64-setup.exe`（NSIS 安装器） |
| macOS（Apple Silicon） | `*_aarch64.dmg` |

### macOS 首次打开会被拦一次（预期行为，不是文件损坏）

安装包是 **ad-hoc 签名、未公证**的（没有 Apple Developer 证书）：

1. 双击应用，看到「无法验证开发者 / 无法检查是否包含恶意软件」→ 点**完成**
2. **系统设置 → 隐私与安全性**，下拉到「安全性」，点 **「仍要打开」**
3. 之后正常双击即可

如果提示的是**「已损坏」**而不是「无法验证开发者」，那是早期版本的签名缺陷，
请改用最新版本；或用命令行修掉：`xattr -dr com.apple.quarantine /Applications/NexTerm.app`

# 应用内更新

桌面版启动后会静默检查一次新版本，有则在顶部挂一条横幅；设置页里也有常驻的
「软件更新」卡片。点「立即更新」→ 下载 → 验签 → 安装 → 提示重启。

```
检查 ──→ 提示（横幅 + 设置卡片）
          │
          └─→ 下载（进度走 update://progress 事件）
                └─→ 验签（minisign，公钥在 tauri.conf.json）
                      └─→ 安装（Windows: NSIS 静默安装器 / macOS: 替换 .app）
                            └─→ 重启（macOS / Linux 必须，Windows 安装器自己会拉起）
```

## ⚠️ 发行方必读：密钥没配 = 功能不可用（但不影响发版）

**仓库里放的是一枚占位公钥**（`src-tauri/tauri.conf.json` 的
`plugins.updater.pubkey`，值为 `REPLACE_ME_RUN_tauri_signer_generate`）。
这不是疏漏 —— 签名私钥属于发行方，不可能随代码走。

在这种状态下：

- 应用照常构建、照常发版；
- 「软件更新」如实显示**「更新服务未配置」**（不会报一串英文验签错误，
  见 `src-tauri/src/update.rs` 的模块文档）；
- 顶部横幅**不出现** —— 因为它上面的按钮点了必然失败。

### 一次性配置（三条命令）

```bash
# ① 生成密钥对（私钥务必存好，丢了就再也签不出能被老版本接受的更新包）
pnpm tauri signer generate -w ~/.tauri/nexterm.key

# ② 把打印出来的**公钥**填进两处（必须一致）
#    · src-tauri/tauri.conf.json  → plugins.updater.pubkey
#    · src-tauri/src/update.rs    → PLACEHOLDER_PUBKEY 常量
#    （后者是「还没配」的哨兵；不改它就永远走降级分支）

# ③ 把**私钥内容**与密码存成仓库 Secret
#    TAURI_SIGNING_PRIVATE_KEY           ← ~/.tauri/nexterm.key 的内容
#    TAURI_SIGNING_PRIVATE_KEY_PASSWORD  ← 第 ① 步设的密码
gh secret set TAURI_SIGNING_PRIVATE_KEY < ~/.tauri/nexterm.key
gh secret set TAURI_SIGNING_PRIVATE_KEY_PASSWORD
```

配好之后推一个 `v*` tag，Release 上会多出 `.sig` 文件与 **`latest.json`** ——
应用检查更新读的就是它。

> `latest.json` 由 `tauri-action` 汇总矩阵里两个平台（macOS / Windows）的 `.sig` 生成。
> 没配密钥时 `.github/workflows/release.yml` 会自动把 `createUpdaterArtifacts`
> 关掉，否则缺私钥会让 `tauri build` **直接失败**、整个发版断掉。

### 更新源

`plugins.updater.endpoints` 指向：

```
https://github.com/ProbiusOfficial/NexTerm/releases/latest/download/latest.json
```

`releases/latest` 天然**跳过预发布**（`v0.2.2-rc.7` 这类不会被推送出去），
也正是我们要的行为。若改了仓库归属，这一行要跟着改 —— 它是**编译期写死**的
（`tauri.conf.json` 在构建时被嵌进二进制）。

## 各平台行为差异

当前发版只出 Windows 与 macOS 两个桌面包（见 `.github/workflows/release.yml` 的矩阵），
所以只有这两行是实际路径：

| 平台 | 安装方式 | 是否需要手动重启 |
|---|---|---|
| Windows | NSIS 静默安装器（`installMode: passive`） | **不需要** —— 安装器自己拉起新进程，当前进程被杀 |
| macOS | 解开 `.app.tar.gz` 替换磁盘上的包 | **需要**，界面会弹「现在重启？」 |

（Tauri updater 在 Linux 上走 AppImage 替换。本项目目前不出桌面 Linux 包，
所以那条路径没有对应的产物。）

正因为 Windows 那条路会让「安装」这个 IPC 调用**永远不返回**（进程没了），
`installUpdate()` 里成功之后的那段代码实际只在 macOS / Linux 上跑 ——
这是预期行为，不是卡住。

## 已知限制

- **只有桌面版能装**。服务端（容器 / 浏览器访问）没有「用新包替换自己」这回事，
  `canInstall` 恒为 `false`，升级方式是换部署包。目前服务端**连检查都不做**
  （没有引入 updater 插件）；要做的话得自己走 GitHub Releases API 比版本号，
  那会多出第二份「最新版是多少」的判据，暂不做。
- **不推送预发布版本**。想收 rc 得改 endpoint（那会同时收下所有预发布）。
- **macOS 包是 ad-hoc 签名、未公证**的（没有 Apple Developer 证书），首次安装
  仍会被 Gatekeeper 拦一次，见 README「macOS 首次打开会被拦一次」。
  更新走的是应用内替换，不经过浏览器下载，所以不会有 quarantine 属性 ——
  但**如果**之后换成手动下载安装，那一步照样会被拦。
- **依赖树里会多一份 `reqwest`**（updater 插件自带 0.12 线，本项目钉的是 0.13）。
  见 `src-tauri/Cargo.toml` 里那段注释：换来的是不自己实现「NSIS 静默安装 +
  macOS 包替换 + 签名校验」——那三件事任何一件写错，用户拿到的就是一个装坏的应用。
- **下载过程没有断点续传**。网络断掉只能重来（更新包通常二三十兆，可以接受）。

# Go 线 · 懒猫 dev 验收（v0.2.2-rc.2）

被测：`rwig` @ `b4df788`（v0.2.2-rc.2）+ 我方两条本地提交（dev 隔离、Origin 修复 + tmux）。
实例：`cloud.lazycat.app.nextermgo.dev` → https://nextermgo.lazycore.heiyu.space

## 截图

| 文件 | 说明 |
|---|---|
| `01-app-rendered-rc2.png` | 首页完整渲染。底部状态栏显示「凭据库已解锁」⇒ 平台注入的 master key 生效 |
| `02-terminal-attached-rc2.png` | 点「打开本地终端」后：`终端 1 · 已连接`，提示符 `#`（容器内 root）。rc.2 新增的编码切换 / 录制 / 命令块入口可见 |

上一轮（rc.1）的取证在同级目录 `../acceptance-lazycat-go/`，含白屏前后对照。

## 本轮关键判据

| 项 | 结果 |
|---|---|
| `/`、`/assets/*.js`、`/assets/*.css` 带公网 Origin | **200**（修前是 403 → 白屏） |
| 带 `Origin: https://evil.example` | **403**（守卫未被削弱） |
| 容器内直连 `/rpc`：无鉴权头 / 正确头 / 错误头 | **401 / 200 / 401** ⇒ rc.2 新增的应用级鉴权确实生效 |
| 真浏览器 | `#root` 有内容（18,271 B HTML）、**零失败请求、零异常** |
| 终端 | xterm 已渲染，**两条 WS 均 101**，`1 个会话 / 1 已连接` |
| 容器内 `tmux -V` | **3.3a** |
| `NEXTERM_ALLOWED_ORIGINS` | 渲染为 `https://nextermgo.lazycore.heiyu.space` ⇒ `.S.AppDomain` 生效 |

## 部署时撞到的阻断问题（与代码无关）

rc.2 就地修改了已发布的 `migrations/0001|0004|0005.sql`（只删注释），
导致 rc.1 建好的库 checksum 不匹配 ⇒ 容器起不来（`Status_Error`）。

处置：旧库数据几乎为空 ⇒ 备份为 `/lzcapp/var/nexterm-rc1-backup`，让应用重新初始化。
完整分析见 `../UPSTREAM-RWIG-ISSUE-migration-checksum.md`。

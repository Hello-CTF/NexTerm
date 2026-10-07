# [阻断级] rc.2 就地修改了已发布的 migration ⇒ 存量部署升级后无法启动

> 直接可粘贴的 issue 正文。实测日期 2026-10-05 ｜ 被测版本 `rwig` @ `b4df788`（v0.2.2-rc.2）
> 对照版本 `ce84b62`（v0.2.2-rc.1）｜ Go 1.26.8 ｜ 部署形态：懒猫微服 LPK v2

---

## 现象

从 rc.1 升到 rc.2 后，服务端起不来：

```
nexterm-server: 数据库迁移错误: migration 1 (init) does not match its applied checksum
```

（懒猫平台上表现为 `Instance status: Status_Error`，且平台侧 `Error message: (empty)`，日志要靠容器侧才看得到。）

## 影响面（这是阻断级，不是边角问题）

**任何已经应用过这些迁移的数据库**都会拒绝启动 —— 也就是：

- 所有 rc.1 / 更早版本的存量部署，升级 rc.2 直接起不来；
- 商店里已安装的用户同样中招；
- **与部署形态无关**：桌面版、自建服务端、容器化部署一并受影响，不是懒猫特有。

## 根因

`internal/store/migrate.go` 把「迁移文件字节的 sha256」当作不可变契约：

```go
contents, err := migrations.Files.ReadFile(name)
checksum := sha256.Sum256(contents)          // ← 文件字节的 hash
```

启动时逐条比对（`migrateOnce`）：

```go
if exists {
    if !bytes.Equal(checksum, m.checksum) {
        return migrateError(fmt.Errorf("migration %d (%s) does not match its applied checksum", m.version, m.description))
    }
}
```

而 rc.2 这一轮**仓库级去注释清理误伤了 `migrations/`**：

| 文件 | 改动 | 剥掉注释 + 行尾空白后的 sha256 |
|---|---|---|
| `migrations/0001_init.sql` | 删掉行内 `--` 注释（43 行） | **与 rc.1 完全相同** |
| `migrations/0002_asset_builtin.sql` | 同上 | 相同 |
| `migrations/0004_outcome.sql` | 同上（27 行） | 相同 |
| `migrations/0005_cron.sql` | 同上（39 行） | 相同 |
| `migrations/0006_message_seq.sql` | 同上 | 相同 |
| `0007`~`0011` | **新增**（5 个）| 新增是正确的 ✅ |

⇒ **语义零变化，但文件字节变了 ⇒ checksum 变了 ⇒ 拒绝启动。**

（`0007`~`0011` 是新增，不受影响；出问题的只有 0001 / 0004 / 0005 这三个「已发布且被改写」的。）

## 最小复现（不需要真部署，本地两条命令）

```bash
# ① 用 rc.1 的二进制建库
NEXTERM_DATA_DIR=/tmp/rc1-data NEXTERM_LISTEN=127.0.0.1:18081 <rc1-nexterm-server> &
sleep 4; kill %1

# ② 用 rc.2 的二进制打开同一个数据目录
NEXTERM_DATA_DIR=/tmp/rc1-data NEXTERM_LISTEN=127.0.0.1:18082 <rc2-nexterm-server> &
sleep 4; kill %1
# → nexterm-server: 数据库迁移错误: migration 1 (init) does not match its applied checksum
```

**为什么本地开发/CI 一直没发现**：所有测试都从**空目录**建库（`t.TempDir()`），
checksum 是当场写进去的，永远不会不匹配。**只有「旧库 + 新二进制」才复现** ——
而 CI 里没有这个组合。

## 关键结论：数据是可救的

我们验证过 —— 障碍**只有 checksum 这一项**：

```
把 schema_migrations 里 v1..v6 的 checksum 更新为 rc.2 的值
  → 迁移错误消失，服务端一路启动到 vault 阶段（只在主密码不对时报错）
```

交叉验证过 checksum 算法：rc.2 空库写入的 `schema_migrations.checksum`
与 `shasum -a 256 migrations/XXXX.sql` **逐字节一致**。

也就是说：**没有真的 schema 漂移，纯粹是「文件字节被改动」触发的自锁。**

## 修复建议

**A. 推荐：把已发布 migration 的字节恢复原样。**
把 `0001_init.sql` / `0004_outcome.sql` / `0005_cron.sql` 的行内注释加回来（其余文件不用动），
让 rc.2 与 rc.1 的这三个文件逐字节相同。去注释清理只应作用于**从未发布过**的新文件。

**B. 长期：给 migrations 加一条「已发布即冻结」的 CI 检查。**
例如在 CI 里对 `git diff <上一个 tag>..HEAD -- migrations/` 做校验：
只允许**新增**文件，已存在文件的任何改动（包括注释、空白）都直接打红。
判据可以很简单 —— `git diff --name-status` 里出现 `M` 就是违规。

**C. 不要用「忽略 checksum 不一致」来兜。**
那会把「有人真的改了迁移语义」也一起放过，风险更大。

## 附：为什么建议走 A 而不是让我们改数据

我们这次是 dev 环境、数据几乎为空（`asset` 1 行、`credential` 0 行），清库重来代价为零。
但**真实用户的数据是清不掉的** —— 他们的凭据库、资产树、会话记录都在那个库里。
只要 rc.2 以现在的形态发出去，这些人升级后就只能面对一个起不来的应用。

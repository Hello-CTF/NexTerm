//! 仓储函数：资产 / 分组 / 凭据 / 设置 / 审计 / AI 会话 / 片段 / 已知主机 / 录制。

use crate::error::{AppError, AppResult};
use crate::ids::now_ms;
use crate::store::models::*;

const KEEPALIVE: &str = "keepalive";

/// 内置「当前设备」资产的**固定 ID**。
///
/// 为什么不用 `new_id()` 现场生成：这个 ID 是「本机」这个概念的身份 ——
/// 会话复用（同一资产只连一条）、审计归属（`audit_log.asset_id`）、
/// 前端判定「是不是本机」全都认它。每次启动换一个 ID，就会攒下一串
/// 同名资产和一堆彼此独立的工作区。
///
/// 形态必须满足 `store::ensure_id`（26 位 ASCII 字母数字），
/// 否则任何拿它当参数走的路径都会被参数校验拒掉 —— 有单测守着。
pub const BUILTIN_LOCAL_ASSET_ID: &str = "01J0NEXTERMLOCALDEVICE0001";

/// 内置资产的显示名。用户可改名（改名只影响显示），但默认叫这个。
pub const BUILTIN_LOCAL_ASSET_NAME: &str = "当前设备";

// ───────────────────────── setting ─────────────────────────

impl super::Store {
    pub async fn setting_get(&self, key: &str) -> AppResult<Option<String>> {
        let row: Option<(String,)> = sqlx::query_as("SELECT value FROM setting WHERE key = ?")
            .bind(key)
            .fetch_optional(self.pool())
            .await?;
        Ok(row.map(|(v,)| v))
    }

    pub async fn setting_set(&self, key: &str, value: &str) -> AppResult<()> {
        sqlx::query(
            "INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
             ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at",
        )
        .bind(key)
        .bind(value)
        .bind(now_ms() as i64)
        .execute(self.pool())
        .await?;
        Ok(())
    }

    pub async fn setting_delete(&self, key: &str) -> AppResult<()> {
        sqlx::query("DELETE FROM setting WHERE key = ?")
            .bind(key)
            .execute(self.pool())
            .await?;
        Ok(())
    }

    // ── 工作区布局（服务端权威运行态的一部分）────────────────────────

    /// 布局在 `setting` 表里的键。
    ///
    /// 复用 `setting` 而不是新开一张表：布局是**单行、整份替换**的数据，
    /// 天然就是「一个键一个值」。为它建表意味着以后每次调整布局结构都要写一次
    /// schema 迁移，而布局是前端的视图模型、改得比内核勤得多。
    pub const LAYOUT_KEY: &str = "layout.workspace";

    /// 读布局：`(revision, updated_at, 正文 JSON 文本)`。没存过时 revision = 0。
    pub async fn layout_load(&self) -> AppResult<(i64, i64, Option<String>)> {
        let row: Option<(String, i64)> =
            sqlx::query_as("SELECT value, updated_at FROM setting WHERE key = ?")
                .bind(Self::LAYOUT_KEY)
                .fetch_optional(self.pool())
                .await?;
        let Some((value, updated_at)) = row else {
            return Ok((0, 0, None));
        };
        // 信封解不开（旧版本写下的 / 被手工改坏）时当作「还没有布局」，
        // **不报错**：一份坏掉的布局不该让整个界面起不来，重新存一次即可。
        let Ok(v) = serde_json::from_str::<serde_json::Value>(&value) else {
            return Ok((0, updated_at, None));
        };
        let revision = v.get("revision").and_then(|x| x.as_i64()).unwrap_or(0);
        let data = v
            .get("data")
            .filter(|d| !d.is_null())
            .map(|d| d.to_string());
        Ok((revision, updated_at, data))
    }

    /// 条件写入布局（乐观锁）。返回 `(是否写入, 写入后的 revision)`。
    ///
    /// # 为什么不能 `setting_get` + `setting_set` 两步走
    ///
    /// 两台设备同时拖布局时，两边都读到 `revision=5`、两边都写成功 ⇒ **后写的
    /// 把先写的整份布局覆盖掉**，而先写的那台还以为自己保存成功了。布局是「整份
    /// 替换」的，一次覆盖就是丢掉对方所有的窗口与标签，伤得很重。
    ///
    /// 这里用一个事务把「比 revision」和「写入」合起来。SQLite 的快照隔离会让
    /// 后写的那个事务拿不到写锁（返回 `db` 错误），**而不是静默丢数据** ——
    /// 界面拉一次最新再重试即可。
    ///
    /// ⚠️ 刻意不做「自动合并」：布局没有可合并的语义（对端删掉的标签该不该复活？
    /// 两个窗口的尺寸听谁的？），强行合并只会产出一份谁也看不懂的布局。
    pub async fn layout_save(&self, expected: i64, data_json: &str) -> AppResult<(bool, i64)> {
        let data: serde_json::Value = serde_json::from_str(data_json)
            .map_err(|e| AppError::param(format!("布局不是合法 JSON: {e}")))?;

        let mut tx = self.pool().begin().await?;
        let cur: Option<(String,)> = sqlx::query_as("SELECT value FROM setting WHERE key = ?")
            .bind(Self::LAYOUT_KEY)
            .fetch_optional(&mut *tx)
            .await?;
        let cur_rev = cur
            .as_ref()
            .and_then(|(v,)| serde_json::from_str::<serde_json::Value>(v).ok())
            .and_then(|v| v.get("revision").and_then(|x| x.as_i64()))
            .unwrap_or(0);
        if cur_rev != expected {
            // 让 Transaction 自然 drop（= 回滚），别把冲突写进去。
            return Ok((false, cur_rev));
        }
        let next = cur_rev + 1;
        let value = serde_json::json!({
            "revision": next,
            "updatedAt": now_ms() as i64,
            "data": data,
        })
        .to_string();
        sqlx::query(
            "INSERT INTO setting(key, value, updated_at) VALUES(?,?,?)
             ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at",
        )
        .bind(Self::LAYOUT_KEY)
        .bind(&value)
        .bind(now_ms() as i64)
        .execute(&mut *tx)
        .await?;
        tx.commit().await?;
        Ok((true, next))
    }
}

// ───────────────────────── asset_group ─────────────────────────

pub struct GroupInput {
    pub parent_id: Option<String>,
    pub name: String,
    pub sort: i64,
}

impl super::Store {
    pub async fn group_list(&self) -> AppResult<Vec<AssetGroupRow>> {
        Ok(
            sqlx::query_as("SELECT * FROM asset_group ORDER BY sort, name")
                .fetch_all(self.pool())
                .await?,
        )
    }

    pub async fn group_create(&self, input: GroupInput) -> AppResult<AssetGroupRow> {
        let id = crate::ids::new_id();
        let now = now_ms() as i64;
        sqlx::query("INSERT INTO asset_group(id, parent_id, name, sort, created_at, updated_at) VALUES(?,?,?,?,?,?)")
            .bind(&id)
            .bind(&input.parent_id)
            .bind(&input.name)
            .bind(input.sort)
            .bind(now)
            .bind(now)
            .execute(self.pool())
            .await?;
        self.group_get(&id).await
    }

    pub async fn group_get(&self, id: &str) -> AppResult<AssetGroupRow> {
        sqlx::query_as("SELECT * FROM asset_group WHERE id = ?")
            .bind(id)
            .fetch_optional(self.pool())
            .await?
            .ok_or_else(|| AppError::NotFound(format!("分组 {id}")))
    }

    pub async fn group_update(
        &self,
        id: &str,
        name: Option<String>,
        parent_id: Option<Option<String>>,
        sort: Option<i64>,
    ) -> AppResult<AssetGroupRow> {
        let row = self.group_get(id).await?;
        let name = name.unwrap_or(row.name);
        let parent = match parent_id {
            Some(p) => p,
            None => row.parent_id,
        };
        let sort = sort.unwrap_or(row.sort);
        // 防环：父级不能是自己或自己的后代
        if parent.as_deref() == Some(id) {
            return Err(AppError::param("父级不能是自己"));
        }
        if let Some(pid) = &parent {
            let mut cursor = pid.clone();
            let mut depth = 0;
            loop {
                let next: Option<Option<String>> =
                    sqlx::query_scalar("SELECT parent_id FROM asset_group WHERE id = ?")
                        .bind(&cursor)
                        .fetch_optional(self.pool())
                        .await?
                        .flatten();
                match next.flatten() {
                    Some(p) => {
                        if p == *id {
                            return Err(AppError::param("不允许把分组移动到自己的后代下"));
                        }
                        cursor = p;
                    }
                    None => break,
                }
                depth += 1;
                if depth > 32 {
                    break;
                }
            }
        }
        sqlx::query("UPDATE asset_group SET name=?, parent_id=?, sort=?, updated_at=? WHERE id=?")
            .bind(&name)
            .bind(&parent)
            .bind(sort)
            .bind(now_ms() as i64)
            .bind(id)
            .execute(self.pool())
            .await?;
        self.group_get(id).await
    }

    pub async fn group_delete(&self, id: &str) -> AppResult<()> {
        sqlx::query("DELETE FROM asset_group WHERE id = ?")
            .bind(id)
            .execute(self.pool())
            .await?;
        Ok(())
    }

    /// 按指定 ID 写入分组（同步导入用）：存在则覆盖，不存在则新建；返回是否新建。
    ///
    /// 与 `group_create` 的唯一区别是 **ID 由调用方给定** —— 同步的两端必须认
    /// 同一套 ID，否则「同一个分组在两台设备上」会被当成两个分组，每次同步都
    /// 变成「新增」而不是「更新」，越同步越乱。
    ///
    /// 两点刻意行为：
    /// - `created_at` 新建时取传入值、**更新时不动** —— 「这条什么时候建的」
    ///   是本地事实，导入不该改写它（`updated_at` 才表示内容新旧）。
    /// - 调用方必须保证父分组先于子分组写入（`parent_id` 是自引用外键，
    ///   父不在库里时 INSERT 直接失败）。拓扑排序在 `sync::apply` 里做。
    pub async fn group_upsert(
        &self,
        id: &str,
        parent_id: Option<&str>,
        name: &str,
        sort: i64,
        created_at: i64,
        updated_at: i64,
    ) -> AppResult<bool> {
        crate::store::ensure_id(id)?;
        if name.trim().is_empty() {
            return Err(AppError::param("分组名称不能为空"));
        }
        let existed: Option<(String,)> = sqlx::query_as("SELECT id FROM asset_group WHERE id = ?")
            .bind(id)
            .fetch_optional(self.pool())
            .await?;
        sqlx::query(
            "INSERT INTO asset_group(id, parent_id, name, sort, created_at, updated_at)
             VALUES(?,?,?,?,?,?)
             ON CONFLICT(id) DO UPDATE SET
                parent_id = excluded.parent_id, name = excluded.name,
                sort = excluded.sort, updated_at = excluded.updated_at",
        )
        .bind(id)
        .bind(parent_id)
        .bind(name.trim())
        .bind(sort)
        .bind(created_at)
        .bind(updated_at)
        .execute(self.pool())
        .await?;
        Ok(existed.is_none())
    }
}

// ───────────────────────── asset ─────────────────────────

pub struct AssetInput {
    pub group_id: Option<String>,
    pub kind: String,
    pub name: String,
    pub host: Option<String>,
    pub port: Option<i32>,
    pub username: Option<String>,
    pub auth_kind: Option<String>,
    pub key_path: Option<String>,
    pub cred_id: Option<String>,
    pub options_json: String,
    pub tags: String,
    pub note: String,
    pub sort: i64,
}

impl super::Store {
    pub async fn asset_list(&self, include_deleted: bool) -> AppResult<Vec<AssetRow>> {
        let sql = if include_deleted {
            "SELECT * FROM asset ORDER BY sort, name"
        } else {
            "SELECT * FROM asset WHERE deleted_at IS NULL ORDER BY sort, name"
        };
        let rows: Vec<AssetRow> = sqlx::query_as(sql).fetch_all(self.pool()).await?;
        Ok(rows)
    }

    pub async fn asset_get(&self, id: &str) -> AppResult<AssetRow> {
        sqlx::query_as("SELECT * FROM asset WHERE id = ?")
            .bind(id)
            .fetch_optional(self.pool())
            .await?
            .ok_or_else(|| AppError::NotFound(format!("资产 {id}")))
    }

    pub async fn asset_create(&self, input: AssetInput) -> AppResult<AssetRow> {
        if input.name.trim().is_empty() {
            return Err(AppError::param("资产名称不能为空"));
        }
        let id = crate::ids::new_id();
        let now = now_ms() as i64;
        sqlx::query(
            "INSERT INTO asset(id, group_id, kind, name, host, port, username, auth_kind,
             key_path, cred_id, options_json, tags, note, sort, created_at, updated_at)
             VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
        )
        .bind(&id)
        .bind(&input.group_id)
        .bind(&input.kind)
        .bind(input.name.trim())
        .bind(&input.host)
        .bind(input.port)
        .bind(&input.username)
        .bind(&input.auth_kind)
        .bind(&input.key_path)
        .bind(&input.cred_id)
        .bind(&input.options_json)
        .bind(&input.tags)
        .bind(&input.note)
        .bind(input.sort)
        .bind(now)
        .bind(now)
        .execute(self.pool())
        .await?;
        self.asset_get(&id).await
    }

    #[allow(clippy::too_many_arguments)]
    pub async fn asset_update(
        &self,
        id: &str,
        group_id: Option<Option<String>>,
        name: Option<String>,
        host: Option<Option<String>>,
        port: Option<Option<i32>>,
        username: Option<Option<String>>,
        auth_kind: Option<Option<String>>,
        key_path: Option<Option<String>>,
        cred_id: Option<Option<String>>,
        options_json: Option<String>,
        tags: Option<String>,
        note: Option<String>,
        sort: Option<i64>,
    ) -> AppResult<AssetRow> {
        let row = self.asset_get(id).await?;
        let group_id = group_id.unwrap_or(row.group_id);
        let name = name.map(|n| n.trim().to_string()).unwrap_or(row.name);
        let host = host.unwrap_or(row.host);
        let port = port.unwrap_or(row.port);
        let username = username.unwrap_or(row.username);
        let auth_kind = auth_kind.unwrap_or(row.auth_kind);
        let key_path = key_path.unwrap_or(row.key_path);
        let cred_id = cred_id.unwrap_or(row.cred_id);
        let options_json = options_json.unwrap_or(row.options_json);
        let tags = tags.unwrap_or(row.tags);
        let note = note.unwrap_or(row.note);
        let sort = sort.unwrap_or(row.sort);
        sqlx::query(
            "UPDATE asset SET group_id=?, name=?, host=?, port=?, username=?, auth_kind=?,
             key_path=?, cred_id=?, options_json=?, tags=?, note=?, sort=?, updated_at=? WHERE id=?",
        )
        .bind(&group_id)
        .bind(&name)
        .bind(&host)
        .bind(port)
        .bind(&username)
        .bind(&auth_kind)
        .bind(&key_path)
        .bind(&cred_id)
        .bind(&options_json)
        .bind(&tags)
        .bind(&note)
        .bind(sort)
        .bind(now_ms() as i64)
        .bind(id)
        .execute(self.pool())
        .await?;
        self.asset_get(id).await
    }

    /// 软删除（墓碑，为同步预留）。
    ///
    /// 内置资产（「当前设备」）拒绝删除：它是「本机」这个概念的锚点，
    /// 删掉之后 `session_connect_local`、终端/容器/文件树这些"要一台机器"的
    /// 入口都会退化成一个悬空的合成会话。UI 上已经不给删除按钮，
    /// 这里再拦一道 —— 命令层能被直接调用（AI 工具、脚本），不能只靠前端自觉。
    pub async fn asset_delete(&self, id: &str) -> AppResult<()> {
        let row = self.asset_get(id).await?;
        if row.builtin {
            return Err(AppError::param("「当前设备」是内置资产，不能删除"));
        }
        sqlx::query("UPDATE asset SET deleted_at = ? WHERE id = ?")
            .bind(now_ms() as i64)
            .bind(id)
            .execute(self.pool())
            .await?;
        Ok(())
    }

    /// 确保内置「当前设备」资产存在，返回它（幂等）。
    ///
    /// 两条路径都走这里：应用启动时 seed 一次；`session_connect_local`
    /// （Ctrl+T / 空态按钮 / 命令面板）每次解析一次。所以必须幂等 ——
    /// 重复调用的代价只是一次主键查询。
    ///
    /// 若那行被软删除过（老版本留下的、或用户手工改库），这里**复活**它：
    /// 内置资产的"不存在"没有第三种解释，留着墓碑只会让「当前设备」
    /// 永远消失又占着 ID，重启也回不来。
    pub async fn asset_ensure_builtin_local(&self) -> AppResult<AssetRow> {
        if let Ok(row) = self.asset_get(BUILTIN_LOCAL_ASSET_ID).await {
            if row.deleted_at.is_some() {
                sqlx::query("UPDATE asset SET deleted_at = NULL, updated_at = ? WHERE id = ?")
                    .bind(now_ms() as i64)
                    .bind(BUILTIN_LOCAL_ASSET_ID)
                    .execute(self.pool())
                    .await?;
                return self.asset_get(BUILTIN_LOCAL_ASSET_ID).await;
            }
            return Ok(row);
        }
        let now = now_ms() as i64;
        // sort = -1：`ORDER BY sort, name` 下它排在最前 —— 本机是绝大多数操作的
        // 起点，排在用户自建资产后面等于每次都要往下找一行。
        sqlx::query(
            "INSERT INTO asset(id, group_id, kind, name, host, port, username, auth_kind,
             key_path, cred_id, options_json, tags, note, sort, created_at, updated_at, builtin)
             VALUES(?,NULL,'local',?,NULL,NULL,NULL,NULL,NULL,NULL,'{}','','',-1,?,?,1)",
        )
        .bind(BUILTIN_LOCAL_ASSET_ID)
        .bind(BUILTIN_LOCAL_ASSET_NAME)
        .bind(now)
        .bind(now)
        .execute(self.pool())
        .await?;
        self.asset_get(BUILTIN_LOCAL_ASSET_ID).await
    }

    /// 全局搜索：匹配名称/主机/用户名/备注/标签。
    pub async fn asset_search(&self, q: &str) -> AppResult<Vec<AssetRow>> {
        let like = format!("%{}%", q.to_ascii_lowercase());
        Ok(sqlx::query_as(
            "SELECT * FROM asset
             WHERE deleted_at IS NULL AND (
               lower(name) LIKE ? OR lower(coalesce(host,'')) LIKE ?
               OR lower(coalesce(username,'')) LIKE ? OR lower(note) LIKE ?
               OR lower(tags) LIKE ?)
             ORDER BY sort, name LIMIT 100",
        )
        .bind(&like)
        .bind(&like)
        .bind(&like)
        .bind(&like)
        .bind(&like)
        .fetch_all(self.pool())
        .await?)
    }

    /// 按指定 ID 写入资产（同步导入用）：存在则覆盖，不存在则新建；返回是否新建。
    ///
    /// 与 `asset_create` / `asset_update` 的三处差异，全都是为了让「导入」不会
    /// 毁掉本地状态：
    /// - **ID 由调用方给定** —— 两端认同一套 ID，「同一台机器」才能在两侧被认成
    ///   同一条；否则每次同步都算「新增」，最后攒出一堆重名资产，而更新永远发不出去。
    /// - **`builtin` 列永不覆盖**，且内置资产的 ID 直接被拒 —— 「是不是内置」是本机
    ///   的属性（「当前设备」这个锚点），不能由对端说了算。
    /// - **`created_at` 只在新插入时采用**，更新时保留本地值。
    ///
    /// `deleted_at` 则**照搬**：墓碑是同步的一等公民，不传播的话「对端删了」
    /// 在本地永远删不掉。
    ///
    /// 调用方须保证 `group_id` / `cred_id` 指向的记录已在库里（外键是硬约束，
    /// 组合不出「先插子后插父」），兜底在 `sync::apply` 里做。
    pub async fn asset_upsert(&self, row: &AssetRow) -> AppResult<bool> {
        if row.id == BUILTIN_LOCAL_ASSET_ID {
            return Err(AppError::param(
                "内置资产「当前设备」不接受同步写入（本机在每台设备上都是各自身份）",
            ));
        }
        crate::store::ensure_id(&row.id)?;
        if row.name.trim().is_empty() {
            return Err(AppError::param("资产名称不能为空"));
        }
        let existed: Option<(String,)> = sqlx::query_as("SELECT id FROM asset WHERE id = ?")
            .bind(&row.id)
            .fetch_optional(self.pool())
            .await?;
        sqlx::query(
            "INSERT INTO asset(id, group_id, kind, name, host, port, username, auth_kind,
             key_path, cred_id, options_json, tags, note, sort, created_at, updated_at,
             deleted_at, builtin)
             VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0)
             ON CONFLICT(id) DO UPDATE SET
                group_id = excluded.group_id, kind = excluded.kind, name = excluded.name,
                host = excluded.host, port = excluded.port, username = excluded.username,
                auth_kind = excluded.auth_kind, key_path = excluded.key_path,
                cred_id = excluded.cred_id, options_json = excluded.options_json,
                tags = excluded.tags, note = excluded.note, sort = excluded.sort,
                updated_at = excluded.updated_at, deleted_at = excluded.deleted_at",
        )
        .bind(&row.id)
        .bind(&row.group_id)
        .bind(&row.kind)
        .bind(row.name.trim())
        .bind(&row.host)
        .bind(row.port)
        .bind(&row.username)
        .bind(&row.auth_kind)
        .bind(&row.key_path)
        .bind(&row.cred_id)
        .bind(&row.options_json)
        .bind(&row.tags)
        .bind(&row.note)
        .bind(row.sort)
        .bind(row.created_at)
        .bind(row.updated_at)
        .bind(row.deleted_at)
        .execute(self.pool())
        .await?;
        Ok(existed.is_none())
    }
}

// ───────────────────────── credential ─────────────────────────

pub struct CredentialInput {
    pub id: Option<String>,
    pub name: String,
    pub kind: String,
    pub nonce: Vec<u8>,
    pub blob: Vec<u8>,
    pub kek_hint: String,
}

/// 凭据引用关系：asset 表里绑定了该凭据的（未删除）资产。凭据页「被谁使用」的数据源。
#[derive(Debug, Clone, sqlx::FromRow, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AssetRef {
    pub id: String,
    pub name: String,
    pub kind: String,
}

impl super::Store {
    pub async fn credential_put(&self, input: CredentialInput) -> AppResult<String> {
        let id = match input.id {
            Some(id) => id,
            None => crate::ids::new_id(),
        };
        let now = now_ms() as i64;
        let exists: Option<(String,)> = sqlx::query_as("SELECT id FROM credential WHERE id = ?")
            .bind(&id)
            .fetch_optional(self.pool())
            .await?;
        if exists.is_some() {
            sqlx::query("UPDATE credential SET name=?, kind=?, cipher=?, nonce=?, blob=?, kek_hint=?, updated_at=? WHERE id=?")
                .bind(&input.name)
                .bind(&input.kind)
                .bind(crate::vault::crypto::CIPHER_XCHACHA)
                .bind(&input.nonce)
                .bind(&input.blob)
                .bind(&input.kek_hint)
                .bind(now)
                .bind(&id)
                .execute(self.pool())
                .await?;
        } else {
            sqlx::query("INSERT INTO credential(id, name, kind, cipher, nonce, blob, kek_hint, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)")
                .bind(&id)
                .bind(&input.name)
                .bind(&input.kind)
                .bind(crate::vault::crypto::CIPHER_XCHACHA)
                .bind(&input.nonce)
                .bind(&input.blob)
                .bind(&input.kek_hint)
                .bind(now)
                .bind(now)
                .execute(self.pool())
                .await?;
        }
        Ok(id)
    }

    pub async fn credential_get_row(&self, id: &str) -> AppResult<CredentialRow> {
        sqlx::query_as("SELECT * FROM credential WHERE id = ?")
            .bind(id)
            .fetch_optional(self.pool())
            .await?
            .ok_or_else(|| AppError::NotFound(format!("凭据 {id}")))
    }

    pub async fn credential_list(&self) -> AppResult<Vec<CredentialRow>> {
        Ok(sqlx::query_as("SELECT * FROM credential ORDER BY name")
            .fetch_all(self.pool())
            .await?)
    }

    pub async fn credential_delete(&self, id: &str) -> AppResult<()> {
        sqlx::query("DELETE FROM credential WHERE id = ?")
            .bind(id)
            .execute(self.pool())
            .await?;
        Ok(())
    }

    /// 一条凭据被哪些在用资产引用（asset.cred_id 反查，软删除的不算）。
    pub async fn credential_usage(&self, cred_id: &str) -> AppResult<Vec<AssetRef>> {
        Ok(sqlx::query_as::<_, AssetRef>(
            "SELECT id, name, kind FROM asset WHERE cred_id = ? AND deleted_at IS NULL ORDER BY sort, name",
        )
        .bind(cred_id)
        .fetch_all(self.pool())
        .await?)
    }
}

// ───────────────────────── audit_log ─────────────────────────

#[derive(Debug, Clone, Default)]
pub struct AuditQuery {
    pub session_id: Option<String>,
    pub asset_id: Option<String>,
    pub source: Option<String>,
    pub kind: Option<String>,
    pub limit: i64,
    pub offset: i64,
}

pub struct AuditInput {
    pub session_id: Option<String>,
    pub asset_id: Option<String>,
    pub source: &'static str,
    pub kind: &'static str,
    pub payload: serde_json::Value,
    pub exit_code: Option<i32>,
    pub duration_ms: Option<i64>,
}

impl super::Store {
    pub async fn audit_insert(&self, input: AuditInput) -> AppResult<()> {
        sqlx::query(
            "INSERT INTO audit_log(ts, session_id, asset_id, source, kind, payload_json, exit_code, duration_ms)
             VALUES(?,?,?,?,?,?,?,?)",
        )
        .bind(now_ms() as i64)
        .bind(&input.session_id)
        .bind(&input.asset_id)
        .bind(input.source)
        .bind(input.kind)
        .bind(input.payload.to_string())
        .bind(input.exit_code)
        .bind(input.duration_ms)
        .execute(self.pool())
        .await?;
        Ok(())
    }

    pub async fn audit_query(&self, q: AuditQuery) -> AppResult<Vec<AuditRow>> {
        let limit = if q.limit <= 0 || q.limit > 1000 {
            200
        } else {
            q.limit
        };
        Ok(sqlx::query_as(
            "SELECT * FROM audit_log
             WHERE (? IS NULL OR session_id = ?)
               AND (? IS NULL OR asset_id = ?)
               AND (? IS NULL OR source = ?)
               AND (? IS NULL OR kind = ?)
             ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?",
        )
        .bind(&q.session_id)
        .bind(&q.session_id)
        .bind(&q.asset_id)
        .bind(&q.asset_id)
        .bind(&q.source)
        .bind(&q.source)
        .bind(&q.kind)
        .bind(&q.kind)
        .bind(limit)
        .bind(q.offset)
        .fetch_all(self.pool())
        .await?)
    }
}

// ───────────────────────── ai_conversation / message ─────────────────────────

impl super::Store {
    pub async fn conv_create(
        &self,
        title: &str,
        scope: &serde_json::Value,
    ) -> AppResult<ConversationRow> {
        let id = crate::ids::new_id();
        let now = now_ms() as i64;
        sqlx::query("INSERT INTO ai_conversation(id, title, scope_json, created_at, updated_at) VALUES(?,?,?,?,?)")
            .bind(&id)
            .bind(title)
            .bind(scope.to_string())
            .bind(now)
            .bind(now)
            .execute(self.pool())
            .await?;
        self.conv_get(&id).await
    }

    pub async fn conv_get(&self, id: &str) -> AppResult<ConversationRow> {
        sqlx::query_as("SELECT * FROM ai_conversation WHERE id = ?")
            .bind(id)
            .fetch_optional(self.pool())
            .await?
            .ok_or_else(|| AppError::NotFound(format!("会话 {id}")))
    }

    pub async fn conv_list(&self) -> AppResult<Vec<ConversationRow>> {
        Ok(
            sqlx::query_as("SELECT * FROM ai_conversation ORDER BY updated_at DESC")
                .fetch_all(self.pool())
                .await?,
        )
    }

    pub async fn conv_rename(&self, id: &str, title: &str) -> AppResult<()> {
        sqlx::query("UPDATE ai_conversation SET title=?, updated_at=? WHERE id=?")
            .bind(title)
            .bind(now_ms() as i64)
            .bind(id)
            .execute(self.pool())
            .await?;
        Ok(())
    }

    pub async fn conv_touch(&self, id: &str) -> AppResult<()> {
        sqlx::query("UPDATE ai_conversation SET updated_at=? WHERE id=?")
            .bind(now_ms() as i64)
            .bind(id)
            .execute(self.pool())
            .await?;
        Ok(())
    }

    pub async fn conv_delete(&self, id: &str) -> AppResult<()> {
        // 消息必须先删：只删会话的话，这些消息会变成永远查不到的孤儿 ——
        // 会话列表里没有它，`ai_messages` 也再不会指向它，只能手写 SQL 才清得掉。
        sqlx::query("DELETE FROM ai_message WHERE conversation_id = ?")
            .bind(id)
            .execute(self.pool())
            .await?;
        sqlx::query("DELETE FROM ai_conversation WHERE id = ?")
            .bind(id)
            .execute(self.pool())
            .await?;
        Ok(())
    }

    pub async fn msg_insert(
        &self,
        conversation_id: &str,
        role: &str,
        content_json: &serde_json::Value,
        tokens_in: Option<i64>,
        tokens_out: Option<i64>,
    ) -> AppResult<()> {
        sqlx::query(
            "INSERT INTO ai_message(id, conversation_id, role, content_json, tokens_in, tokens_out, created_at)
             VALUES(?,?,?,?,?,?,?)",
        )
        .bind(crate::ids::new_id())
        .bind(conversation_id)
        .bind(role)
        .bind(content_json.to_string())
        .bind(tokens_in)
        .bind(tokens_out)
        .bind(now_ms() as i64)
        .execute(self.pool())
        .await?;
        self.conv_touch(conversation_id).await?;
        Ok(())
    }

    pub async fn msg_list(&self, conversation_id: &str) -> AppResult<Vec<MessageRow>> {
        Ok(sqlx::query_as(
            "SELECT * FROM ai_message WHERE conversation_id = ? ORDER BY created_at, id",
        )
        .bind(conversation_id)
        .fetch_all(self.pool())
        .await?)
    }
}

// ───────────────────────── snippet ─────────────────────────

impl super::Store {
    pub async fn snippet_list(&self) -> AppResult<Vec<SnippetRow>> {
        Ok(sqlx::query_as("SELECT * FROM snippet ORDER BY sort, name")
            .fetch_all(self.pool())
            .await?)
    }

    pub async fn snippet_create(
        &self,
        name: &str,
        body: &str,
        group_id: Option<String>,
        sort: i64,
    ) -> AppResult<SnippetRow> {
        let id = crate::ids::new_id();
        let now = now_ms() as i64;
        sqlx::query("INSERT INTO snippet(id, group_id, name, body, sort, created_at, updated_at) VALUES(?,?,?,?,?,?,?)")
            .bind(&id)
            .bind(&group_id)
            .bind(name)
            .bind(body)
            .bind(sort)
            .bind(now)
            .bind(now)
            .execute(self.pool())
            .await?;
        self.snippet_get(&id).await
    }

    pub async fn snippet_get(&self, id: &str) -> AppResult<SnippetRow> {
        sqlx::query_as("SELECT * FROM snippet WHERE id = ?")
            .bind(id)
            .fetch_optional(self.pool())
            .await?
            .ok_or_else(|| AppError::NotFound(format!("片段 {id}")))
    }

    pub async fn snippet_update(&self, id: &str, name: &str, body: &str) -> AppResult<()> {
        sqlx::query("UPDATE snippet SET name=?, body=?, updated_at=? WHERE id=?")
            .bind(name)
            .bind(body)
            .bind(now_ms() as i64)
            .bind(id)
            .execute(self.pool())
            .await?;
        Ok(())
    }

    pub async fn snippet_delete(&self, id: &str) -> AppResult<()> {
        sqlx::query("DELETE FROM snippet WHERE id = ?")
            .bind(id)
            .execute(self.pool())
            .await?;
        Ok(())
    }
}

// ───────────────────────── known_host ─────────────────────────

impl super::Store {
    /// 查已记录指纹。返回 None 表示首次连接。
    pub async fn known_host_get(
        &self,
        host: &str,
        port: i32,
        key_type: &str,
    ) -> AppResult<Option<KnownHostRow>> {
        Ok(
            sqlx::query_as("SELECT * FROM known_host WHERE host=? AND port=? AND key_type=?")
                .bind(host)
                .bind(port)
                .bind(key_type)
                .fetch_optional(self.pool())
                .await?,
        )
    }

    /// 记录/更新指纹（用户确认后调用；Strict 策略下变更需显式确认）。
    pub async fn known_host_accept(
        &self,
        host: &str,
        port: i32,
        key_type: &str,
        fingerprint: &str,
    ) -> AppResult<()> {
        let exists = self.known_host_get(host, port, key_type).await?;
        if exists.is_some() {
            sqlx::query("UPDATE known_host SET fingerprint=?, added_at=? WHERE host=? AND port=? AND key_type=?")
                .bind(fingerprint)
                .bind(now_ms() as i64)
                .bind(host)
                .bind(port)
                .bind(key_type)
                .execute(self.pool())
                .await?;
        } else {
            sqlx::query("INSERT INTO known_host(id, host, port, key_type, fingerprint, added_at) VALUES(?,?,?,?,?,?)")
                .bind(crate::ids::new_id())
                .bind(host)
                .bind(port)
                .bind(key_type)
                .bind(fingerprint)
                .bind(now_ms() as i64)
                .execute(self.pool())
                .await?;
        }
        Ok(())
    }

    pub async fn known_host_list(&self) -> AppResult<Vec<KnownHostRow>> {
        Ok(
            sqlx::query_as("SELECT * FROM known_host ORDER BY host, port")
                .fetch_all(self.pool())
                .await?,
        )
    }

    pub async fn known_host_remove(&self, id: &str) -> AppResult<()> {
        sqlx::query("DELETE FROM known_host WHERE id = ?")
            .bind(id)
            .execute(self.pool())
            .await?;
        Ok(())
    }
}

// ───────────────────────── terminal_recording ─────────────────────────

impl super::Store {
    pub async fn recording_start(
        &self,
        session_id: &str,
        tab_id: &str,
        path: &str,
    ) -> AppResult<String> {
        let id = crate::ids::new_id();
        sqlx::query("INSERT INTO terminal_recording(id, session_id, tab_id, path, bytes, started_at) VALUES(?,?,?,?,0,?)")
            .bind(&id)
            .bind(session_id)
            .bind(tab_id)
            .bind(path)
            .bind(now_ms() as i64)
            .execute(self.pool())
            .await?;
        Ok(id)
    }

    pub async fn recording_update_bytes(&self, id: &str, bytes: i64) -> AppResult<()> {
        sqlx::query("UPDATE terminal_recording SET bytes=? WHERE id=?")
            .bind(bytes)
            .bind(id)
            .execute(self.pool())
            .await?;
        Ok(())
    }

    pub async fn recording_end(&self, id: &str) -> AppResult<()> {
        sqlx::query("UPDATE terminal_recording SET ended_at=? WHERE id=?")
            .bind(now_ms() as i64)
            .bind(id)
            .execute(self.pool())
            .await?;
        Ok(())
    }

    pub async fn recording_list(&self) -> AppResult<Vec<RecordingRow>> {
        Ok(
            sqlx::query_as("SELECT * FROM terminal_recording ORDER BY started_at DESC LIMIT 200")
                .fetch_all(self.pool())
                .await?,
        )
    }
}

/// 供 reconnect 模块使用的保活键名常量测试兜底（避免 unused 警告）。
#[allow(dead_code)]
pub(crate) fn _keepalive_key() -> &'static str {
    KEEPALIVE
}

#[cfg(test)]
mod builtin_tests {
    use super::*;
    use crate::store::Store;

    /// 内置资产的固定 ID 必须过得了参数校验层（`ensure_id` 要求 26 位 ASCII 字母数字）。
    /// 写错一位（比如 25 位、带连字符）不会编译报错，而是在用户点「新建终端」时
    /// 变成一句「非法 ID」—— 那种错只能靠这条用例挡。
    #[test]
    fn builtin_id_is_a_valid_ulid_shape() {
        crate::store::ensure_id(BUILTIN_LOCAL_ASSET_ID).expect("内置资产 ID 形态必须合法");
    }

    #[tokio::test]
    async fn ensure_builtin_is_idempotent() {
        let store = Store::open_in_memory().await.expect("内存库");
        let a = store.asset_ensure_builtin_local().await.expect("首次 seed");
        assert_eq!(a.id, BUILTIN_LOCAL_ASSET_ID);
        assert_eq!(a.kind, "local");
        assert!(a.builtin, "必须带上内置标记");
        assert!(a.deleted_at.is_none());

        let b = store.asset_ensure_builtin_local().await.expect("再次调用");
        assert_eq!(a.id, b.id);
        assert_eq!(b.created_at, a.created_at, "重复调用不能重建（时间戳会变）");
        assert_eq!(store.asset_list(false).await.unwrap().len(), 1);
    }

    /// 内置资产排在自建资产前面（`ORDER BY sort, name`，内置 sort = -1）。
    #[tokio::test]
    async fn builtin_sorts_first() {
        let store = Store::open_in_memory().await.expect("内存库");
        store.asset_ensure_builtin_local().await.expect("seed");
        store
            .asset_create(AssetInput {
                group_id: None,
                kind: "ssh".into(),
                name: "web-01".into(),
                host: Some("10.0.0.1".into()),
                port: Some(22),
                username: Some("root".into()),
                auth_kind: Some("password".into()),
                key_path: None,
                cred_id: None,
                options_json: "{}".into(),
                tags: String::new(),
                note: String::new(),
                sort: 0,
            })
            .await
            .expect("建资产");
        let list = store.asset_list(false).await.unwrap();
        assert_eq!(list[0].id, BUILTIN_LOCAL_ASSET_ID, "当前设备应排在最前");
    }

    /// 内置资产不可删除 —— 命令层能被脚本/AI 直接调，不能只靠前端藏按钮。
    #[tokio::test]
    async fn builtin_cannot_be_deleted() {
        let store = Store::open_in_memory().await.expect("内存库");
        store.asset_ensure_builtin_local().await.expect("seed");
        let err = store
            .asset_delete(BUILTIN_LOCAL_ASSET_ID)
            .await
            .expect_err("必须拒绝");
        assert_eq!(err.code(), "bad_param");
        assert!(
            store.asset_get(BUILTIN_LOCAL_ASSET_ID).await.is_ok(),
            "还在"
        );
    }

    /// 墓碑残留（老版本删过 / 手工改库）要能复活：否则「当前设备」永远消失又占着 ID。
    #[tokio::test]
    async fn ensure_builtin_revives_tombstone() {
        let store = Store::open_in_memory().await.expect("内存库");
        store.asset_ensure_builtin_local().await.expect("seed");
        // 绕过 asset_delete 的拒绝，直接造一个墓碑（模拟历史遗留）
        sqlx::query("UPDATE asset SET deleted_at = 1 WHERE id = ?")
            .bind(BUILTIN_LOCAL_ASSET_ID)
            .execute(store.pool())
            .await
            .expect("写墓碑");
        assert!(
            store.asset_list(false).await.unwrap().is_empty(),
            "墓碑不该出现在列表里"
        );

        let back = store.asset_ensure_builtin_local().await.expect("复活");
        assert!(back.deleted_at.is_none(), "应已复活");
        assert_eq!(store.asset_list(false).await.unwrap().len(), 1);
    }

    /// 布局：乐观锁必须**真的**挡住后写的那个。
    ///
    /// 这条要是失效，症状是「两台设备同时改布局，后写的那份把先写的整份覆盖掉」——
    /// 丢的是对方所有的窗口与标签，而两边都显示「保存成功」。
    #[tokio::test]
    async fn layout_rejects_stale_revision() {
        let store = Store::open_in_memory().await.expect("内存库");

        let (rev, _, data) = store.layout_load().await.expect("读空库");
        assert_eq!(rev, 0, "没存过时 revision 应为 0");
        assert!(data.is_none(), "没存过时不该有正文");

        let (ok, r1) = store.layout_save(0, r#"{"panes":1}"#).await.expect("首写");
        assert!(ok, "首写应当成功");
        assert_eq!(r1, 1);

        // 另一台设备拿着同一个 revision=0 来写 → 必须被拒
        let (ok2, r2) = store
            .layout_save(0, r#"{"panes":999}"#)
            .await
            .expect("过期写");
        assert!(
            !ok2,
            "过期 revision 必须被拒（否则就是静默覆盖对方整份布局）"
        );
        assert_eq!(r2, 1, "应把当前 revision 报回去，好让前端先拉最新");

        // 正文没有被污染
        let (_, _, data) = store.layout_load().await.expect("回读");
        assert_eq!(data.as_deref(), Some(r#"{"panes":1}"#));

        // 拿对 revision 就能写
        let (ok3, r3) = store
            .layout_save(1, r#"{"panes":2}"#)
            .await
            .expect("正常写");
        assert!(ok3);
        assert_eq!(r3, 2);
    }

    /// 布局正文不是合法 JSON 时要**报参数错误**，而不是把一份坏数据存进去。
    #[tokio::test]
    async fn layout_rejects_malformed_json() {
        let store = Store::open_in_memory().await.expect("内存库");
        assert!(
            store.layout_save(0, "{ not json").await.is_err(),
            "坏 JSON 不该被写进库"
        );
        let (rev, _, data) = store.layout_load().await.unwrap();
        assert_eq!(rev, 0);
        assert!(data.is_none(), "被拒之后库里应当还是空的");
    }

    /// 信封坏掉（老版本写下的 / 被手工改过）不该让整个界面起不来 ——
    /// 当作「还没有布局」，重新存一次即可。
    #[tokio::test]
    async fn layout_tolerates_corrupt_envelope() {
        let store = Store::open_in_memory().await.expect("内存库");
        store
            .setting_set(Store::LAYOUT_KEY, "这不是 JSON")
            .await
            .expect("塞坏值");
        let (rev, _, data) = store.layout_load().await.expect("读坏值不该报错");
        assert_eq!(rev, 0);
        assert!(data.is_none());
    }
}

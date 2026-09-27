//! 仓储函数：资产 / 分组 / 凭据 / 设置 / 审计 / AI 会话 / 片段 / 已知主机 / 录制。

use crate::error::{AppError, AppResult};
use crate::ids::now_ms;
use crate::store::models::*;

const KEEPALIVE: &str = "keepalive";

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
    pub async fn asset_delete(&self, id: &str) -> AppResult<()> {
        sqlx::query("UPDATE asset SET deleted_at = ? WHERE id = ?")
            .bind(now_ms() as i64)
            .bind(id)
            .execute(self.pool())
            .await?;
        Ok(())
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

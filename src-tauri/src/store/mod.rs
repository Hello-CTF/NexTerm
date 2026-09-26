//! SQLite 存储层：连接池 + 迁移 + 仓储（L4：业务无关）。

pub mod models;
mod repo;

pub use models::*;
pub use repo::{AssetInput, AuditInput, AuditQuery, CredentialInput, GroupInput};

use sqlx::sqlite::SqlitePoolOptions;
use sqlx::SqlitePool;

use crate::error::{AppError, AppResult};

#[derive(Clone)]
pub struct Store {
    pool: SqlitePool,
}

impl Store {
    /// 打开（或创建）数据库并执行迁移。
    pub async fn open(path: &std::path::Path) -> AppResult<Self> {
        if let Some(dir) = path.parent() {
            tokio::fs::create_dir_all(dir).await?;
        }
        let url = format!(
            "sqlite://{}?mode=rwc",
            path.to_string_lossy().replace('\\', "/")
        );
        let pool = SqlitePoolOptions::new()
            .max_connections(8)
            .after_connect(|conn, _meta| {
                Box::pin(async move {
                    // WAL + 外键约束 + NORMAL 同步（§3）
                    sqlx::query("PRAGMA journal_mode = WAL;")
                        .execute(&mut *conn)
                        .await?;
                    sqlx::query("PRAGMA synchronous = NORMAL;")
                        .execute(&mut *conn)
                        .await?;
                    sqlx::query("PRAGMA foreign_keys = ON;")
                        .execute(&mut *conn)
                        .await?;
                    Ok(())
                })
            })
            .connect(&url)
            .await?;
        let store = Self { pool };
        store.migrate().await?;
        Ok(store)
    }

    /// 供测试使用的内存库。
    pub async fn open_in_memory() -> AppResult<Self> {
        let pool = SqlitePoolOptions::new()
            .max_connections(1)
            .connect("sqlite::memory:")
            .await?;
        let store = Self { pool };
        store.migrate().await?;
        Ok(store)
    }

    async fn migrate(&self) -> AppResult<()> {
        sqlx::migrate!("./migrations").run(&self.pool).await?;
        Ok(())
    }

    pub fn pool(&self) -> &SqlitePool {
        &self.pool
    }

    pub async fn close(&self) {
        self.pool.close().await;
    }
}

/// 把可选的 json 字符串解析为 Value，坏数据不致崩。
pub fn parse_json_or(s: &str) -> serde_json::Value {
    serde_json::from_str(s).unwrap_or_else(|e| {
        tracing::warn!(target: "store", "坏 JSON 字段已兜底: {e}");
        serde_json::Value::Object(Default::default())
    })
}

/// 校验 ULID 形态 ID，防注入路径滥用（参数校验层）。
pub fn ensure_id(id: &str) -> AppResult<()> {
    if id.len() == 26 && id.bytes().all(|b| b.is_ascii_alphanumeric()) {
        Ok(())
    } else {
        Err(AppError::param(format!("非法 ID: {id}")))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn migration_creates_all_tables() {
        let store = Store::open_in_memory().await.unwrap();
        let rows: Vec<(String,)> =
            sqlx::query_as("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
                .fetch_all(store.pool())
                .await
                .unwrap();
        let names: Vec<String> = rows.into_iter().map(|(n,)| n).collect();
        for expected in [
            "asset_group",
            "asset",
            "credential",
            "setting",
            "audit_log",
            "ai_conversation",
            "ai_message",
            "snippet",
            "known_host",
            "terminal_recording",
        ] {
            assert!(names.contains(&expected.to_string()), "缺表: {expected}");
        }
    }

    #[test]
    fn id_validation() {
        assert!(ensure_id(&"A".repeat(26)).is_ok());
        assert!(ensure_id("short").is_err());
        assert!(ensure_id(&format!("{}!", "A".repeat(25))).is_err());
    }
}

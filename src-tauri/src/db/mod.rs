//! 数据库管理（M3）：v1 只做 MySQL/MariaDB + Redis（§8 技术选型 v1 范围）。

pub mod mysql;
pub mod redis;

use std::collections::HashMap;
use std::time::Duration;

use serde::Serialize;
use tokio::sync::RwLock;

use ::redis::aio::MultiplexedConnection as RedisConn;
use ::redis::Client as RedisClient;
use sqlx::{Column as _, Row as _};

use crate::error::{AppError, AppResult};

/// 查询结果（表格化）。
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct QueryResult {
    pub columns: Vec<String>,
    pub rows: Vec<Vec<serde_json::Value>>,
    pub rows_affected: u64,
    pub duration_ms: u64,
    pub truncated: bool,
    pub error: Option<String>,
}

/// Redis 键视图。
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct RedisKeyView {
    pub key: String,
    pub key_type: String,
    pub ttl: i64,
    pub value: serde_json::Value,
}

/// 连接池容器。
pub enum DbConn {
    MySql(sqlx::MySqlPool),
    Redis {
        client: RedisClient,
        conn: tokio::sync::Mutex<Option<RedisConn>>,
    },
}

/// 连接管理器。
#[derive(Default)]
pub struct DbManager {
    pub conns: RwLock<HashMap<String, DbConn>>,
}

impl DbManager {
    pub async fn put(&self, id: String, conn: DbConn) {
        self.conns.write().await.insert(id, conn);
    }

    pub async fn get(&self, id: &str) -> AppResult<DbRef> {
        let guard = self.conns.read().await;
        let conn = guard
            .get(id)
            .ok_or_else(|| AppError::NotFound(format!("数据库连接 {id}")))?;
        match conn {
            DbConn::MySql(p) => Ok(DbRef::MySql(p.clone())),
            DbConn::Redis { client, .. } => Ok(DbRef::Redis(client.clone())),
        }
    }

    pub async fn remove(&self, id: &str) -> AppResult<()> {
        let mut guard = self.conns.write().await;
        if let Some(DbConn::MySql(p)) = guard.remove(id) {
            p.close().await;
        }
        Ok(())
    }

    pub async fn close_all(&self) {
        let mut guard = self.conns.write().await;
        for (_, c) in guard.drain() {
            if let DbConn::MySql(p) = c {
                p.close().await;
            }
        }
    }
}

/// 供操作使用的连接引用。
pub enum DbRef {
    MySql(sqlx::MySqlPool),
    Redis(RedisClient),
}

/// 大结果集上限（§5.7：超阈值警告，不一次拉 100 万行）。
pub const MAX_ROWS: usize = 5_000;

/// MySQL 查询执行（超时 + 行数上限）。
pub async fn mysql_query(
    pool: &sqlx::MySqlPool,
    sql: &str,
    timeout: Duration,
) -> AppResult<QueryResult> {
    let start = std::time::Instant::now();
    let q = tokio::time::timeout(timeout, sqlx::query(sql).fetch_all(pool)).await;
    match q {
        Err(_) => Err(AppError::Timeout(format!(
            "SQL 超时（{}s）",
            timeout.as_secs()
        ))),
        Ok(Err(e)) => Ok(QueryResult {
            columns: vec![],
            rows: vec![],
            rows_affected: 0,
            duration_ms: start.elapsed().as_millis() as u64,
            truncated: false,
            error: Some(e.to_string()),
        }),
        Ok(Ok(rows)) => {
            if rows.is_empty() {
                return Ok(QueryResult {
                    columns: vec![],
                    rows: vec![],
                    rows_affected: 0,
                    duration_ms: start.elapsed().as_millis() as u64,
                    truncated: false,
                    error: None,
                });
            }
            let columns: Vec<String> = rows[0]
                .columns()
                .iter()
                .map(|c| c.name().to_string())
                .collect();
            let mut out = Vec::with_capacity(rows.len());
            let mut truncated = false;
            for row in rows.into_iter().take(MAX_ROWS + 1) {
                if out.len() >= MAX_ROWS {
                    truncated = true;
                    break;
                }
                let mut cells = Vec::with_capacity(columns.len());
                for i in 0..columns.len() {
                    let v: serde_json::Value = if row.try_get::<Option<i64>, _>(i).is_ok() {
                        row.try_get::<Option<i64>, _>(i)
                            .map(|v| {
                                v.map(serde_json::Value::from)
                                    .unwrap_or(serde_json::Value::Null)
                            })
                            .unwrap_or(serde_json::Value::Null)
                    } else if row.try_get::<Option<f64>, _>(i).is_ok() {
                        row.try_get::<Option<f64>, _>(i)
                            .map(|v| {
                                v.map(serde_json::Value::from)
                                    .unwrap_or(serde_json::Value::Null)
                            })
                            .unwrap_or(serde_json::Value::Null)
                    } else if row.try_get::<Option<bool>, _>(i).is_ok() {
                        row.try_get::<Option<bool>, _>(i)
                            .map(|v| {
                                v.map(serde_json::Value::from)
                                    .unwrap_or(serde_json::Value::Null)
                            })
                            .unwrap_or(serde_json::Value::Null)
                    } else {
                        row.try_get::<Option<String>, _>(i)
                            .map(|v| {
                                v.map(serde_json::Value::from)
                                    .unwrap_or(serde_json::Value::Null)
                            })
                            .unwrap_or(serde_json::Value::Null)
                    };
                    cells.push(v);
                }
                out.push(cells);
            }
            Ok(QueryResult {
                columns,
                rows: out,
                rows_affected: 0,
                duration_ms: start.elapsed().as_millis() as u64,
                truncated,
                error: None,
            })
        }
    }
}

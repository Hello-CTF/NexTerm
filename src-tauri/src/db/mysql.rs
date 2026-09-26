//! MySQL / MariaDB（sqlx）：连接、库表浏览、查询。

use std::time::Duration;

use sqlx::Row as _;

use crate::error::{AppError, AppResult};

/// 建立连接（可走 SSH 隧道端口，由命令层先建好 forward）。
pub async fn connect(
    host: &str,
    port: u16,
    username: &str,
    password: &str,
    database: Option<&str>,
) -> AppResult<sqlx::MySqlPool> {
    let url = format!(
        "mysql://{username}:{password}@{host}:{port}/{}",
        database.unwrap_or(""),
    );
    let pool = sqlx::mysql::MySqlPoolOptions::new()
        .max_connections(4)
        .acquire_timeout(Duration::from_secs(10))
        .connect(&url)
        .await
        .map_err(|e| AppError::Internal(format!("MySQL 连接失败: {e}")))?;
    Ok(pool)
}

/// 库列表。
pub async fn schemas(pool: &sqlx::MySqlPool) -> AppResult<Vec<String>> {
    let rows = sqlx::query_as::<_, (String,)>("SHOW DATABASES")
        .fetch_all(pool)
        .await
        .map_err(|e| AppError::Internal(e.to_string()))?;
    Ok(rows.into_iter().map(|(d,)| d).collect())
}

/// 表列表。
pub async fn tables(pool: &sqlx::MySqlPool, schema: &str) -> AppResult<Vec<String>> {
    let rows = sqlx::query_as::<_, (String,)>(
        "SELECT table_name FROM information_schema.tables WHERE table_schema = ? ORDER BY table_name",
    )
    .bind(schema)
    .fetch_all(pool)
    .await
    .map_err(|e| AppError::Internal(e.to_string()))?;
    Ok(rows.into_iter().map(|(t,)| t).collect())
}

/// 表结构。
pub async fn describe(
    pool: &sqlx::MySqlPool,
    schema: &str,
    table: &str,
) -> AppResult<serde_json::Value> {
    // 标识符不可参数化：做严格白名单校验防注入
    if !is_safe_identifier(schema) || !is_safe_identifier(table) {
        return Err(AppError::param("表名/库名包含非法字符"));
    }
    let columns = sqlx::query(
        "SELECT column_name, data_type, is_nullable, column_key, column_default, extra
         FROM information_schema.columns WHERE table_schema = ? AND table_name = ? ORDER BY ordinal_position",
    )
    .bind(schema)
    .bind(table)
    .fetch_all(pool)
    .await
    .map_err(|e| AppError::Internal(e.to_string()))?;
    let indexes = sqlx::query(
        "SELECT index_name, non_unique, seq_in_index, column_name
         FROM information_schema.statistics WHERE table_schema = ? AND table_name = ? ORDER BY index_name, seq_in_index",
    )
    .bind(schema)
    .bind(table)
    .fetch_all(pool)
    .await
    .map_err(|e| AppError::Internal(e.to_string()))?;

    let cols: Vec<serde_json::Value> = columns
        .iter()
        .map(|r| {
            serde_json::json!({
                "name": r.try_get::<String, _>("column_name").unwrap_or_default(),
                "type": r.try_get::<String, _>("data_type").unwrap_or_default(),
                "nullable": r.try_get::<String, _>("is_nullable").unwrap_or_default() == "YES",
                "key": r.try_get::<String, _>("column_key").unwrap_or_default(),
                "default": r.try_get::<Option<String>, _>("column_default").ok(),
                "extra": r.try_get::<String, _>("extra").unwrap_or_default(),
            })
        })
        .collect();
    let idx: Vec<serde_json::Value> = indexes
        .iter()
        .map(|r| {
            serde_json::json!({
                "name": r.try_get::<String, _>("index_name").unwrap_or_default(),
                "unique": r.try_get::<i64, _>("non_unique").unwrap_or(1) == 0,
                "column": r.try_get::<String, _>("column_name").unwrap_or_default(),
                "seq": r.try_get::<i64, _>("seq_in_index").unwrap_or(0),
            })
        })
        .collect();
    Ok(serde_json::json!({ "columns": cols, "indexes": idx }))
}

/// 标识符白名单（字母数字下划线美元）。
pub fn is_safe_identifier(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 64
        && s.chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '$')
}

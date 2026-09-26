//! 数据库命令（§6.2 db）：MySQL + Redis。

use serde::Deserialize;
use std::collections::HashMap;
use std::sync::Arc;

use crate::db::{DbConn, DbRef, QueryResult, RedisKeyView};
use crate::error::{AppError, AppResult};
use crate::state::ManagedState;

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DbConnectArgs {
    pub asset_id: Option<String>,
    /// 内联连接参数（不走资产）。
    pub inline: Option<InlineDb>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct InlineDb {
    pub kind: String, // mysql | redis
    pub host: String,
    pub port: Option<u16>,
    pub username: Option<String>,
    pub password: Option<String>,
    pub database: Option<String>,
    /// SSH 隧道复用（v1：传入已建立的本地转发端口）。
    pub via_session: Option<String>,
}

#[tauri::command]
pub async fn db_connect(
    state: ManagedState<'_>,
    args: DbConnectArgs,
) -> AppResult<serde_json::Value> {
    let (kind, host, port, user, pass, database) = if let Some(a) = &args.asset_id {
        let asset = state.store.asset_get(a).await?;
        let options = crate::transport::parse_options(&asset.options_json);
        let password = crate::session::asset_credential(&state, &asset)
            .await?
            .unwrap_or_default();
        (
            asset.kind.clone(),
            asset.host.clone().unwrap_or_default(),
            asset.port.map(|p| p as u16),
            asset.username.clone().unwrap_or_default(),
            password,
            options
                .get("database")
                .and_then(|v| v.as_str())
                .map(String::from),
        )
    } else if let Some(i) = &args.inline {
        (
            i.kind.clone(),
            i.host.clone(),
            i.port,
            i.username.clone().unwrap_or_default(),
            i.password.clone().unwrap_or_default(),
            i.database.clone(),
        )
    } else {
        return Err(AppError::param("需要 assetId 或 inline 参数"));
    };

    let conn_id = crate::ids::new_id();
    match kind.as_str() {
        "mysql" => {
            let pool = crate::db::mysql::connect(
                &host,
                port.unwrap_or(3306),
                &user,
                &pass,
                database.as_deref(),
            )
            .await?;
            state.db.put(conn_id.clone(), DbConn::MySql(pool)).await;
        }
        "redis" => {
            let url = if pass.is_empty() {
                format!("redis://{host}:{}/", port.unwrap_or(6379))
            } else {
                format!("redis://:{pass}@{host}:{}/", port.unwrap_or(6379))
            };
            let client = crate::db::redis::connect(&url).await?;
            state
                .db
                .put(
                    conn_id.clone(),
                    DbConn::Redis {
                        client,
                        conn: tokio::sync::Mutex::new(None),
                    },
                )
                .await;
        }
        other => return Err(AppError::param(format!("不支持的数据库类型 {other}"))),
    }
    Ok(serde_json::json!({ "connId": conn_id }))
}

#[tauri::command]
pub async fn db_disconnect(state: ManagedState<'_>, conn_id: String) -> AppResult<()> {
    state.db.remove(&conn_id).await
}

async fn mysql_of(state: &ManagedState<'_>, conn_id: &str) -> AppResult<sqlx::MySqlPool> {
    match state.db.get(conn_id).await? {
        DbRef::MySql(p) => Ok(p),
        DbRef::Redis(_) => Err(AppError::param("该连接是 Redis")),
    }
}

async fn redis_of(state: &ManagedState<'_>, conn_id: &str) -> AppResult<redis::Client> {
    match state.db.get(conn_id).await? {
        DbRef::Redis(c) => Ok(c),
        DbRef::MySql(_) => Err(AppError::param("该连接是 MySQL")),
    }
}

#[tauri::command]
pub async fn db_schemas(state: ManagedState<'_>, conn_id: String) -> AppResult<Vec<String>> {
    let pool = mysql_of(&state, &conn_id).await?;
    crate::db::mysql::schemas(&pool).await
}

#[tauri::command]
pub async fn db_tables(
    state: ManagedState<'_>,
    conn_id: String,
    schema: Option<String>,
) -> AppResult<Vec<String>> {
    let pool = mysql_of(&state, &conn_id).await?;
    crate::db::mysql::tables(&pool, schema.as_deref().unwrap_or("public")).await
}

#[tauri::command]
pub async fn db_columns(
    state: ManagedState<'_>,
    conn_id: String,
    schema: Option<String>,
    table: String,
) -> AppResult<serde_json::Value> {
    let pool = mysql_of(&state, &conn_id).await?;
    crate::db::mysql::describe(&pool, schema.as_deref().unwrap_or("public"), &table).await
}

#[tauri::command]
pub async fn db_query(
    state: ManagedState<'_>,
    conn_id: String,
    sql: String,
    limit: Option<u64>,
    timeout_ms: Option<u64>,
) -> AppResult<QueryResult> {
    let _ = limit;
    let pool = mysql_of(&state, &conn_id).await?;
    crate::db::mysql_query(
        &pool,
        &sql,
        std::time::Duration::from_millis(timeout_ms.unwrap_or(30_000)),
    )
    .await
}

#[tauri::command]
pub async fn redis_scan(
    state: ManagedState<'_>,
    conn_id: String,
    cursor: Option<u64>,
    pattern: Option<String>,
    count: Option<u64>,
) -> AppResult<(u64, Vec<String>)> {
    let client = redis_of(&state, &conn_id).await?;
    let mut c = crate::db::redis::conn(&client).await?;
    crate::db::redis::scan(
        &mut c,
        cursor.unwrap_or(0),
        pattern.as_deref().unwrap_or("*"),
        count.unwrap_or(100) as usize,
    )
    .await
}

#[tauri::command]
pub async fn redis_inspect(
    state: ManagedState<'_>,
    conn_id: String,
    key: String,
) -> AppResult<RedisKeyView> {
    let client = redis_of(&state, &conn_id).await?;
    let mut c = crate::db::redis::conn(&client).await?;
    crate::db::redis::inspect(&mut c, &key).await
}

#[tauri::command]
pub async fn redis_command(
    state: ManagedState<'_>,
    conn_id: String,
    args: Vec<String>,
) -> AppResult<String> {
    let client = redis_of(&state, &conn_id).await?;
    let mut c = crate::db::redis::conn(&client).await?;
    crate::db::redis::raw_command(&mut c, &args).await
}

#[tauri::command]
pub async fn redis_set_ttl(
    state: ManagedState<'_>,
    conn_id: String,
    key: String,
    seconds: i64,
) -> AppResult<()> {
    let client = redis_of(&state, &conn_id).await?;
    let mut c = crate::db::redis::conn(&client).await?;
    crate::db::redis::set_ttl(&mut c, &key, seconds).await
}

#[allow(dead_code)]
fn _keep_imports(_: HashMap<String, Arc<()>>) {}

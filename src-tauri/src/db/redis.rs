//! Redis（redis-rs）：SCAN 分页、类型感知查看、命令台、TTL。

use crate::error::{AppError, AppResult};

type Conn = ::redis::aio::MultiplexedConnection;

/// 建立连接。
pub async fn connect(url: &str) -> AppResult<::redis::Client> {
    let client =
        redis::Client::open(url).map_err(|e| AppError::Internal(format!("Redis URL 无效: {e}")))?;
    let mut test = client
        .get_multiplexed_async_connection()
        .await
        .map_err(|e| AppError::Internal(format!("Redis 连接失败: {e}")))?;
    let pong: String = redis::cmd("PING")
        .query_async(&mut test)
        .await
        .map_err(|e| AppError::Internal(format!("Redis PING 失败: {e}")))?;
    let _ = pong;
    Ok(client)
}

/// 拿一个复用连接。
pub async fn conn(client: &::redis::Client) -> AppResult<Conn> {
    client
        .get_multiplexed_async_connection()
        .await
        .map_err(|e| AppError::Internal(format!("Redis 连接失败: {e}")))
}

/// SCAN 分页（避免 KEYS 阻塞）。
pub async fn scan(
    conn: &mut Conn,
    cursor: u64,
    pattern: &str,
    count: usize,
) -> AppResult<(u64, Vec<String>)> {
    let (next, keys): (u64, Vec<String>) = redis::cmd("SCAN")
        .arg(cursor)
        .arg("MATCH")
        .arg(pattern)
        .arg("COUNT")
        .arg(count)
        .query_async(conn)
        .await
        .map_err(|e| AppError::Internal(format!("SCAN 失败: {e}")))?;
    Ok((next, keys))
}

/// 键类型。
pub async fn key_type(conn: &mut Conn, key: &str) -> AppResult<String> {
    let t: String = redis::cmd("TYPE")
        .arg(key)
        .query_async(conn)
        .await
        .map_err(|e| AppError::Internal(e.to_string()))?;
    Ok(t)
}

/// TTL（秒）。
pub async fn ttl(conn: &mut Conn, key: &str) -> AppResult<i64> {
    let t: i64 = redis::cmd("TTL")
        .arg(key)
        .query_async(conn)
        .await
        .map_err(|e| AppError::Internal(e.to_string()))?;
    Ok(t)
}

/// 类型感知读取键值。
pub async fn inspect(conn: &mut Conn, key: &str) -> AppResult<super::RedisKeyView> {
    let key_type = key_type(conn, key).await?;
    let ttl_secs = ttl(conn, key).await?;
    let value: serde_json::Value = match key_type.as_str() {
        "string" => {
            let v: Option<String> = redis::cmd("GET")
                .arg(key)
                .query_async(conn)
                .await
                .map_err(|e| AppError::Internal(e.to_string()))?;
            serde_json::Value::String(v.unwrap_or_default())
        }
        "hash" => {
            let v: Vec<(String, String)> = redis::cmd("HGETALL")
                .arg(key)
                .query_async(conn)
                .await
                .map_err(|e| AppError::Internal(e.to_string()))?;
            serde_json::Value::Object(
                v.into_iter()
                    .map(|(k, val)| (k, serde_json::Value::String(val)))
                    .collect(),
            )
        }
        "list" => {
            let v: Vec<String> = redis::cmd("LRANGE")
                .arg(key)
                .arg(0)
                .arg(99)
                .query_async(conn)
                .await
                .map_err(|e| AppError::Internal(e.to_string()))?;
            serde_json::json!(v)
        }
        "set" => {
            let v: Vec<String> = redis::cmd("SMEMBERS")
                .arg(key)
                .query_async(conn)
                .await
                .map_err(|e| AppError::Internal(e.to_string()))?;
            serde_json::json!(v)
        }
        "zset" => {
            let v: Vec<(String, f64)> = redis::cmd("ZRANGE")
                .arg(key)
                .arg(0)
                .arg(99)
                .arg("WITHSCORES")
                .query_async(conn)
                .await
                .map_err(|e| AppError::Internal(e.to_string()))?;
            serde_json::json!(v
                .into_iter()
                .map(|(m, s)| serde_json::json!({"member": m, "score": s}))
                .collect::<Vec<_>>())
        }
        "stream" => {
            let v: Vec<String> = redis::cmd("XRANGE")
                .arg(key)
                .arg("-")
                .arg("+")
                .arg("COUNT")
                .arg(50)
                .query_async(conn)
                .await
                .map_err(|e| AppError::Internal(e.to_string()))?;
            serde_json::json!(v)
        }
        "none" => serde_json::Value::Null,
        other => serde_json::Value::String(format!("<{other}>")),
    };
    Ok(super::RedisKeyView {
        key: key.to_string(),
        key_type,
        ttl: ttl_secs,
        value,
    })
}

/// 设置 TTL。
pub async fn set_ttl(conn: &mut Conn, key: &str, seconds: i64) -> AppResult<()> {
    if seconds < 0 {
        let n: i64 = ::redis::cmd("PERSIST")
            .arg(key)
            .query_async(conn)
            .await
            .map_err(|e| AppError::Internal(e.to_string()))?;
        let _ = n;
    } else {
        let n: i64 = ::redis::cmd("EXPIRE")
            .arg(key)
            .arg(seconds)
            .query_async(conn)
            .await
            .map_err(|e| AppError::Internal(e.to_string()))?;
        let _ = n;
    }
    Ok(())
}

/// 自由命令台（护栏在 guard.rs 分级）。
pub async fn raw_command(conn: &mut Conn, args: &[String]) -> AppResult<String> {
    if args.is_empty() {
        return Err(AppError::param("Redis 命令为空"));
    }
    let mut real = ::redis::cmd(args[0].as_str());
    for a in &args[1..] {
        real.arg(a.as_str());
    }
    let v: redis::Value = real
        .query_async(conn)
        .await
        .map_err(|e| AppError::Internal(format!("Redis 命令失败: {e}")))?;
    Ok(format_redis_value(&v))
}

/// 渲染 Redis 回复。
pub fn format_redis_value(v: &redis::Value) -> String {
    match v {
        redis::Value::Nil => "(nil)".into(),
        redis::Value::Int(i) => i.to_string(),
        redis::Value::BulkString(b) => String::from_utf8_lossy(b).into_owned(),
        redis::Value::SimpleString(s) => s.clone(),
        redis::Value::Okay => "OK".into(),
        redis::Value::Array(items) => items
            .iter()
            .enumerate()
            .map(|(i, item)| format!("{}) {}", i + 1, format_redis_value(item)))
            .collect::<Vec<_>>()
            .join("\n"),
        redis::Value::Map(_) => "<map>".into(),
        redis::Value::Double(d) => d.to_string(),
        redis::Value::Boolean(b) => b.to_string(),
        redis::Value::BigNumber(b) => b.to_string(),
        redis::Value::VerbatimString { text, .. } => text.clone(),
        _ => format!("{v:?}"),
    }
}

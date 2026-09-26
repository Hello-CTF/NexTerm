//! 数据库类工具（§8.2 db.rs）。

use serde_json::json;

use crate::db::DbRef;
use crate::state::AppState;

use super::{scoped_db, ToolOutput};

fn arg_str<'a>(args: &'a serde_json::Value, key: &str) -> Option<&'a str> {
    args.get(key).and_then(|v| v.as_str())
}

fn s(name: &str, desc: &str, params: serde_json::Value) -> crate::ai::provider::ToolSchema {
    crate::ai::provider::ToolSchema {
        name: name.into(),
        description: desc.into(),
        parameters: params,
    }
}

pub fn schemas() -> Vec<crate::ai::provider::ToolSchema> {
    vec![
        s(
            "db_list_tables",
            "列出当前数据库连接的表。",
            json!({"type":"object","properties":{"schema":{"type":"string"}}}),
        ),
        s(
            "db_describe",
            "查看表结构（字段/索引）。",
            json!({"type":"object","properties":{
            "schema":{"type":"string"},"table":{"type":"string"},
          },"required":["table"]}),
        ),
        s(
            "db_query",
            "执行只读 SQL（写语句会被拒绝）。",
            json!({"type":"object","properties":{"sql":{"type":"string"}},"required":["sql"]}),
        ),
        s(
            "redis_scan",
            "Redis SCAN 浏览键。",
            json!({"type":"object","properties":{
              "pattern":{"type":"string"},"count":{"type":"number"},
            }}),
        ),
    ]
}

pub async fn db_list_tables(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let conn = match scoped_db(state, scope).await {
        Ok(c) => c,
        Err(e) => return e,
    };
    match conn {
        DbRef::MySql(pool) => {
            let schema = arg_str(args, "schema").unwrap_or("public");
            match crate::db::mysql::tables(&pool, schema).await {
                Ok(t) => ToolOutput::ok(if t.is_empty() {
                    "无表".into()
                } else {
                    t.join("\n")
                }),
                Err(e) => ToolOutput::fail(format!("查表失败: {e}")),
            }
        }
        DbRef::Redis(_) => ToolOutput::fail("Redis 没有表概念，用 redis_scan"),
    }
}

pub async fn db_describe(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let Some(table) = arg_str(args, "table") else {
        return ToolOutput::fail("缺少 table");
    };
    let schema = arg_str(args, "schema").unwrap_or("public");
    let conn = match scoped_db(state, scope).await {
        Ok(c) => c,
        Err(e) => return e,
    };
    match conn {
        DbRef::MySql(pool) => match crate::db::mysql::describe(&pool, schema, table).await {
            Ok(d) => ToolOutput::ok(serde_json::to_string_pretty(&d).unwrap_or_default()),
            Err(e) => ToolOutput::fail(format!("describe 失败: {e}")),
        },
        DbRef::Redis(_) => ToolOutput::fail("Redis 不支持 describe"),
    }
}

pub async fn db_query(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let Some(sql) = arg_str(args, "sql") else {
        return ToolOutput::fail("缺少 sql");
    };
    // 护栏前置：AI 的 db_query 只放行只读 SQL（§8.2：只读校验）
    let verdict = crate::ai::guard::classify_sql(sql);
    if verdict.risk != crate::ai::guard::Risk::Safe {
        return ToolOutput::fail(format!(
            "该 SQL 不是只读（{}），已被拒绝。如需写操作请明确告知用户并在界面确认。",
            verdict.reason
        ));
    }
    let conn = match scoped_db(state, scope).await {
        Ok(c) => c,
        Err(e) => return e,
    };
    match conn {
        DbRef::MySql(pool) => {
            match crate::db::mysql_query(&pool, sql, std::time::Duration::from_secs(30)).await {
                Ok(r) => {
                    if let Some(err) = r.error {
                        return ToolOutput::fail(format!("SQL 错误: {err}"));
                    }
                    let mut text = String::new();
                    if !r.columns.is_empty() {
                        text.push_str(&r.columns.join(" | "));
                        text.push('\n');
                    }
                    for row in r.rows.iter().take(50) {
                        let cells: Vec<String> = row
                            .iter()
                            .map(|c| serde_json::to_string(c).unwrap_or_default())
                            .collect();
                        text.push_str(&cells.join(" | "));
                        text.push('\n');
                    }
                    if r.truncated {
                        text.push_str(&format!("[已截断，仅显示前 {} 行]\n", r.rows.len()));
                    }
                    ToolOutput::ok(text)
                }
                Err(e) => ToolOutput::fail(format!("查询失败: {e}")),
            }
        }
        DbRef::Redis(_) => ToolOutput::fail("Redis 用 redis_scan / redis_command"),
    }
}

pub async fn redis_scan_tool(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let pattern = arg_str(args, "pattern").unwrap_or("*");
    let count = args
        .get("count")
        .and_then(|v| v.as_u64())
        .unwrap_or(100)
        .min(1000) as usize;
    let conn = match scoped_db(state, scope).await {
        Ok(c) => c,
        Err(e) => return e,
    };
    let DbRef::Redis(client) = conn else {
        return ToolOutput::fail("该连接不是 Redis");
    };
    let mut c = match crate::db::redis::conn(&client).await {
        Ok(c) => c,
        Err(e) => return ToolOutput::fail(e.to_string()),
    };
    match crate::db::redis::scan(&mut c, 0, pattern, count).await {
        Ok((cursor, keys)) => {
            ToolOutput::ok(format!("cursor={} keys:\n{}", cursor, keys.join("\n")))
        }
        Err(e) => ToolOutput::fail(format!("SCAN 失败: {e}")),
    }
}

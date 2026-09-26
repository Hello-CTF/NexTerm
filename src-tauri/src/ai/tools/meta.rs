//! 元工具（§8.2 meta.rs）：list_assets / ask_user。

use serde_json::json;

use crate::state::AppState;

use super::ToolOutput;

pub fn schemas() -> Vec<crate::ai::provider::ToolSchema> {
    vec![
        crate::ai::provider::ToolSchema {
            name: "list_assets".into(),
            description: "列出用户所有资产（服务器/数据库/容器主机），用于跨主机编排。".into(),
            parameters: json!({"type":"object","properties":{}}),
        },
        crate::ai::provider::ToolSchema {
            name: "ask_user".into(),
            description: "向用户提问（可带选项）。信息不足时必须问，不要瞎猜。".into(),
            parameters: json!({"type":"object","properties":{
                "question":{"type":"string"},
                "options":{"type":"array","items":{"type":"string"}},
            },"required":["question"]}),
        },
    ]
}

pub async fn list_assets(state: &AppState) -> ToolOutput {
    match state.store.asset_list(false).await {
        Ok(list) => {
            let lines: Vec<String> = list
                .iter()
                .map(|a| {
                    format!(
                        "{} [{}] {} {}",
                        a.name,
                        a.kind,
                        a.host.clone().unwrap_or_default(),
                        a.username.clone().unwrap_or_default()
                    )
                })
                .collect();
            ToolOutput::ok(if lines.is_empty() {
                "没有资产".into()
            } else {
                lines.join("\n")
            })
        }
        Err(e) => ToolOutput::fail(format!("列资产失败: {e}")),
    }
}

/// ask_user：把问题作为“需要用户输入”的结果返回（agent 结束本轮并等待用户）。
pub fn ask_user(args: &serde_json::Value) -> ToolOutput {
    let question = args
        .get("question")
        .and_then(|v| v.as_str())
        .unwrap_or("(无问题内容)");
    let options: Vec<String> = args
        .get("options")
        .and_then(|v| v.as_array())
        .map(|a| {
            a.iter()
                .filter_map(|x| x.as_str().map(String::from))
                .collect()
        })
        .unwrap_or_default();
    ToolOutput {
        ok: true,
        text: if options.is_empty() {
            format!("（等待用户回答）{question}")
        } else {
            format!("（等待用户回答）{question}\n选项：{}", options.join(" / "))
        },
        exit_code: Some(0),
        truncated: false,
    }
}

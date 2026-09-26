//! Docker 类工具（§8.2 docker.rs）。

use serde_json::json;

use crate::state::AppState;

use super::{scoped_transport, ToolOutput};

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
            "docker_ps",
            "列出目标主机 Docker 容器（结构化）。",
            json!({"type":"object","properties":{}}),
        ),
        s(
            "docker_logs",
            "取容器日志（tail/grep）。",
            json!({"type":"object","properties":{
            "container_id":{"type":"string"},"tail":{"type":"number"},"grep":{"type":"string"},
          },"required":["container_id"]}),
        ),
        s(
            "docker_exec",
            "在容器内执行命令。",
            json!({"type":"object","properties":{
            "container_id":{"type":"string"},"cmd":{"type":"string"},
          },"required":["container_id","cmd"]}),
        ),
        s(
            "docker_control",
            "容器生命周期操作（start/stop/restart/rm，需确认）。",
            json!({"type":"object","properties":{
            "container_id":{"type":"string"},
            "action":{"type":"string","enum":["start","stop","restart","rm"]},
          },"required":["container_id","action"]}),
        ),
    ]
}

pub async fn docker_ps(state: &AppState, scope: &crate::ai::AiScope) -> ToolOutput {
    let transport = match scoped_transport(state, scope).await {
        Ok(t) => t,
        Err(e) => return e,
    };
    match crate::docker::cli::ps(&*transport).await {
        Ok(list) => {
            let lines: Vec<String> = list
                .iter()
                .map(|c| {
                    format!(
                        "{} [{}] {} ({}) {}",
                        c.name, c.state, c.image, c.status, c.ports
                    )
                })
                .collect();
            ToolOutput::ok(if lines.is_empty() {
                "没有容器".into()
            } else {
                lines.join("\n")
            })
        }
        Err(e) => ToolOutput::fail(format!("docker ps 失败: {e}")),
    }
}

pub async fn docker_logs(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let Some(container) = arg_str(args, "container_id") else {
        return ToolOutput::fail("缺少 container_id");
    };
    let tail = args
        .get("tail")
        .and_then(|v| v.as_u64())
        .unwrap_or(200)
        .min(5000);
    let grep = arg_str(args, "grep");
    let transport = match scoped_transport(state, scope).await {
        Ok(t) => t,
        Err(e) => return e,
    };
    match crate::docker::cli::logs(&*transport, container, tail, grep).await {
        Ok(out) => ToolOutput::ok(if out.trim().is_empty() {
            "日志为空".into()
        } else {
            out
        }),
        Err(e) => ToolOutput::fail(format!("取日志失败: {e}")),
    }
}

pub async fn docker_exec(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let (Some(container), Some(cmd)) = (arg_str(args, "container_id"), arg_str(args, "cmd")) else {
        return ToolOutput::fail("缺少 container_id/cmd");
    };
    let transport = match scoped_transport(state, scope).await {
        Ok(t) => t,
        Err(e) => return e,
    };
    super::echo_to_tab(state, scope, &format!("docker exec {container} {cmd}")).await;
    match crate::docker::cli::exec_in_container(
        &*transport,
        container,
        cmd,
        std::time::Duration::from_secs(60),
    )
    .await
    {
        Ok(out) => ToolOutput::ok(if out.trim().is_empty() {
            "(无输出)".into()
        } else {
            out
        }),
        Err(e) => ToolOutput::fail(format!("容器内执行失败: {e}")),
    }
}

pub async fn docker_control(
    state: &AppState,
    scope: &crate::ai::AiScope,
    args: &serde_json::Value,
) -> ToolOutput {
    let (Some(container), Some(action)) = (arg_str(args, "container_id"), arg_str(args, "action"))
    else {
        return ToolOutput::fail("缺少 container_id/action");
    };
    let transport = match scoped_transport(state, scope).await {
        Ok(t) => t,
        Err(e) => return e,
    };
    let result = match action {
        "rm" => crate::docker::cli::remove_container(&*transport, container, false).await,
        other => crate::docker::cli::action(&*transport, container, other).await,
    };
    match result {
        Ok(()) => {
            let _ = state
                .store
                .audit_insert(crate::store::AuditInput {
                    session_id: scope.session_id.clone(),
                    asset_id: scope.asset_id.clone(),
                    source: "ai",
                    kind: "docker_action",
                    payload: json!({ "container": container, "action": action }),
                    exit_code: Some(0),
                    duration_ms: None,
                })
                .await;
            ToolOutput::ok(format!("{action} {container} 完成"))
        }
        Err(e) => ToolOutput::fail(format!("{action} 失败: {e}")),
    }
}

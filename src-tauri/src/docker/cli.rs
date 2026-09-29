//! docker CLI 封装与 JSON 解析。

use std::time::Duration;

use crate::error::{AppError, AppResult};
use crate::transport::Transport;

use super::{ContainerSummary, HostStats, ImageSummary};

/// 在会话上执行 docker 命令（幂等只读走 30s 超时）。
pub async fn docker_exec(
    transport: &dyn Transport,
    args: &str,
    timeout: Duration,
) -> AppResult<String> {
    let cmd = format!("docker {args}");
    let out = transport.exec(&cmd, timeout).await?;
    // 传输层不再按行裁剪，这是唯一还能让结果不完整的地方（原始字节熔断，SSH 8MB）。
    // 真触到了要留痕 —— 否则又是「UI 显示 400、实机 569」那种静默少数据。
    if out.truncated {
        tracing::warn!(
            target: "docker",
            "输出触发原始字节熔断，结果可能不完整: {cmd}"
        );
    }
    if let Some(code) = out.exit_code {
        if code != 0 && out.stdout.trim().is_empty() {
            return Err(AppError::Internal(format!(
                "docker 命令失败（{}）: {}",
                code,
                out.stderr.trim()
            )));
        }
    }
    Ok(out.stdout)
}

/// `docker ps -a` → 结构化容器列表。
pub async fn ps(transport: &dyn Transport) -> AppResult<Vec<ContainerSummary>> {
    let out = docker_exec(
        transport,
        "ps -a --format '{{json .}}'",
        Duration::from_secs(30),
    )
    .await?;
    let mut list = Vec::new();
    for line in out.lines() {
        let line = line.trim();
        if line.is_empty() || !line.starts_with('{') {
            continue;
        }
        let v: serde_json::Value = match serde_json::from_str(line) {
            Ok(v) => v,
            Err(_) => continue,
        };
        list.push(ContainerSummary {
            id: v
                .get("ID")
                .and_then(|s| s.as_str())
                .unwrap_or("")
                .to_string(),
            name: v
                .get("Names")
                .and_then(|s| s.as_str())
                .unwrap_or("")
                .to_string(),
            image: v
                .get("Image")
                .and_then(|s| s.as_str())
                .unwrap_or("")
                .to_string(),
            state: v
                .get("State")
                .and_then(|s| s.as_str())
                .unwrap_or("")
                .to_string(),
            status: v
                .get("Status")
                .and_then(|s| s.as_str())
                .unwrap_or("")
                .to_string(),
            ports: v
                .get("Ports")
                .and_then(|s| s.as_str())
                .unwrap_or("")
                .to_string(),
            compose_project: v.get("Labels").and_then(|s| s.as_str()).and_then(|labels| {
                labels.split(',').find_map(|kv| {
                    kv.strip_prefix("com.docker.compose.project=")
                        .map(|s| s.to_string())
                })
            }),
        });
    }
    Ok(list)
}

/// `docker images` → 镜像列表。
pub async fn images(transport: &dyn Transport) -> AppResult<Vec<ImageSummary>> {
    let out = docker_exec(
        transport,
        "images --format '{{json .}}'",
        Duration::from_secs(30),
    )
    .await?;
    let mut list = Vec::new();
    for line in out.lines() {
        let line = line.trim();
        if !line.starts_with('{') {
            continue;
        }
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(line) {
            list.push(ImageSummary {
                id: v
                    .get("ID")
                    .and_then(|s| s.as_str())
                    .unwrap_or("")
                    .to_string(),
                repository: v
                    .get("Repository")
                    .and_then(|s| s.as_str())
                    .unwrap_or("")
                    .to_string(),
                tag: v
                    .get("Tag")
                    .and_then(|s| s.as_str())
                    .unwrap_or("")
                    .to_string(),
                size: v
                    .get("Size")
                    .and_then(|s| s.as_str())
                    .unwrap_or("")
                    .to_string(),
                created_since: v
                    .get("CreatedSince")
                    .and_then(|s| s.as_str())
                    .unwrap_or("")
                    .to_string(),
            });
        }
    }
    Ok(list)
}

/// `docker stats --no-stream` 原始表（UI 展示）。
pub async fn stats(transport: &dyn Transport) -> AppResult<String> {
    docker_exec(
        transport,
        "stats --no-stream --format '{{json .}}'",
        Duration::from_secs(30),
    )
    .await
}

/// 主机概览。
pub async fn overview(transport: &dyn Transport) -> AppResult<HostStats> {
    let ps_out = ps(transport).await?;
    let images_out = images(transport).await?;
    Ok(HostStats {
        containers_running: ps_out.iter().filter(|c| c.state == "running").count() as u64,
        containers_total: ps_out.len() as u64,
        images: images_out.len() as u64,
    })
}

/// 容器日志（tail N；grep 过滤在内核侧做）。
pub async fn logs(
    transport: &dyn Transport,
    container: &str,
    tail: u64,
    grep: Option<&str>,
) -> AppResult<String> {
    let out = docker_exec(
        transport,
        &format!("logs --tail {tail} {container}"),
        Duration::from_secs(60),
    )
    .await?;
    match grep {
        Some(g) => {
            let lines: Vec<&str> = out.lines().filter(|l| l.contains(g)).collect();
            Ok(lines.join("\n"))
        }
        None => Ok(out),
    }
}

/// 容器内执行。
pub async fn exec_in_container(
    transport: &dyn Transport,
    container: &str,
    cmd: &str,
    timeout: Duration,
) -> AppResult<String> {
    docker_exec(
        transport,
        &format!("exec {container} sh -c {cmd:?}"),
        timeout,
    )
    .await
}

/// 生命周期动作。
pub async fn action(transport: &dyn Transport, container: &str, action: &str) -> AppResult<()> {
    let allowed = ["start", "stop", "restart", "pause", "unpause", "kill"];
    if !allowed.contains(&action) {
        return Err(AppError::param(format!("不支持的动作 {action}")));
    }
    docker_exec(
        transport,
        &format!("{action} {container}"),
        Duration::from_secs(60),
    )
    .await?;
    Ok(())
}

/// 删除容器（rm）。
pub async fn remove_container(
    transport: &dyn Transport,
    container: &str,
    force: bool,
) -> AppResult<()> {
    let f = if force { " -f" } else { "" };
    docker_exec(
        transport,
        &format!("rm{f} {container}"),
        Duration::from_secs(60),
    )
    .await?;
    Ok(())
}

/// 拉取镜像。
pub async fn pull(transport: &dyn Transport, image: &str) -> AppResult<String> {
    docker_exec(
        transport,
        &format!("pull {image}"),
        Duration::from_secs(600),
    )
    .await
}

/// 删除镜像。
pub async fn remove_image(transport: &dyn Transport, image: &str, force: bool) -> AppResult<()> {
    let f = if force { " -f" } else { "" };
    docker_exec(
        transport,
        &format!("rmi{f} {image}"),
        Duration::from_secs(60),
    )
    .await?;
    Ok(())
}

/// inspect JSON。
pub async fn inspect(transport: &dyn Transport, container: &str) -> AppResult<serde_json::Value> {
    let out = docker_exec(
        transport,
        &format!("inspect {container}"),
        Duration::from_secs(30),
    )
    .await?;
    serde_json::from_str(out.trim())
        .map_err(|e| AppError::Internal(format!("inspect 解析失败: {e}")))
}

/// 容器文件浏览（exec + tar 封装）：列目录。
pub async fn container_list_dir(
    transport: &dyn Transport,
    container: &str,
    path: &str,
) -> AppResult<Vec<String>> {
    let out = exec_in_container(
        transport,
        container,
        &format!("ls -1a {}", path),
        Duration::from_secs(30),
    )
    .await?;
    Ok(out
        .lines()
        .map(|l| l.trim().to_string())
        .filter(|l| !l.is_empty())
        .collect())
}

#[cfg(test)]
mod tests {

    #[test]
    fn parse_ps_json_lines() {
        let line = r#"{"Command":"\"nginx -g 'daemon of","CreatedAt":"2026-01-01","ID":"abc123","Image":"nginx","Labels":"com.docker.compose.project=web","LocalVolumes":"0","Mounts":"","Names":"web-1","Networks":"bridge","Ports":"0.0.0.0:80->80/tcp","RunningFor":"2 days","Size":"0B","State":"running","Status":"Up 2 days"}"#;
        let transport_result = serde_json::from_str::<serde_json::Value>(line);
        assert!(transport_result.is_ok());
        let v = transport_result.unwrap();
        assert_eq!(v.get("Names").unwrap().as_str(), Some("web-1"));
        assert_eq!(v.get("State").unwrap().as_str(), Some("running"));
    }
}

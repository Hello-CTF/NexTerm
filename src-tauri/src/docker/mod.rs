//! Docker 管理（M3）：CLI 通道为主（§1 技术选型）——在已连会话上执行
//! `docker` 命令并解析 `--format '{{json .}}'` 输出；零额外连接。

pub mod cli;

use serde::Serialize;

/// 容器摘要（docker ps 解析）。
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ContainerSummary {
    pub id: String,
    pub name: String,
    pub image: String,
    pub state: String,
    pub status: String,
    pub ports: String,
    pub compose_project: Option<String>,
}

/// 镜像摘要。
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ImageSummary {
    pub id: String,
    pub repository: String,
    pub tag: String,
    pub size: String,
    pub created_since: String,
}

/// 主机水位（docker stats --no-stream 单行）。
#[derive(Debug, Clone, Serialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct HostStats {
    pub containers_running: u64,
    pub containers_total: u64,
    pub images: u64,
}

/// Docker 操作动作。
#[derive(Debug, Clone, Copy)]
pub enum DockerAction {
    Start,
    Stop,
    Restart,
    Pause,
    Unpause,
    Remove,
    Rename(&'static str),
}

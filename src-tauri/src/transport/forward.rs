//! 端口转发 / 隧道（M3-T8）：本地转发（SSH direct-tcpip）。
//!
//! 用途：复用 SSH 会话打通 MySQL/Redis 等内网服务，零额外连接。

use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::Arc;

use crate::error::{AppError, AppResult};

#[derive(Debug, Clone, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ForwardSpec {
    pub id: String,
    pub session_id: String,
    /// 本地监听端口。
    pub listen_port: u16,
    pub target_host: String,
    pub target_port: u16,
    /// local（v1 仅本地转发）。
    pub kind: String,
    pub created_at: u64,
}

/// 运行中的转发。
pub struct RunningForward {
    pub spec: ForwardSpec,
    stop: Arc<tokio_util::sync::CancellationToken>,
    pub connections: Arc<AtomicU64>,
    active: Arc<AtomicBool>,
}

impl RunningForward {
    pub fn is_active(&self) -> bool {
        self.active.load(Ordering::Relaxed)
    }

    pub fn stop(&self) {
        self.stop.cancel();
        self.active.store(false, Ordering::Relaxed);
    }
}

/// 启动一条本地转发：`本地 listen_port → SSH → target`。
pub async fn spawn_local_forward(
    session_id: &str,
    ssh: Arc<crate::transport::ssh::SshTransport>,
    listen_port: u16,
    target_host: String,
    target_port: u16,
) -> AppResult<RunningForward> {
    let listener = tokio::net::TcpListener::bind(("127.0.0.1", listen_port))
        .await
        .map_err(|e| AppError::param(format!("端口 {listen_port} 监听失败: {e}")))?;
    let stop = Arc::new(tokio_util::sync::CancellationToken::new());
    let connections = Arc::new(AtomicU64::new(0));
    let active = Arc::new(AtomicBool::new(true));
    let spec = ForwardSpec {
        id: crate::ids::new_id(),
        session_id: session_id.to_string(),
        listen_port,
        target_host: target_host.clone(),
        target_port,
        kind: "local".into(),
        created_at: crate::ids::now_ms(),
    };

    let stop2 = Arc::clone(&stop);
    let conns = Arc::clone(&connections);
    let active2 = Arc::clone(&active);
    let sid = spec.id.clone();
    tokio::spawn(async move {
        loop {
            tokio::select! {
                _ = stop2.cancelled() => break,
                accepted = listener.accept() => {
                    match accepted {
                        Ok((mut local, addr)) => {
                            let channel = match ssh
                                .open_direct_tcpip(
                                    &target_host,
                                    target_port,
                                    addr.ip(),
                                    addr.port(),
                                )
                                .await
                            {
                                Ok(ch) => ch,
                                Err(e) => {
                                    tracing::warn!(target: "forward", fwd = %sid, error = %e, "direct-tcpip 打开失败");
                                    continue;
                                }
                            };
                            let mut remote = channel.into_stream();
                            conns.fetch_add(1, Ordering::Relaxed);
                            tokio::spawn(async move {
                                let _ = tokio::io::copy_bidirectional(&mut local, &mut remote).await;
                            });
                        }
                        Err(_) => break,
                    }
                }
            }
        }
        active2.store(false, Ordering::Relaxed);
    });

    Ok(RunningForward {
        spec,
        stop,
        connections,
        active,
    })
}

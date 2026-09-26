//! 上下文自动装配（§8.5 / §6.4）：会话 + 终端 + 侦察快照，按预算裁剪。

use serde::Serialize;

use crate::state::AppState;
use crate::transport::Transport;

/// 上下文包（§8.5）。
#[derive(Debug, Clone, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ContextBundle {
    pub session: Option<SessionBrief>,
    pub screen: Option<String>,
    pub tail: Vec<String>,
    pub recon: Option<String>,
    pub selection: Option<String>,
    pub tables: Option<Vec<TableBrief>>,
    pub containers: Option<Vec<String>>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionBrief {
    pub name: String,
    pub kind: String,
    pub host: Option<String>,
    pub username: Option<String>,
    pub cwd: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TableBrief {
    pub schema: String,
    pub name: String,
}

/// 总预算（字符近似，32k tokens ≈ 96k 字符——中文按 1 字 1 token 计从严取 64k）。
const BUDGET_CHARS: usize = 64 * 1024;
/// tail 默认行数（§8.5：最近 80 行）。
const TAIL_LINES: usize = 80;

/// 装配上下文（按需注入 + 超预算按优先级裁剪：screen/tail > recon > selection）。
pub async fn build(
    state: &AppState,
    scope: &crate::ai::AiScope,
    selection: Option<String>,
) -> ContextBundle {
    let mut bundle = ContextBundle {
        selection,
        ..Default::default()
    };

    if let Some(sid) = &scope.session_id {
        if let Ok(session) = state.sessions.get(sid).await {
            let transport = session.transport().await;
            bundle.session = Some(SessionBrief {
                name: session.name.clone(),
                kind: session.kind.clone(),
                host: asset_host(state, scope.asset_id.as_deref()).await,
                username: None,
                cwd: transport.cwd(),
            });
            bundle.recon = Some(recon_cached(state, sid, &*transport).await);

            if let Some(tid) = &scope.tab_id {
                if let Ok(tab) = state.sessions.get_tab(tid).await {
                    let snap = tab.snapshot();
                    bundle.screen = Some(snap.text);
                    bundle.tail = tab.tail_lines(TAIL_LINES);
                }
            }
        }
    }

    trim_to_budget(&mut bundle);
    bundle
}

/// 渲染为 system 附加文本。
pub fn render(bundle: &ContextBundle) -> String {
    let mut out = String::new();
    if let Some(s) = &bundle.session {
        out.push_str("[会话上下文]\n");
        out.push_str(&format!("- 连接：{}（{}）\n", s.name, s.kind));
        if let Some(h) = &s.host {
            out.push_str(&format!("- 主机：{h}\n"));
        }
        if let Some(c) = &s.cwd {
            out.push_str(&format!("- 当前目录：{c}\n"));
        }
    }
    if !bundle.tail.is_empty() {
        out.push_str("[终端最近输出]\n");
        let joined = bundle.tail.join("\n");
        let cut: String = joined
            .chars()
            .rev()
            .take(8000)
            .collect::<Vec<_>>()
            .into_iter()
            .rev()
            .collect();
        out.push_str(&cut);
        out.push('\n');
    }
    if let Some(r) = &bundle.recon {
        if !r.is_empty() {
            out.push_str("[环境侦察快照]\n");
            let cut: String = r.chars().take(12_000).collect();
            out.push_str(&cut);
            out.push('\n');
        }
    }
    if let Some(sel) = &bundle.selection {
        if !sel.is_empty() {
            out.push_str("[用户选中的文本]\n");
            out.push_str(sel);
            out.push('\n');
        }
    }
    out
}

/// 确定性侦察快照（§8.5）：连接时采集、缓存，不消耗模型轮次。
/// Linux 集合；失败命令静默跳过。
pub async fn recon_snapshot(transport: &dyn Transport) -> String {
    const RECON: &[&str] = &[
        "hostname; uname -a; whoami; uptime",
        "df -h | head -15",
        "free -m 2>/dev/null | head -5",
        "ss -lntp 2>/dev/null | head -25 || netstat -lntp 2>/dev/null | head -25",
        "systemctl --failed --no-pager 2>/dev/null | head -15",
        "docker ps --format '{{.Names}} {{.Status}}' 2>/dev/null | head -20",
    ];
    let mut out = String::new();
    for cmd in RECON {
        match transport.exec(cmd, std::time::Duration::from_secs(8)).await {
            Ok(r) => {
                out.push_str(&format!("$ {cmd}\n{}\n", r.stdout.trim_end()));
            }
            Err(e) => {
                out.push_str(&format!("$ {cmd}\n<侦察命令失败: {e}>\n"));
            }
        }
    }
    out
}

/// 侦察结果按会话缓存（60s TTL）。
async fn recon_cached(state: &AppState, session_id: &str, transport: &dyn Transport) -> String {
    let now = crate::ids::now_ms();
    {
        let cache = state.recon_cache.lock().unwrap_or_else(|e| e.into_inner());
        if let Some((ts, text)) = cache.get(session_id) {
            if now.saturating_sub(*ts) < 60_000 {
                return text.clone();
            }
        }
    }
    let text = recon_snapshot(transport).await;
    state
        .recon_cache
        .lock()
        .unwrap_or_else(|e| e.into_inner())
        .insert(session_id.to_string(), (now, text.clone()));
    text
}

async fn asset_host(state: &AppState, asset_id: Option<&str>) -> Option<String> {
    let id = asset_id?;
    let asset = state.store.asset_get(id).await.ok()?;
    Some(format!("{}:{}", asset.host?, asset.port.unwrap_or(0)))
}

/// 超预算裁剪：screen/tail > recon > selection（§8.5 优先级）。
fn trim_to_budget(bundle: &mut ContextBundle) {
    let total: usize = bundle.screen.as_ref().map(|s| s.len()).unwrap_or(0)
        + bundle.tail.iter().map(|l| l.len() + 1).sum::<usize>()
        + bundle.recon.as_ref().map(|s| s.len()).unwrap_or(0)
        + bundle.selection.as_ref().map(|s| s.len()).unwrap_or(0);
    if total <= BUDGET_CHARS {
        return;
    }
    if bundle.selection.is_some() {
        bundle.selection = None;
    }
    if total - (bundle.selection.as_ref().map(|s| s.len()).unwrap_or(0)) > BUDGET_CHARS {
        bundle.recon = bundle.recon.take().map(|r| r.chars().take(8_000).collect());
    }
    if bundle.screen.as_ref().map(|s| s.len()).unwrap_or(0) > 16_000 {
        bundle.screen = bundle
            .screen
            .take()
            .map(|s| s.chars().take(16_000).collect());
    }
    if bundle.tail.len() > 40 {
        bundle.tail = bundle.tail.split_off(bundle.tail.len() - 40);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn render_includes_sections() {
        let bundle = ContextBundle {
            session: Some(SessionBrief {
                name: "web-01".into(),
                kind: "ssh".into(),
                host: Some("1.2.3.4:22".into()),
                username: Some("deploy".into()),
                cwd: Some("/data/app".into()),
            }),
            tail: vec!["line1".into(), "line2".into()],
            recon: Some("$ uname\nLinux".into()),
            ..Default::default()
        };
        let s = render(&bundle);
        assert!(s.contains("[会话上下文]"));
        assert!(s.contains("web-01"));
        assert!(s.contains("[环境侦察快照]"));
        assert!(!s.contains("[用户选中的文本]"));
    }

    #[test]
    fn budget_trim_drops_lowest_priority() {
        let mut bundle = ContextBundle {
            screen: Some("x".repeat(20_000)),
            tail: vec!["t".repeat(100); 100],
            recon: Some("r".repeat(30_000)),
            selection: Some("sel".repeat(5_000)),
            ..Default::default()
        };
        trim_to_budget(&mut bundle);
        assert!(bundle.selection.is_none(), "selection 应最先被裁");
    }
}

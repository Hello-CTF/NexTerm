//! 上下文自动装配（§8.5 / §6.4）：会话 + 终端 + 侦察快照，按预算裁剪。

use serde::Serialize;

use crate::ai::provider::ChatMessage;
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

/// 「稳定前缀 + 易变后缀」的拆分结果（P0-5 上下文缓存）。
///
/// prompt cache 的命中的前提是**前缀字节级稳定**：只要最前面那段每次都不一样，
/// 后面再长也没用。而会话目录、终端输出这些东西天然每轮在变，绝不能进前缀。
#[derive(Debug, Clone, Default)]
pub struct SplitPrompt {
    /// 稳定前缀：角色设定 + 工具使用规范，同一进程内每次调用完全相同。
    /// 调用方应把它作为第一条 `system` 消息的内容。
    pub stable: String,
    /// 易变后缀：会话 / 目录 / 终端输出 / 侦察 / 选中文本。
    /// 调用方应把它追加到消息数组**末尾**（真实提问之前），不要放回 system。
    pub volatile: String,
}

impl SplitPrompt {
    /// 易变后缀是否为空（没有会话、没有选中文本等，纯全局提问时为空）。
    pub fn is_volatile_empty(&self) -> bool {
        self.volatile.trim().is_empty()
    }

    /// 把易变后缀包成一条独立的 `user` 消息。
    ///
    /// 建议插在**真实提问那条 user 消息之前**（即数组倒数第二个位置）：这样
    /// 环境上下文与提问贴在一起、容易被模型关联上，同时不污染前面那条稳定前缀。
    /// 为空时返回 `None`，调用方直接跳过即可。
    pub fn volatile_message(&self) -> Option<ChatMessage> {
        if self.is_volatile_empty() {
            None
        } else {
            Some(ChatMessage::user(format!(
                "[环境上下文]\n{}",
                self.volatile
            )))
        }
    }
}

/// 装配上下文并拆成「稳定前缀 / 易变后缀」。
///
/// 等价于旧的 `system_prompt_base() + "\n\n" + render(&build(..))` 那一段：
/// `stable + "\n\n" + volatile` 与旧写法逐字节相同，只是把两段分开交给调用方，
/// 让易变内容可以挪到消息数组尾部、把 system 前缀留住给 prompt cache 命中。
///
/// 与 [`build`] 一样是 async（要读会话 / 侦察快照）；`build()` 的签名与行为保持不变，
/// 本函数内部复用它，旧调用点不会断。
pub async fn build_split(
    state: &AppState,
    scope: &crate::ai::AiScope,
    selection: Option<String>,
) -> SplitPrompt {
    let bundle = build(state, scope, selection).await;
    split_rendered(&bundle)
}

/// [`build_split`] 的纯函数内核：不碰 IO，方便单测证明「前缀稳定」这件事。
pub fn split_rendered(bundle: &ContextBundle) -> SplitPrompt {
    SplitPrompt {
        stable: crate::ai::system_prompt_base().to_string(),
        volatile: render(bundle),
    }
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
///
/// 每个命令的输出在这里过一遍 [`cap_text`]：传输层已不做裁剪（结构化消费者
/// 需要完整数据），喂模型这一侧必须自己守预算。容器名单单独放宽到 60 行并附
/// 总数 —— 原来写死 `head -20`，容器一多模型就会基于残缺名单作答却看不出来。
pub async fn recon_snapshot(transport: &dyn Transport) -> String {
    const RECON: &[&str] = &[
        "hostname; uname -a; whoami; uptime",
        "df -h | head -15",
        "free -m 2>/dev/null | head -5",
        "ss -lntp 2>/dev/null | head -25 || netstat -lntp 2>/dev/null | head -25",
        "systemctl --failed --no-pager 2>/dev/null | head -15",
        "echo \"== docker ps（总数 $(docker ps -q 2>/dev/null | wc -l)）==\"; \
         docker ps --format '{{.Names}} {{.Status}}' 2>/dev/null | head -60",
    ];
    let mut out = String::new();
    for cmd in RECON {
        match transport.exec(cmd, std::time::Duration::from_secs(8)).await {
            Ok(r) => {
                let (stdout, capped) = crate::transport::cap_text(&r.stdout);
                out.push_str(&format!("$ {cmd}\n{}\n", stdout.trim_end()));
                if capped {
                    out.push_str("（上一条命令输出超出预算已截断）\n");
                }
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

    #[test]
    fn split_stable_prefix_is_byte_identical() {
        // 两份内容天差地别的上下文包：稳定前缀必须逐字节相同，否则 prompt cache
        // 永远命中不了。这正是这次拆分的全部意义。
        let a = ContextBundle {
            session: Some(SessionBrief {
                name: "web-01".into(),
                kind: "ssh".into(),
                host: Some("1.2.3.4:22".into()),
                username: None,
                cwd: Some("/data/app".into()),
            }),
            tail: vec!["line1".into()],
            ..Default::default()
        };
        let b = ContextBundle {
            session: Some(SessionBrief {
                name: "db-02".into(),
                kind: "ssh".into(),
                host: Some("10.0.0.5:22".into()),
                username: None,
                cwd: Some("/var/lib/mysql".into()),
            }),
            selection: Some("一段选中的文本".into()),
            ..Default::default()
        };
        let sa = split_rendered(&a);
        let sb = split_rendered(&b);
        assert_eq!(sa.stable, sb.stable, "稳定前缀必须与上下文内容无关");
        assert_eq!(sa.stable, crate::ai::system_prompt_base());
        assert_ne!(sa.volatile, sb.volatile, "易变后缀应随上下文变化");
    }

    #[test]
    fn split_reassembles_to_old_layout() {
        // 与旧写法 `system_prompt_base() + "\n\n" + render(bundle)` 完全等价。
        let bundle = ContextBundle {
            tail: vec!["hello".into()],
            recon: Some("$ uname\nLinux".into()),
            ..Default::default()
        };
        let s = split_rendered(&bundle);
        let reassembled = format!("{}\n\n{}", s.stable, s.volatile);
        let old = format!("{}\n\n{}", crate::ai::system_prompt_base(), render(&bundle));
        assert_eq!(reassembled, old);
    }

    #[test]
    fn volatile_message_skips_empty_context() {
        let empty = split_rendered(&ContextBundle::default());
        assert!(empty.is_volatile_empty());
        assert!(empty.volatile_message().is_none());

        let with_ctx = split_rendered(&ContextBundle {
            tail: vec!["x".into()],
            ..Default::default()
        });
        let msg = with_ctx.volatile_message().expect("非空上下文应有消息");
        assert_eq!(msg.role, "user");
        assert!(matches!(
            msg.content,
            serde_json::Value::String(ref t) if t.starts_with("[环境上下文]")
        ));
    }
}

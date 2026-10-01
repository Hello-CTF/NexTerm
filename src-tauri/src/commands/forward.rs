//! 端口转发命令（M3-T8 / §5.2）：本地静态转发 + SOCKS5 动态转发。

use std::sync::Arc;

use crate::error::{AppError, AppResult};
use crate::ipc_shim as tauri;
use crate::ipc_types::ForwardEnvDto;
use crate::state::ManagedState;
use crate::transport::forward::{
    spawn_local_forward, spawn_socks_forward, ForwardPolicy, ForwardSpec, RunningForward,
};
use crate::transport::ssh::SshTransport;

/// 平台门：懒猫微服上不提供端口转发。
///
/// 为什么在**命令层**拒，而不是只在界面上把按钮禁掉：界面禁用只防误点，不防直接调
/// `/rpc`（服务端形态下 `/rpc` 就在那儿）。而且「建了却永远连不上」是最难查的一类症状 ——
/// 用户会一直以为是自己的端口或目标填错了。直接给一句能解释清楚的话，比让它静默无效强。
///
/// 平台为什么禁用（不是技术做不到）见 `transport::forward::ForwardPolicy` 的说明。
fn ensure_forward_allowed(policy: &ForwardPolicy) -> AppResult<()> {
    if policy.available {
        return Ok(());
    }
    Err(AppError::Unsupported(
        "端口转发在懒猫微服上暂不可用 —— 请改用微服平台自带的转发功能，或改用 SSH 隧道".into(),
    ))
}

/// 取会话底下的 SSH 传输 —— 转发只能建在 SSH 上。
///
/// 抽出来是因为两条命令都要做这同一件事，而 `as_any_arc().downcast()` 那串
/// 泛型体操在这里写两遍很容易写歪。
///
/// 被拒时按会话类型给话：本机会话不是"这个功能缺了"，而是"这件事没有意义"
/// （你已经在目标机器上了）—— 后者才解释得清，也才指得出下一步。
async fn ssh_of(state: &ManagedState<'_>, session_id: &str) -> AppResult<Arc<SshTransport>> {
    let s = state.sessions.get(session_id).await?;
    let kind = s.kind.clone();
    let t = s.transport().await;
    t.clone()
        .as_any_arc()
        .downcast::<SshTransport>()
        .map_err(|_| {
            if kind == "local" {
                AppError::Unsupported(
                    "本机就是目标机器，端口转发没有意义 —— 它存在的理由是把远端的口子搬到本机；\
                 要访问内网其他主机，请先建一台 SSH 资产并连上"
                        .into(),
                )
            } else {
                AppError::Unsupported(format!("端口转发需要 SSH 会话（当前是 {kind} 会话）"))
            }
        })
}

/// 把刚建好的转发登记进全局表。
async fn register(state: &ManagedState<'_>, running: RunningForward) -> ForwardSpec {
    let spec = running.spec.clone();
    state
        .sessions
        .forwards
        .write()
        .await
        .insert(spec.id.clone(), Arc::new(running));
    spec
}

/// 本地转发：`监听 bind_ip:listen_port → SSH → target_host:target_port`。
///
/// `bind_ip` 由平台策略定（桌面 = `127.0.0.1`，服务端 = `0.0.0.0`），
/// 也就是说同一条命令在服务端形态下就是「把远端服务搬到服务端端口上」。
#[tauri::command]
pub async fn forward_create(
    state: ManagedState<'_>,
    session_id: String,
    listen_port: u16,
    target_host: String,
    target_port: u16,
) -> AppResult<ForwardSpec> {
    ensure_forward_allowed(&state.forward_policy)?;
    let ssh = ssh_of(&state, &session_id).await?;
    let running = spawn_local_forward(
        &session_id,
        ssh,
        state.forward_policy.bind_ip,
        listen_port,
        target_host,
        target_port,
    )
    .await?;
    Ok(register(&state, running).await)
}

/// SOCKS5 动态转发：一个监听端口当通用代理，目标由客户端当场指定。
///
/// 和 `forward_create` 的差别只在"要不要固定目标" —— 所以参数里没有 target。
#[tauri::command]
pub async fn forward_create_socks(
    state: ManagedState<'_>,
    session_id: String,
    listen_port: u16,
) -> AppResult<ForwardSpec> {
    ensure_forward_allowed(&state.forward_policy)?;
    let ssh = ssh_of(&state, &session_id).await?;
    let running =
        spawn_socks_forward(&session_id, ssh, state.forward_policy.bind_ip, listen_port).await?;
    Ok(register(&state, running).await)
}

/// 转发能力自述：界面据此显示提示条、禁用创建、以及显示正确的监听地址。
///
/// 做成命令而不是写死在前端：这三个值取决于**跑在哪种形态、部署在哪个平台**，
/// 只有内核知道。前端自己猜会给出错误承诺（在懒猫上画出「外部可访问」而实际连不上）。
#[tauri::command]
pub async fn forward_env(state: ManagedState<'_>) -> AppResult<ForwardEnvDto> {
    let policy = &state.forward_policy;
    Ok(ForwardEnvDto {
        available: policy.available,
        platform: policy.platform.to_string(),
        listen_host: policy.bind_ip.to_string(),
    })
}

#[tauri::command]
pub async fn forward_list(state: ManagedState<'_>) -> AppResult<Vec<ForwardSpec>> {
    let forwards = state.sessions.forwards.read().await;
    Ok(forwards.values().map(|f| f.spec.clone()).collect())
}

#[tauri::command]
pub async fn forward_remove(state: ManagedState<'_>, id: String) -> AppResult<()> {
    if let Some(f) = state.sessions.forwards.write().await.remove(&id) {
        f.stop();
    }
    Ok(())
}

#[allow(dead_code)]
fn _type_anchor(_: &RunningForward) {}

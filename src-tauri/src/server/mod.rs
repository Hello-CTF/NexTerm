//! 服务端（懒猫微包容器，Linux 无桌面）装配与启动。
//!
//! # 与桌面版的关系
//!
//! **内核完全共用**：`store` / `vault` / `session` / `ai` / `db` / `docker` / `fs`
//! / `transport` 一行不改，改的只是最外面那层「IPC 出口」——
//! 桌面是 Tauri 的 IPC，服务端是 HTTP + WebSocket。
//!
//! # 两种形态：完整版与 onlyServer
//!
//! | | 完整版（默认） | onlyServer（`--sync-only`） |
//! |---|---|---|
//! | 用途 | 浏览器版：懒猫微服里那个、自建服务器上给人用的那个 | 只当资产同步的对端 |
//! | 端点 | `/rpc` `/sync/rpc` `/healthz` `/ws/*` `/files/blob*` + 静态资源 | 只有 `/sync/rpc` `/healthz` |
//! | 命令表 | 全部 132 条 | **3 条**（`sync_digest` / `sync_export` / `sync_import`） |
//! | 前端资源 | 需要 | 不需要 |
//!
//! 两态**同一份二进制、运行时切换**（不是两套 feature / 两个 bin），理由：
//! 内核与同步路径本来就共用，为「少挂几条路由」再造一套 cfg 门控，只会让
//! 双态架构多出一个需要同步维护的维度。
//!
//! ⚠️ **onlyServer 不是可选项而是必需的**：公网上的 `/rpc` 没有任何自身鉴权
//! （它靠懒猫平台登录门保护），而 `/sync/rpc` 的令牌走的是同一张命令表 ——
//! 也就是说**完整版一旦暴露在公网，令牌 = 完全控制权**。只挂三条命令，
//! 才谈得上「放到公网上只做同步」。
//!
//! # 启动顺序
//!
//! 日志 → SQLite（迁移）→ Vault → **平台注入的根密钥** → AI 运行时 → AppState
//! → 后台任务 → HTTP 监听。
//!
//! # 凭据库在服务端是「解锁态」
//!
//! 容器没有桌面、没有交互式输入主密码的场合，所以根密钥由平台注入
//! （`lzc-manifest.yml` 里的 `{{ stable_secret "..." }}`，同一微服+同应用内稳定、
//! 免用户交互），启动时用它 `init_master` / `unlock_master`。
//!
//! 因此**进程内存里持有明文 DEK**，这是设计选择不是疏漏：微服是用户自己的硬件，
//! 而「每次访问都要输主密码」在浏览器场景里会让整个凭据库功能废掉。
//! 商店文案里会写明这一点。
//!
//! 也正因为是解锁态，服务端**不启动自动锁后台任务**（`vault::auto_lock_task`）——
//! 那个任务的语义是「用户离开一会儿就锁上，等他自己回来输密码」，
//! 在无人值守的服务端上只会把功能锁死。
//!
//! # 服务端也不做「空闲会话清理」
//!
//! 同一条设计原则的第二个落点：服务端**不启动 `session::idle_sweeper`**。
//! 那个任务的语义是「前端标签全关掉、且再过 30 分钟，就把这个会话收掉」。
//! 这个前提**在服务端不成立**：「标签全关」只说明浏览器关了，**不代表用户走了**
//! —— 他完全可能正跑着一个几个小时的日志 / 编译 / 压测任务，只是把窗口最小化，
//! 或者换到手机上看。30 分钟把这种会话断掉是最恶劣的体验，而且用户很难把
//! 「链接突然断了」和「前端标签关过」这两件事联系起来。
//!
//! 代价要写清楚：**会话与 PTY 会一直留在内存里**。释放途径只有两条 ——
//! ① 用户在界面上显式结束 / 断开；② 服务端进程重启。这是有意接受的取舍，
//! 不是遗漏。**刻意不加一个「更长的阈值」来和稀泥**：那只是把问题从 30 分钟
//! 推到 3 小时，更难排查，还给人「有个阈值在兜底」的错觉。
//! （桌面版仍然保留 idle_sweeper —— 那里的「人就在本机、标签关了就是关了」
//! 是成立的，见 `lib.rs` 的 setup。）

mod blobs;
pub mod cli;
pub mod hub;
pub mod rpc;
mod static_files;

use std::net::SocketAddr;
use std::path::PathBuf;
use std::sync::Arc;

use axum::extract::{DefaultBodyLimit, State as AxumState};
use axum::http::{HeaderMap, StatusCode, Uri};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use serde::Deserialize;
use serde_json::{json, Value};

use crate::error::AppResult;
use crate::ipc_shim::AppHandle;
use crate::server::hub::WsHub;
use crate::server::rpc::Table;

/// 服务端全局上下文（axum 的 state）。
pub struct ServerCtx {
    pub state: Arc<crate::state::AppState>,
    pub app: AppHandle,
    pub table: Table,
    pub hub: Arc<WsHub>,
    /// 前端静态资源目录。**onlyServer 下是 `None`** —— 没有浏览器界面可服务，
    /// 用 `Option` 而不是「指一个不存在的目录」，是为了让「没挂静态资源」这件事
    /// 在类型上就能看出来（`/healthz` 也据此报 `null` 而不是一条假路径）。
    pub web_root: Option<PathBuf>,
    /// onlyServer 形态（只做资产同步）。`/healthz` 报出来，便于运维自证。
    pub sync_only: bool,
    /// 解析后的数据目录（`--data-dir` / `NEXTERM_DATA_DIR` / 内置默认）。
    ///
    /// 放进来是为了让各处理器**用同一份**：`blobs` 那几个接口此前各自去读环境变量，
    /// 命令行传了 `--data-dir` 时它们会算到另一个目录去（库与暂存区分家）。
    pub data_dir: PathBuf,
}

/// 监听地址的默认值。
///
/// `0.0.0.0` 是**容器要的**（懒猫平台从容器网络另一侧来访问，绑 127.0.0.1 会连不上），
/// 所以默认值不动。但裸 Linux 上照默认跑就等于把没有自身鉴权的 `/rpc` 挂到了
/// 公网 —— 见 `serve` 里那条非回环警告，以及 `--sync-only`。
pub const DEFAULT_LISTEN: &str = "0.0.0.0:8080";

/// 默认监听地址（`DEFAULT_LISTEN` 的解析结果）。
pub fn default_listen() -> SocketAddr {
    SocketAddr::from(([0, 0, 0, 0], 8080))
}

/// 短命标签（日志跟随）在最后一个订阅者离开后，延迟多久复查并回收。
///
/// # 为什么要留宽限期
///
/// 通道 WS 断开**不区分**「用户关掉页面走了」和「网络抖了一下 / 浏览器 bfcache
/// 后退恢复」：两种都会先让订阅者归零。无条件立即回收，就会把「马上就要重连
/// 回来」的日志跟随标签误杀。所以先等一个宽限期，再复查订阅者是否仍为 0。
///
/// # 取值依据（实测）
///
/// 前端在有视图回来时的订阅登记耗时实测：普通终端（`XtermView` 的
/// `onChannelReopen` 会重发 `terminal_attach`）**368ms** —— 即 `subscribers`
/// 从 0 回到 1 的实测值（本机 + headless Chrome，`/rpc terminal_list` 轮询）。
/// 取 **5s ≈ 13×** 这个实测值，留足余量：WS 重连退避是 300ms 起步、逐次翻倍
/// （300 / 600 / 1200 / 2400 / 4800 / 8000ms 封顶），5s 足以覆盖前四次退避，
/// 也就是说连续几次抖动也不会误杀。
///
/// ⚠️ **只对 `TerminalTab::is_ephemeral()` 为真的标签生效**（目前只有
/// `docker_logs_attach` 打的标记）。普通交互终端订阅归零后**永不**被这条路径回收。
const EPHEMERAL_RECLAIM_GRACE: std::time::Duration = std::time::Duration::from_secs(5);

/// 数据目录**内置默认值**（不含环境变量 / 命令行 —— 那两层由 `cli` 负责）。
///
/// 容器里 `/lzcapp/var` 是**持久卷**（LPK 规范里唯一保证跨升级保留的路径），
/// 所以有它就一定用它的子目录。本机跑（验收 / 调试）时退回 `./data`。
pub fn default_data_dir() -> PathBuf {
    let lzc = PathBuf::from("/lzcapp/var");
    if lzc.is_dir() {
        return lzc.join("nexterm");
    }
    PathBuf::from("data")
}

/// 猜前端静态资源目录（没有显式配置时）。
fn probe_web_root() -> PathBuf {
    for cand in ["/app/dist", "dist", "../dist"] {
        let p = PathBuf::from(cand);
        if p.join("index.html").is_file() {
            return p;
        }
    }
    PathBuf::from("/app/dist")
}

/// 运行时参数（命令行 / 环境变量的解析结果，见 `cli`）。
///
/// 把「从哪读配置」与「怎么跑起来」分开：`serve` 只认这个结构，
/// 于是测试里也能直接造一份，不必去动进程环境变量。
#[derive(Debug, Clone)]
pub struct Options {
    /// 只做资产同步（只挂 `/sync/rpc` + `/healthz`，只注册三条命令）。
    pub sync_only: bool,
    pub listen: SocketAddr,
    pub data_dir: PathBuf,
    /// 前端资源目录。`None` = 按 `probe_web_root()` 猜。
    pub web_root: Option<PathBuf>,
    /// 凭据库根密钥。`None` = 凭据库保持未初始化（密码类资产同步会失败）。
    pub master_key: Option<String>,
}

/// 绑到非回环地址时把风险喊出来。
///
/// 为什么**不直接拒绝启动**：懒猫微包容器必须绑 `0.0.0.0` —— 平台从容器网络
/// 另一侧来访问，绑 `127.0.0.1` 就是连不上。所以默认值不能改成回环，
/// 「裸 Linux 上照默认跑」这条只能靠**说清楚**来防（`--sync-only` 是另一道闸）。
///
/// ⚠️ 必须**同时**走 tracing 与 stderr，不能只发 tracing：本项目所有 tracing 都只
/// 落到 `<数据目录>/logs/nexterm.log`（见 `init_tracing`），进程的 stdout/stderr
/// 平时是空的（实测 0 字节）。只落文件就等于没警告 —— 首次部署的人不会想到去翻
/// 数据目录里的日志，而这条警告的全部意义就是**当场被看见**。
/// stderr 是终端、`journalctl`、`docker logs` 都能收到的那一路。
fn warn_if_exposed(addr: SocketAddr, sync_only: bool) {
    if addr.ip().is_loopback() {
        return;
    }
    let detail = if sync_only {
        "onlyServer 模式：能连上这个端口、又拿到同步令牌的人，可以读写你的资产库\n\
         （含密码类凭据）。请确保端口没有直接暴露在公网，或用防火墙只放行对端地址。"
    } else {
        "⚠ 完整版：`/rpc` 与浏览器界面没有任何自身鉴权，能连上这个端口的人即\n\
         拥有完整控制权 —— 终端、文件、Docker、凭据库。\n\
         公网部署请改用 `--sync-only`（onlyServer），或只监听 127.0.0.1 再由带鉴权\n\
         的反向代理转发。"
    };
    tracing::warn!(target: "boot", %addr, sync_only, "非回环地址监听：{}", detail);
    eprintln!(
        "\n\
         ════════════════════════════════════════════════════════════════\n\
         ⚠  NexTerm 服务端正在非回环地址 {addr} 上监听\n\
         {detail}\n\
         ════════════════════════════════════════════════════════════════\n"
    );
}

/// 启动服务端。返回即进程该退出（正常关闭或致命错误）。
pub async fn serve(opts: Options) -> Result<(), Box<dyn std::error::Error>> {
    let data_dir = opts.data_dir.clone();
    std::fs::create_dir_all(&data_dir)?;
    let log_dir = data_dir.join("logs");
    let _ = std::fs::create_dir_all(&log_dir);
    let _log_guard = crate::init_tracing(&log_dir);
    crate::install_panic_hook(log_dir.clone());

    // onlyServer 没有浏览器界面 ⇒ 不去猜前端资源目录（猜了也没人用）。
    let web_root = if opts.sync_only {
        None
    } else {
        Some(opts.web_root.clone().unwrap_or_else(probe_web_root))
    };

    tracing::info!(
        target: "boot",
        mode = if opts.sync_only { "onlyServer（只做资产同步）" } else { "完整版（浏览器界面）" },
        data_dir = %data_dir.display(),
        web_root = %web_root
            .as_ref()
            .map(|p| p.display().to_string())
            .unwrap_or_else(|| "(无)".to_string()),
        "NexTerm 服务端启动中"
    );
    warn_if_exposed(opts.listen, opts.sync_only);

    // ── 内核装配（与桌面版逐条对齐，见 lib.rs 的 setup）──────────────
    let db_path = data_dir.join("data.db");
    let store = match crate::store::Store::open(&db_path).await {
        Ok(s) => Arc::new(s),
        Err(e) => {
            tracing::error!(target: "boot", error = %e, "SQLite 打开失败");
            return Err(e.into());
        }
    };
    let vault = crate::vault::Vault::load(Arc::clone(&store)).await;
    bootstrap_vault(&vault, opts.master_key.as_deref()).await;

    match store.asset_ensure_builtin_local().await {
        Ok(a) => tracing::info!(target: "boot", asset = %a.name, "内置本地资产就绪"),
        Err(e) => tracing::warn!(target: "boot", error = %e, "内置本地资产创建失败"),
    }

    // 同步令牌：第一次启动就生成并落库。
    // **不把令牌本身写进日志** —— 日志会被收集、转发、贴进工单。
    match crate::sync::ensure_token(&store).await {
        Ok(_) => tracing::info!(
            target: "boot",
            hint = if opts.sync_only {
                "用 `nexterm-server token` 查看"
            } else {
                "浏览器版「设置 → 资产同步」里可以查看"
            },
            "同步令牌已就绪"
        ),
        Err(e) => {
            tracing::warn!(target: "boot", error = %e, "同步令牌初始化失败，桌面端同步将不可用")
        }
    }

    let sessions = Arc::new(crate::session::SessionManager::new());
    let model_store = crate::ai::profiles::ModelProfileStore::load(&store)
        .await
        .unwrap_or_default();
    let provider = model_store
        .active()
        .map(|p| p.to_provider())
        .unwrap_or_default();
    let ai = Arc::new(crate::ai::AiRuntime::new(provider));
    if let Some(cfg) = store
        .setting_get("ai.permission")
        .await
        .ok()
        .flatten()
        .and_then(|s| serde_json::from_str::<crate::ai::guard::PermissionConfig>(&s).ok())
    {
        *ai.permission.write().await = cfg;
    }
    let dbmgr = Arc::new(crate::db::DbManager::default());

    let hub = WsHub::new();
    // 服务端的事件出口就是一个 AppHandle：数据目录 + 总线。
    let app = AppHandle::new(
        data_dir.clone(),
        Arc::clone(&hub) as Arc<dyn crate::ipc_shim::Hub>,
    );

    let state = crate::state::AppState::new(app.clone(), store, vault, sessions, ai, dbmgr);

    // 把状态回填给句柄：`session::AppCallbacks` 拿到的只有 `AppHandle`，泵退出后
    // 靠 `Manager::try_state` 把状态取回来判断"该不该自动重连"。桌面那边等价于
    // Tauri 的 `manage()`；服务端没有框架，只能手动挂一次。
    app.attach_state(Arc::clone(&state));

    // 通道 WS 断开 → 摘掉终端标签里对应的那条订阅；对短命标签（日志跟随）
    // 则进一步在宽限期后回收（见 [`EPHEMERAL_RECLAIM_GRACE`]）。
    //
    // 不做摘订阅的后果很具体：浏览器关掉页面后，标签的 `sinks` 表里会留下一个
    // 已经死掉的通道，之后每一帧 PTY 输出都要白跑一遍；而且**不能指望发送失败
    // 来自动清理** —— 门面的 `Channel::send` 永远返回 `Ok`（投递结果由 hub 自己
    // 消化），也就是说"这条通道断了"这件事**只有 hub 知道**，所以只能由它回调出来。
    //
    // 回调里不 `await`：它在关闭路径上，被拖住就等于连接关不掉。
    {
        let st = Arc::clone(&state);
        hub.set_on_channel_closed(Arc::new(move |channel_id: &str| {
            let st = Arc::clone(&st);
            let cid = channel_id.to_string();
            tokio::spawn(async move {
                // 遍历所有标签而不是查索引：前端是「先开通道、再调 attach」，
                // 服务端拿到通道 id 的那一刻还不知道它属于谁，建反向索引得额外
                // 维护一致性；而断连既不频繁、标签数量也就是几十个。
                let tabs: Vec<_> = st.sessions.tabs.read().await.values().cloned().collect();
                for t in tabs {
                    // 只有真的摘掉了某条订阅（人数变了）才广播 —— 关一条通道时会
                    // 遍历所有标签，绝大多数标签并不含这条通道，为它们白发事件只是噪音。
                    let before = t.subscriber_count().await;
                    t.detach_subscriber(&cid).await;
                    if t.subscriber_count().await != before {
                        // 走的人可能正是控制者：detach_subscriber 会顺带释放控制权，
                        // 剩下的人必须立刻知道「现在没人持权 / 换成谁了」。
                        crate::session::notify_control_changed(&st, &t).await;
                    }
                    // 短命资源（日志跟随）在**最后一个订阅者**走后回收。
                    //
                    // # 为什么不能立刻回收
                    //
                    // 这条回调**不区分**「用户关掉页面走了」和「网络抖了一下 /
                    // 浏览器 bfcache 后退恢复」—— 两种都会先看到订阅者归零。
                    // 立即回收会把「马上就要重连回来」的标签误杀，用户按一下后退
                    // 就发现日志面板死了。延迟一个宽限期再复查一次：宽限期内有视图
                    // 回来（`subscribers` 回到 > 0）就自然免于误杀，**不需要**任何
                    // 额外的取消 / 续期机制 —— 复查本身就是判据。
                    if t.is_ephemeral() && t.subscriber_count().await == 0 {
                        let reclaim = Arc::clone(&st);
                        let tab_id = t.tab_id.clone();
                        tokio::spawn(async move {
                            tokio::time::sleep(EPHEMERAL_RECLAIM_GRACE).await;
                            // 复查：标签可能已被别的路径回收（拿不到就直接返回）。
                            let Ok(tab) = reclaim.sessions.get_tab(&tab_id).await else {
                                return;
                            };
                            if tab.subscriber_count().await != 0 {
                                return; // 宽限期内有视图回来：免于误杀。
                            }
                            // 幂等：`close_tab` 自己会再 `get_tab`，拿不到 tab 返回 Err，
                            // 吞掉即可（与上面这次复查之间的窗口里被别的路径关掉了）。
                            let _ = crate::session::close_tab(&reclaim, &tab_id).await;
                        });
                    }
                }
            });
        }));
    }

    // 后台任务：**不启动空闲会话清理，也不启动自动锁** —— 两者是同一条理由
    // （服务端无人值守，「用户走了」这个前提不成立），详见模块文档
    // 「凭据库在服务端是「解锁态」」与「服务端也不做「空闲会话清理」」两节。
    //
    // ⚠️ 只删掉了服务端这一处 spawn，**`session::idle_sweeper` 函数本身保留**：
    // 桌面版在 `lib.rs` 的 setup 里仍在用它（那边「标签关了就是人走了」成立）。

    // 命令表按形态注册。⚠️ **这是 onlyServer 的全部意义所在**：`/sync/rpc` 的
    // 令牌走同一张表，全量注册（132 条）时它等价于完全控制权；只留三条，
    // 令牌的权限就被限制在「能读写这份资产库」。
    let table = if opts.sync_only {
        Table::build_sync_only()
    } else {
        Table::build()
    };
    tracing::info!(
        target: "boot",
        commands = table.len(),
        sync_only = opts.sync_only,
        "RPC 表已装载"
    );
    if table.is_empty() {
        return Err("RPC 表为空：#[command] 展开或命令清单出了问题".into());
    }

    if let Some(root) = web_root.as_ref() {
        if !root.join("index.html").is_file() {
            tracing::warn!(
                target: "boot",
                root = %root.display(),
                "web root 里没有 index.html：前端不会加载（RPC 与 WS 仍然可用）"
            );
        }
    }

    let ctx = Arc::new(ServerCtx {
        state,
        app,
        table,
        hub,
        web_root,
        sync_only: opts.sync_only,
        data_dir: data_dir.clone(),
    });

    // onlyServer 只挂两条：同步入口 + 健康检查。
    //
    // 不挂静态资源（没有界面）、不挂 `/ws/*`（没有界面订阅事件）、不挂 `/files/blob*`
    // （那是浏览器版的文件中转站，只在有界面时有意义）、**也不挂 `/rpc`**
    // —— 最后这条是关键：`/rpc` 没有自身鉴权，挂上去等于把完整控制权重新放出来。
    let router = if opts.sync_only {
        Router::new()
            .route("/sync/rpc", post(handle_sync_rpc))
            .route("/healthz", get(handle_healthz))
            .layer(DefaultBodyLimit::max(256 * 1024 * 1024))
            .with_state(Arc::clone(&ctx))
    } else {
        Router::new()
            .route("/rpc", post(handle_rpc))
            // 桌面端的同步入口。**单独一条路径而不是给 `/rpc` 加锁**：`/rpc` 是
            // 浏览器版在用的（靠平台登录门保护），动它等于给已有功能引入新的
            // 故障模式；同步是新增能力，就该待在新增的路径上。见 `handle_sync_rpc`。
            .route("/sync/rpc", post(handle_sync_rpc))
            .route("/healthz", get(handle_healthz))
            .route("/ws/events", get(hub::ws_events))
            .route("/ws/channel/{id}", get(hub::ws_channel))
            // 浏览器版的文件中转站（见 `blobs` 模块文档）：
            // 桌面版的文件路径在浏览器里不存在，上传/下载要经过这里换一次手，
            // 传输内核（fs_upload / fs_download / 打包 / 日志导出）才能一行不改地复用。
            //
            // 这几个接口**不在** `/rpc` 里，是因为它们承载的是**原始字节**：
            // 走 JSON 得 base64，体积涨 1/3 且要在内存里复制一遍。
            .route(
                "/files/blob",
                get(blobs::get_blob)
                    .post(blobs::post_blob)
                    .delete(blobs::delete_blob),
            )
            .route("/files/blob/reserve", post(blobs::post_reserve))
            .fallback(handle_static)
            // 文件写入 / 上传会把内容 base64 塞进 JSON，默认 2MB 上限对它们太小。
            .layer(DefaultBodyLimit::max(256 * 1024 * 1024))
            .with_state(Arc::clone(&ctx))
    };

    if !opts.sync_only {
        // 暂存区巡检：只按时间扫，因为「选完文件就走了」这种情况不会再有请求来触发清理。
        tokio::spawn(blobs::sweep_task(data_dir.clone()));
    }

    let addr = opts.listen;
    let listener = tokio::net::TcpListener::bind(addr).await?;
    tracing::info!(target: "boot", %addr, "NexTerm 服务端就绪");

    axum::serve(listener, router)
        .with_graceful_shutdown(shutdown_signal())
        .await?;
    tracing::info!(target: "boot", "NexTerm 服务端已退出");
    Ok(())
}

/// 用注入的根密钥把凭据库拉到解锁态。
///
/// 密钥来源已在 `cli` 里收敛为「命令行 > 环境变量」（懒猫 manifest 走后者）。
/// 这里只负责用，不再自己读环境 —— 上一版的 `std::env::var` 会让
/// 「命令行传了、但环境变量也有」变成两套真相。
async fn bootstrap_vault(vault: &Arc<crate::vault::Vault>, master_key: Option<&str>) {
    let key = match master_key {
        Some(k) if !k.is_empty() => k,
        _ => {
            tracing::warn!(
                target: "boot",
                "未提供凭据库根密钥（--master-key / NEXTERM_MASTER_KEY）：\
                 凭据库保持未初始化，密码类资产的同步会失败"
            );
            return;
        }
    };
    // 与 `Vault::init_master` 同一条下限。密钥短于 8 位时**显式报错**，
    // 不静默截断 —— 那是「应用能起来但凭据永远解不开」的经典成因。
    if key.len() < 8 {
        tracing::error!(
            target: "boot",
            len = key.len(),
            "根密钥少于 8 位，拒绝使用（凭据库保持未初始化）"
        );
        return;
    }
    let status = vault.status().await;
    let outcome = if status.initialized {
        vault.unlock_master(key).await
    } else {
        vault.init_master(key).await
    };
    match outcome {
        Ok(()) => tracing::info!(
            target: "boot",
            initialized_now = !status.initialized,
            "凭据库已解锁（服务端为常开解锁态，公开部署请自行评估）"
        ),
        Err(e) => tracing::error!(target: "boot", error = %e, "凭据库根密钥未被接受"),
    }
}

async fn shutdown_signal() {
    let ctrl_c = async {
        let _ = tokio::signal::ctrl_c().await;
    };
    #[cfg(unix)]
    let term = async {
        if let Ok(mut s) = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
        {
            s.recv().await;
        }
    };
    #[cfg(not(unix))]
    let term = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => {}
        _ = term => {}
    }
    tracing::info!(target: "boot", "收到退出信号，正在优雅关闭");
}

// ── HTTP 处理器 ───────────────────────────────────────────────────────

#[derive(Deserialize)]
struct RpcRequest {
    cmd: String,
    #[serde(default)]
    args: Value,
}

/// `POST /rpc`
///
/// 错误也回 200 + 信封。理由：前端要拿 `body.error.code` 才分得出
/// 「凭据库锁了」和「参数写错了」，用 4xx/5xx 会让 `fetch` 的错误路径
/// 和业务错误路径分裂成两套处理。
async fn handle_rpc(
    AxumState(ctx): AxumState<Arc<ServerCtx>>,
    Json(req): Json<RpcRequest>,
) -> Response {
    dispatch(ctx, req).await
}

/// `POST /sync/rpc` —— 桌面端的同步入口。
///
/// 与 `/rpc` 共用同一条分发路径，差别只有一处：**先鉴权**。两条路任一成立即可：
///
/// ① `X-NexTerm-Sync-Token` 与本实例令牌一致 —— 不依赖平台，`public_path`
///    放行或同网段直连时单靠它就够；
/// ② 请求带 `X-HC-User-ID` —— 说明平台网关已经鉴过权。桌面用官方
///    `Lzc-Api-Auth-Token` 走公网域名时走的就是这条路（该头由网关消费，
///    转发进容器时会被移除，所以应用只能靠注入的用户标识判断）。
///
/// ⚠️ **令牌等价于「本实例的完全控制权」**：它调的是同一张 RPC 表，能执行任何
/// 已注册命令，不只是资产同步。界面与文档的文案必须据实说明，不能做成
/// 「只读配对码」的样子。
async fn handle_sync_rpc(
    AxumState(ctx): AxumState<Arc<ServerCtx>>,
    headers: HeaderMap,
    Json(req): Json<RpcRequest>,
) -> Response {
    if let Err(e) = authorize_sync(&ctx, &headers).await {
        tracing::warn!(target: "sync", cmd = %req.cmd, code = e.code(), "同步请求被拒");
        return (
            StatusCode::UNAUTHORIZED,
            Json(json!({ "ok": false, "error": e })),
        )
            .into_response();
    }
    dispatch(ctx, req).await
}

/// `/sync/rpc` 的准入判定。
async fn authorize_sync(ctx: &ServerCtx, headers: &HeaderMap) -> AppResult<()> {
    let presented = headers
        .get(crate::sync::TOKEN_HEADER)
        .and_then(|v| v.to_str().ok())
        .unwrap_or_default();
    // 令牌对了就走这条路；不对也**不立刻拒** —— 先看平台头。最后再拿令牌校验
    // 的错误信息回报：说「令牌不正确」比说「缺少令牌」对排查有用得多。
    if !presented.is_empty()
        && crate::sync::verify_token(&ctx.state.store, presented)
            .await
            .is_ok()
    {
        return Ok(());
    }
    if headers.contains_key(crate::sync::PLATFORM_USER_HEADER) {
        return Ok(());
    }
    crate::sync::verify_token(&ctx.state.store, presented).await
}

/// 命令分发的唯一实现（两条路径共用，避免两侧行为漂移）。
async fn dispatch(ctx: Arc<ServerCtx>, req: RpcRequest) -> Response {
    let started = std::time::Instant::now();
    let result = ctx
        .table
        .call(&req.cmd, req.args, &ctx.state, &ctx.app)
        .await;
    match result {
        Ok(data) => {
            tracing::debug!(target: "rpc", cmd = %req.cmd, ms = started.elapsed().as_millis() as u64, "ok");
            Json(json!({ "ok": true, "data": data })).into_response()
        }
        Err(e) => {
            tracing::debug!(target: "rpc", cmd = %req.cmd, code = e.code(), error = %e, "err");
            Json(json!({ "ok": false, "error": e })).into_response()
        }
    }
}

/// `GET /healthz` —— 自证「装了多少条命令、几条连接」。
///
/// 对容器与商店审核都有用：看得到服务**真的起来了**，而不是只回一个 200。
async fn handle_healthz(AxumState(ctx): AxumState<Arc<ServerCtx>>) -> Response {
    let vault = ctx.state.vault.status().await;
    Json(json!({
        "ok": true,
        "service": "nexterm-server",
        "version": env!("CARGO_PKG_VERSION"),
        // onlyServer 与完整版的区别在**命令表**上，所以这两个字段要能一眼对上：
        // `syncOnly: true` 时 commands 必须是 3，多了就说明模式没生效。
        "syncOnly": ctx.sync_only,
        "commands": ctx.table.len(),
        "eventSubscribers": ctx.hub.event_subscribers(),
        "liveChannels": ctx.hub.live_channels(),
        "webRoot": ctx.web_root.as_ref().map(|p| p.to_string_lossy()),
        "vault": vault,
    }))
    .into_response()
}

async fn handle_static(AxumState(ctx): AxumState<Arc<ServerCtx>>, uri: Uri) -> Response {
    match ctx.web_root.as_deref() {
        Some(root) => static_files::serve(root, uri.path()).await,
        // onlyServer 根本没挂这条路由；真走到这里说明路由装配被改了。
        // 明确报出来，别回一个看不出所以然的 404 页面。
        None => (
            StatusCode::NOT_FOUND,
            "onlyServer 模式没有浏览器界面（只有 /sync/rpc 与 /healthz）",
        )
            .into_response(),
    }
}

/// `/healthz` 里自证用的命令名列表（排查用，不进 HTTP 面）。
#[allow(dead_code)]
pub fn command_names() -> Vec<&'static str> {
    Table::build().names()
}

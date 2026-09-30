// NexTerm 服务端入口（懒猫微包容器 / 自建 Linux / onlyServer）。
//
// # 三种跑法
//
//   nexterm-server                        完整版（容器 ENTRYPOINT 就是这个，不带参数）
//   nexterm-server --sync-only            onlyServer：只做资产同步
//   nexterm-server token | rotate-token   取 / 换同步令牌（没有 web UI 时的唯一入口）
//
// 参数解析全在 `nexterm_lib::server::cli`，这里只做「解析 → 起运行时 → 分发」。
//
// # 为什么与桌面入口分成两个 bin
//
// 桌面 `main.rs` 第一行就把 Windows 子系统设成 `windows`（**不要控制台**），
// 而服务端**必须有 stdout** —— 容器日志、`docker logs`、`lzc-cli project log`、
// 以及 `token` 子命令的输出全长在它上面。合成一个 bin 就得在编译期分叉入口属性，
// 还要处理「服务端的日志去哪」这种只在一边成立的问题，不值得。
//
// # 关于 `required-features`
//
// 本 bin 在 `Cargo.toml` 里带 `required-features = ["server"]`，那是**必须的**：
// 桌面打包时 Tauri CLI 会「取 cargo 产出的那个二进制塞进 .app」，包里一旦有两个
// bin，它挑中的就是这个服务端桩 ⇒ 装上去点开就退。加上这条后桌面构建里它压根不参与。
// （完整复盘见 `src-tauri/Cargo.toml` 里 `[[bin]] nexterm-server` 上方那段注释。）
//
// 而 `--all-features` 会同时打开 desktop + server，那时本文件走下面 desktop 分支。

#[cfg(not(feature = "desktop"))]
fn main() {
    use clap::Parser;
    use nexterm_lib::server::cli::{Action, Cli};

    let cli = Cli::parse();

    // 与桌面版同样的多线程 runtime。服务端会同时跑 PTY 泵、SSH 会话、
    // 若干 WS 连接，单线程 runtime 会把它们串成一条队。
    let rt = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .expect("构建 tokio 运行时失败");

    let code = rt.block_on(async {
        match cli.action() {
            Action::Serve(opts) => match nexterm_lib::server::serve(opts).await {
                Ok(()) => 0,
                Err(e) => {
                    eprintln!("NexTerm 服务端启动失败: {e}");
                    1
                }
            },
            Action::PrintToken {
                data_dir,
                rotate,
                sync_only,
            } => match nexterm_lib::server::cli::print_token(&data_dir, rotate, sync_only).await {
                Ok(()) => 0,
                Err(e) => {
                    eprintln!("读取同步令牌失败: {e}");
                    1
                }
            },
        }
    });
    std::process::exit(code);
}

#[cfg(feature = "desktop")]
fn main() {
    // 走到这里说明这个 bin 是在桌面模式下编出来的（`cargo build` 默认就是）。
    // 明说清楚，免得有人拿着它去容器里跑，然后收到一堆看不懂的 Tauri 报错。
    eprintln!(
        "nexterm-server 是服务端二进制，当前是桌面模式构建，里面没有服务端代码。\n\
         请这样构建：\n\
         \x20 cargo build --release --no-default-features --features server --bin nexterm-server"
    );
    std::process::exit(2);
}

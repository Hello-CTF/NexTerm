// NexTerm 服务端入口（懒猫微包容器：Linux、无桌面、浏览器访问）。
//
// # 为什么与桌面入口分成两个 bin
//
// 桌面 `main.rs` 第一行就把 Windows 子系统设成 `windows`（**不要控制台**），
// 而服务端**必须有 stdout** —— 容器日志、`docker logs`、`lzc-cli project log`
// 全靠它。合成一个 bin 就得在编译期分叉入口属性，还要处理「服务端的日志
// 去哪」这种只在一边成立的问题，不值得。
//
// # 为什么这里不需要 `required-features`
//
// CI 跑的是 `clippy --all-targets --all-features`，而 `--all-features` 会
// 同时打开 `desktop`（我们的设计里 `--all-features` 等价于桌面模式，见
// ipc_shim.rs）。若给这个 bin 加 `required-features = ["server"]`，
// `--all-features` 下它照样会被编，但那时服务端模块是 cfg 掉的 ——
// 所以直接在这里按 cfg 分叉，两种口径都能过。

#[cfg(not(feature = "desktop"))]
fn main() {
    // 与桌面版同样的多线程 runtime。服务端会同时跑 PTY 泵、SSH 会话、
    // 若干 WS 连接，单线程 runtime 会把它们串成一条队。
    let rt = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .expect("构建 tokio 运行时失败");
    if let Err(e) = rt.block_on(nexterm_lib::server::serve()) {
        eprintln!("NexTerm 服务端启动失败: {e}");
        std::process::exit(1);
    }
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

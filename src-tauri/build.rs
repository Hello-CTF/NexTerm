fn main() {
    // 只在桌面模式跑 Tauri 的构建脚本。
    //
    // 服务端模式没有 `tauri` 依赖，也不需要它生成的 `OUT_DIR`（唯一消费者是
    // `tauri::generate_context!`，那处已经门控掉了）。而 `tauri-build` 本身
    // **不能**设为 optional（Cargo 的 build-dependencies 不支持 optional），
    // 所以门控只能写在调用处。
    //
    // 判据用环境变量而不是 `#[cfg(feature = ...)]`：构建脚本能拿到 `--cfg feature`
    // 是 Cargo 的实现细节，而 `CARGO_FEATURE_*` 是文档承诺的接口。
    if std::env::var_os("CARGO_FEATURE_DESKTOP").is_some() {
        tauri_build::build();
    }
}

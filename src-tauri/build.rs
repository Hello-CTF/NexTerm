fn main() {
    // Cargo 的默认行为是「**包内任何文件变动就重跑构建脚本**」（构建脚本一旦没有
    // 输出任何 `cargo:rerun-if-changed`，Cargo 就退回这个包级追踪）。本脚本的真实
    // 输入只有它自己，所以必须显式声明，否则在 `src-tauri/` 下改一个无关文件
    // （哪怕只是个临时 .md）都会白跑一遍构建脚本。
    //
    // 桌面模式下 `tauri_build::build()` 会另行声明它自己的输入
    // （tauri.conf.json / frontendDist / capabilities / icons —— 见 tauri-build
    // 的 codegen/context.rs 与 acl.rs），与本行是并集关系，互不覆盖。
    println!("cargo:rerun-if-changed=build.rs");

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

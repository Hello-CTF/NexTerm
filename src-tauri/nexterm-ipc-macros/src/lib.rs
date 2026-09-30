//! `#[command]` —— 把一条 IPC 命令**同时**编译成桌面版与服务端版。
//!
//! # 它解决什么
//!
//! NexTerm 的命令层有 122 个 `#[tauri::command] pub async fn`。桌面版靠 Tauri 的
//! 宏把「反序列化参数 → 调用函数 → 序列化返回值」这套胶水生成出来；服务端
//! （懒猫微服容器，无 Tauri）需要**等价的一份胶水**，否则得手写 122 条
//! `match` + 手工解包每个参数。
//!
//! 更糟的是「改一处漏一处」：手写的分发表与真实函数签名会漂移，而**编译器不会报错**
//! （参数解错只是运行期 400）。所以胶水必须由**签名本身**生成。
//!
//! # 它做什么
//!
//! 展开成两项：
//!
//! ```ignore
//! // ① 原函数原样保留。桌面下补回 `#[tauri::command]`，服务端下一个属性都不挂。
//! #[cfg_attr(feature = "desktop", ::tauri::command)]
//! pub async fn terminal_resize(state: ManagedState<'_>, tab_id: String, ...) -> AppResult<()> { ... }
//!
//! // ② 同名模块（服务端专属）：`call` 就是 RPC 适配器。
//! #[cfg(not(feature = "desktop"))]
//! pub mod terminal_resize {
//!     pub fn call<'a>(ctx: &'a Ctx<'a>) -> RpcFuture<'a> {
//!         Box::pin(async move {
//!             let state = ctx.state();
//!             let tab_id: String = ctx.arg("tabId", "tab_id")?;
//!             ...
//!             let out = super::terminal_resize(state, tab_id, ...).await?;
//!             ctx.ok(out)
//!         })
//!     }
//! }
//! ```
//!
//! **模块名 = 函数名**是刻意的：Rust 的模块与函数分属两个命名空间，可以同名共存，
//! 这样就**不需要拼接标识符**（稳定 Rust 里做不到），注册表也就能用声明式宏按
//! `<模块路径>::call` 取到适配器。这条与「`use` 别名遮蔽别名属性宏」一样，
//! 都先用 `rustc` 单文件实验验证过，不是推测。
//!
//! # 参数分类
//!
//! 只有三类需要特殊处理，其余一律按「同名字段」从 RPC 载荷里反序列化：
//!
//! | 形参类型 | 服务端取值 |
//! |---|---|
//! | `ManagedState<'_>` / `State<'_, T>` | `ctx.state()` |
//! | `tauri::AppHandle` | `ctx.app()` |
//! | `Channel<T>` | `ctx.channel("channel", "channel")` —— 载荷里是 WS 通道 id |
//! | 其它（含 `Option<T>`、结构体） | `ctx.arg("camelCase", "snake_case")` |
//!
//! # 返回值
//!
//! 返回类型是 `AppResult<T>` 的（122 条里的绝大多数）走 `?` 自动转成 RPC 错误；
//! 不是的（如 `app_platform() -> String`）直接序列化。

use proc_macro::TokenStream;
use proc_macro2::TokenStream as TokenStream2;
use quote::quote;
use syn::{FnArg, ItemFn, Pat, ReturnType, Type};

/// 见模块文档。用法与 `#[tauri::command]` 完全一致（无参数）。
#[proc_macro_attribute]
pub fn command(attr: TokenStream, item: TokenStream) -> TokenStream {
    let func = syn::parse_macro_input!(item as ItemFn);
    let attr = TokenStream2::from(attr);
    match expand(func, attr) {
        Ok(ts) => ts.into(),
        Err(e) => e.to_compile_error().into(),
    }
}

/// 形参分类。
enum ParamKind {
    /// `ManagedState<'_>` / `State<'_, Arc<AppState>>`
    State,
    /// `tauri::AppHandle`
    App,
    /// `Channel<T>` —— 服务端由 WS 通道 id 构造
    Channel,
    /// 其余：按名从载荷取
    Plain,
}

fn classify(ty: &Type) -> ParamKind {
    let Type::Path(tp) = ty else {
        return ParamKind::Plain;
    };
    let Some(seg) = tp.path.segments.last() else {
        return ParamKind::Plain;
    };
    match seg.ident.to_string().as_str() {
        // 注意：`ManagedState` 是 crate 内别名，`State` 是 Tauri 原名，两个都收。
        "ManagedState" | "State" => ParamKind::State,
        "AppHandle" => ParamKind::App,
        "Channel" => ParamKind::Channel,
        _ => ParamKind::Plain,
    }
}

/// `snake_case` → `camelCase`。
///
/// 前端一律用 camelCase 传参（Tauri v2 的默认约定，现有 773 行 `src/ipc/` 都按它写），
/// 所以服务端必须按同一套找字段。同时保留 snake_case 兜底 —— 手写 curl 调试时
/// 没人愿意去数驼峰。
fn to_camel(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut upper_next = false;
    for ch in s.chars() {
        if ch == '_' {
            upper_next = true;
        } else if upper_next {
            out.extend(ch.to_uppercase());
            upper_next = false;
        } else {
            out.push(ch);
        }
    }
    out
}

fn expand(func: ItemFn, attr: TokenStream2) -> syn::Result<TokenStream2> {
    let name = func.sig.ident.clone();
    let attrs = &func.attrs;
    let vis = &func.vis;
    let sig = &func.sig;
    let block = &func.block;

    let mut prelude: Vec<TokenStream2> = Vec::new();
    let mut call_args: Vec<TokenStream2> = Vec::new();

    for input in &func.sig.inputs {
        let pt = match input {
            FnArg::Typed(pt) => pt,
            FnArg::Receiver(r) => {
                return Err(syn::Error::new_spanned(
                    r,
                    "#[command] 不支持 self 参数（命令一律是自由函数）",
                ))
            }
        };
        let ident = match &*pt.pat {
            Pat::Ident(pi) => pi.ident.clone(),
            other => {
                return Err(syn::Error::new_spanned(
                    other,
                    "#[command] 的参数必须是简单标识符（不支持解构模式）",
                ))
            }
        };
        let ty = &pt.ty;
        match classify(ty) {
            ParamKind::State => prelude.push(quote! { let #ident = __ctx.state(); }),
            ParamKind::App => prelude.push(quote! { let #ident = __ctx.app(); }),
            ParamKind::Channel => {
                let camel = to_camel(&name_str_of(&ident));
                let snake = name_str_of(&ident);
                prelude.push(quote! {
                    let #ident: #ty = __ctx.channel(#camel, #snake)?;
                });
            }
            ParamKind::Plain => {
                let camel = to_camel(&name_str_of(&ident));
                let snake = name_str_of(&ident);
                prelude.push(quote! {
                    let #ident: #ty = __ctx.arg(#camel, #snake)?;
                });
            }
        }
        call_args.push(quote!(#ident));
    }

    // 返回 AppResult<T> 的走 `?`；其它（如 `String`）直接交给 ctx.ok。
    let returns_result = match &func.sig.output {
        ReturnType::Default => false,
        ReturnType::Type(_, ty) => match &**ty {
            Type::Path(tp) => tp
                .path
                .segments
                .last()
                .is_some_and(|s| s.ident == "AppResult"),
            _ => false,
        },
    };

    let call = if func.sig.asyncness.is_some() {
        quote!(super::#name(#(#call_args),*).await)
    } else {
        quote!(super::#name(#(#call_args),*))
    };
    let call = if returns_result { quote!(#call?) } else { call };

    // 桌面：把属性转发给真 Tauri（保留调用点写 `#[tauri::command]` 的原样体验）。
    let passthrough = if attr.is_empty() {
        quote!(::tauri::command)
    } else {
        quote!(::tauri::command(#attr))
    };

    Ok(quote! {
        #[cfg_attr(feature = "desktop", #passthrough)]
        #(#attrs)*
        #vis #sig #block

        #[cfg(not(feature = "desktop"))]
        #[doc(hidden)]
        #[allow(non_snake_case)]
        pub mod #name {
            // 把父模块的一切拉进来：命令的形参类型（`UpdateCredentialArgs` 这类）
            // 都定义在父模块里，而这里是个**新模块**，不 import 就看不见。
            //
            // 用 glob 而不是逐条 import：形参类型是任意 token，宏没法知道哪些是
            // 本地类型、哪些来自 `use`。glob 两者都覆盖，且对未使用的导入不告警。
            #[allow(unused_imports)]
            use super::*;

            /// RPC 适配器：由 `#[command]` 按函数签名生成，别手改。
            pub fn call<'__ctx>(
                __ctx: &'__ctx crate::server::rpc::Ctx<'__ctx>,
            ) -> crate::server::rpc::RpcFuture<'__ctx> {
                ::std::boxed::Box::pin(async move {
                    #(#prelude)*
                    let __out = #call;
                    __ctx.ok(__out)
                })
            }
        }
    })
}

fn name_str_of(ident: &syn::Ident) -> String {
    ident.to_string()
}

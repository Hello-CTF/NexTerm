//! 服务端命令行（`nexterm-server` 的参数解析与「取令牌」动作）。
//!
//! # 为什么「不带子命令」必须是起服务
//!
//! 懒猫微包里 `ENTRYPOINT ["/usr/local/bin/nexterm-server"]` **一个参数都不带**
//! （见 `lazycat/image/Dockerfile`），容器与商店审核那条路完全依赖「裸启动 = 起服务」。
//! 所以子命令是可选的，而 `--listen` 这类选项全部放在顶层并标 `global`——写在
//! 子命令**前或后**都认，因为两种写法用户都会试。
//!
//! # 为什么每个选项都带 `env`
//!
//! 懒猫那条路是**用环境变量配的**（`NEXTERM_LISTEN` / `_DATA_DIR` / `_WEB_ROOT` /
//! `_MASTER_KEY`，见 `lzc-manifest.yml` 的 services 段），而 onlyServer 是在命令行里配的。
//! `env` 让两边共用同一份定义：**命令行 > 环境变量 > 内置默认**，懒猫那份 manifest
//! 因此一行不用改。
//!
//! # 为什么「取令牌」必须是子命令
//!
//! onlyServer 没有浏览器界面 —— 少了 `token`，用户在界面上根本看不到令牌，
//! 这个部署形态直接不可用（改数据目录里的 SQLite 显然不能算办法）。

use std::path::{Path, PathBuf};

use clap::{Parser, Subcommand};

use crate::server::{default_data_dir, default_listen, Options};

const AFTER_HELP: &str = "\
示例：
  # 完整版：浏览器界面 + 全部命令（懒猫微包容器走的就是这条，且不带任何参数）
  nexterm-server --listen 0.0.0.0:8080 --web-root /opt/nexterm/web

  # onlyServer：只做资产同步，放在公网服务器上当中转
  nexterm-server --sync-only --listen 0.0.0.0:9000 --data-dir /var/lib/nexterm \\
                 --master-key \"$(cat /etc/nexterm/master.key)\"

  # 取（或首次生成）同步令牌 —— 只把令牌写 stdout，可以直接命令替换
  TOKEN=$(nexterm-server token)

配置优先级：命令行 > 环境变量（NEXTERM_LISTEN / NEXTERM_DATA_DIR / NEXTERM_WEB_ROOT /
NEXTERM_MASTER_KEY）> 内置默认。";

#[derive(Parser, Debug)]
#[command(
    name = "nexterm-server",
    version,
    about = "NexTerm 服务端 —— 浏览器版（微服 / 自建）与 onlyServer（只做资产同步）",
    after_help = AFTER_HELP
)]
pub struct Cli {
    /// 省略子命令时就是 `serve`（容器那条路不带任何参数）。
    #[command(subcommand)]
    pub cmd: Option<Cmd>,

    /// 只做资产同步：只挂 /sync/rpc 与 /healthz，且只注册同步需要的三条命令。给「公网服务器上只当同步中转」用：浏览器界面与 /rpc 都不在，于是同步令牌能做的事被限制在资产读写 —— 这就是 onlyServer 的形态。
    #[arg(long, global = true)]
    pub sync_only: bool,

    /// 监听地址。默认 0.0.0.0:8080 是给容器用的（平台从容器网络另一侧访问），裸机上照默认跑会把服务暴露出去，请按需显式指定。
    #[arg(long, global = true, env = "NEXTERM_LISTEN", value_name = "IP:PORT")]
    pub listen: Option<std::net::SocketAddr>,

    /// 数据目录（SQLite、日志、令牌都落在这里）。
    #[arg(long, global = true, env = "NEXTERM_DATA_DIR", value_name = "DIR")]
    pub data_dir: Option<PathBuf>,

    /// 前端静态资源目录（构建产物 dist）。只在不带 --sync-only 时用得上。
    #[arg(long, global = true, env = "NEXTERM_WEB_ROOT", value_name = "DIR")]
    pub web_root: Option<PathBuf>,

    /// 凭据库根密钥。不提供则凭据库保持未初始化（密码类资产的同步会失败）。
    #[arg(long, global = true, env = "NEXTERM_MASTER_KEY", value_name = "KEY")]
    pub master_key: Option<String>,
}

// 上面每个 `///` 都是**一整行**（哪怕很长也不换行）。
//
// 原因：clap 把文档注释当帮助文本，而 Rust 会把连续的文档行用**空格**接起来 ——
// 中文句子被手工折行后，帮助里就会出现「浏览器界面与 /rpc 都不在， 于是…」
// 这种多出来的空格。同理，帮助文本里**不用 `**加粗**`**（它不解析 Markdown，
// 只会把星号原样打给用户看）。

#[derive(Subcommand, Debug, Clone, Copy, PartialEq, Eq)]
pub enum Cmd {
    /// 启动服务（默认动作）。
    Serve,
    /// 打印本实例的同步令牌；没有就生成一个（幂等，不会改动已有令牌）。
    Token,
    /// 重新生成同步令牌并打印 —— 旧令牌立即失效（怀疑令牌泄漏时用它）。
    #[command(name = "rotate-token")]
    RotateToken,
}

/// 解析结果对应的动作。
#[derive(Debug, Clone)]
pub enum Action {
    /// 起服务（不带子命令时的默认动作）。
    Serve(Options),
    /// 打印同步令牌。
    PrintToken {
        data_dir: PathBuf,
        /// `true` = 重新生成（`rotate-token`）。
        rotate: bool,
        /// 只影响那段「这令牌有多大权限」的说明文案。
        sync_only: bool,
    },
}

impl Cli {
    pub fn action(&self) -> Action {
        match self.cmd {
            Some(Cmd::Token) => Action::PrintToken {
                data_dir: self.resolve_data_dir(),
                rotate: false,
                sync_only: self.sync_only,
            },
            Some(Cmd::RotateToken) => Action::PrintToken {
                data_dir: self.resolve_data_dir(),
                rotate: true,
                sync_only: self.sync_only,
            },
            Some(Cmd::Serve) | None => Action::Serve(self.options()),
        }
    }

    /// 数据目录（命令行 > 环境变量 > 内置默认）。
    pub fn resolve_data_dir(&self) -> PathBuf {
        self.data_dir.clone().unwrap_or_else(default_data_dir)
    }

    /// 运行参数。
    pub fn options(&self) -> Options {
        Options {
            sync_only: self.sync_only,
            listen: self.listen.unwrap_or_else(default_listen),
            data_dir: self.resolve_data_dir(),
            web_root: self.web_root.clone(),
            master_key: self.master_key.clone(),
        }
    }
}

/// 打印（或重新生成）同步令牌。
///
/// **只把令牌写 stdout**，说明一律走 stderr，而且这条路**不初始化日志** ——
/// 用户要能写 `TOKEN=$(nexterm-server token)`，任何一行日志混进 stdout 都会把它污染掉。
pub async fn print_token(
    data_dir: &Path,
    rotate: bool,
    sync_only: bool,
) -> Result<(), Box<dyn std::error::Error>> {
    std::fs::create_dir_all(data_dir)?;
    // 走 `Store::open` 而不是直接读 SQLite：它会跑迁移，所以「全新机器上先取令牌、
    // 再启动服务」这条顺序也能用（第一次启动不会因为库还没建出来而失败）。
    let store = crate::store::Store::open(&data_dir.join("data.db")).await?;
    let token = if rotate {
        crate::sync::rotate_token(&store).await?
    } else {
        crate::sync::ensure_token(&store).await?
    };

    println!("{token}");

    if rotate {
        eprintln!("已重新生成同步令牌 —— 旧令牌立即失效，正在用它的对端会连不上。");
    }
    let scope = if sync_only {
        "只做资产同步（只认 sync_digest / sync_export / sync_import 三条命令，\
         没有浏览器界面，也没有 /rpc）"
    } else {
        "⚠️ 等价于本实例的完整控制权 —— 它调的是同一张命令表，能开终端、\
         读文件、起容器、读凭据库。别贴到公开的地方；只想「放到公网上当同步中转」，\
         请用 --sync-only 起服务"
    };
    eprintln!(
        "数据目录: {}\n权限说明: {scope}\n用法    : 对端填 `https://<本机地址>` 加这串令牌\
         （请求头 X-NexTerm-Sync-Token）",
        data_dir.display()
    );
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn parse(args: &[&str]) -> Cli {
        Cli::try_parse_from(std::iter::once("nexterm-server").chain(args.iter().copied()))
            .expect("这组参数应当能解析")
    }

    /// 懒猫容器的 `ENTRYPOINT` 不带任何参数 ⇒ 裸启动必须等于「起服务」。
    ///
    /// 这条守的是**已经在跑的那条路**：改坏了，微服上的应用会直接起不来。
    #[test]
    fn bare_invocation_means_serve() {
        match parse(&[]).action() {
            Action::Serve(o) => {
                assert!(!o.sync_only);
                // 环境变量存在时（比如有人 shell 里 export 过）不该拿内置默认值做断言。
                if std::env::var_os("NEXTERM_LISTEN").is_none() {
                    assert_eq!(o.listen, default_listen(), "默认监听地址变了");
                }
            }
            other => panic!("裸启动应当等价于 serve，实际是 {other:?}"),
        }
    }

    #[test]
    fn sync_only_reaches_options() {
        match parse(&["--sync-only", "serve"]).action() {
            Action::Serve(o) => assert!(o.sync_only),
            other => panic!("{other:?}"),
        }
        // `serve` 写不写都行，两种都要认同一件事。
        match parse(&["--sync-only"]).action() {
            Action::Serve(o) => assert!(o.sync_only),
            other => panic!("{other:?}"),
        }
    }

    /// 全局选项写在子命令**前或后**都要认 —— 两种写法用户都会试。
    #[test]
    fn data_dir_works_on_both_sides_of_the_subcommand() {
        for args in [
            vec!["--data-dir", "/tmp/nexterm-cli-test", "token"],
            vec!["token", "--data-dir", "/tmp/nexterm-cli-test"],
        ] {
            match parse(&args).action() {
                Action::PrintToken {
                    data_dir, rotate, ..
                } => {
                    assert_eq!(data_dir, PathBuf::from("/tmp/nexterm-cli-test"), "{args:?}");
                    assert!(!rotate, "{args:?}");
                }
                other => panic!("{args:?} → {other:?}"),
            }
        }
    }

    #[test]
    fn rotate_token_is_its_own_action() {
        match parse(&["rotate-token"]).action() {
            Action::PrintToken { rotate, .. } => assert!(rotate),
            other => panic!("{other:?}"),
        }
    }

    #[test]
    fn sync_only_is_visible_to_the_token_action() {
        match parse(&["--sync-only", "token"]).action() {
            Action::PrintToken { sync_only, .. } => assert!(sync_only),
            other => panic!("{other:?}"),
        }
    }

    /// 监听地址写错必须是**硬错误**。
    ///
    /// 回归测试：改之前的实现是「解析失败 → 打一条 warning → 退回 0.0.0.0:8080」，
    /// 放到命令行场景里就是「手滑打错一个字符，服务照常起来、还公开在 0.0.0.0 上」。
    #[test]
    fn malformed_listen_is_rejected() {
        let err = Cli::try_parse_from(["nexterm-server", "--listen", "127.0.0.1:9o80"])
            .expect_err("非法监听地址必须报错，不能静默退回默认值");
        let text = err.to_string();
        assert!(text.contains("listen"), "错误信息要指出是哪个参数：{text}");
    }
}

//! AI 护栏（§8.3）：风险分类 + 权限档位决策。
//!
//! **必须在工具层拦截，不能只靠提示词。** 第一层为确定性正则/前缀规则；
//! 第二层模型复核为可选增强（v1 默认关，接口预留）。
//!
//! 分两层看：
//!   · `classify_*` 只回答「这个动作有多危险」（风险，与用户设置无关）；
//!   · `decide_*` 再结合**档位**回答「这次放不放行」（决策）。
//! 拆开是因为风险是客观的、档位是用户选的 —— 混在一起后，
//! "改档位"和"改分类"会互相牵动，测试也没法单独写。

use serde::{Deserialize, Serialize};
use std::sync::OnceLock;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Risk {
    /// 只读、无副作用。
    Safe,
    /// 有副作用但可控、常见。
    NeedsConfirm,
    /// 危险命令（内置危险表或**用户自定义表**命中）—— 任何档位都要问用户。
    Danger,
    /// 硬底线 —— 任何档位都不执行，且**不询问**。
    ///
    /// 不询问是刻意的：弹了确认就等于给了入口，用户手滑一下世界就没了。
    /// 这一档也不允许用户配置（见 `HARD_FORBIDDEN`）。
    Forbidden,
}

/// 常规 AI 的权限档位。
///
/// ⚠️ 这里**不含终端接管**：接管是把终端整个交出去，属于完全权限，
/// 是独立模块（`takeover.rs`），既不随档位变化，也不因接管而改变档位。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum PermissionMode {
    /// 只读：只放行只读动作，任何写/执行**直接拒绝**（不是"多问几次"）。
    ReadOnly,
    /// 读写（默认）：只读放行；有副作用的动作问用户。
    #[default]
    ReadWrite,
    /// 完全静默：只有危险命令才问用户，其余一路放行。
    Silent,
}

/// 一次决策的结论。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Decision {
    Allow,
    Ask,
    Deny,
}

/// 权限配置：档位 + 用户自定义危险规则（存 sqlite）。
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PermissionConfig {
    pub mode: PermissionMode,
    /// 用户自定义的危险规则，**按子串匹配**（大小写不敏感）。
    ///
    /// 刻意不收正则：让用户写正则等于把"少个括号就整条失效"的风险
    /// 交给非工程用户；子串足够表达「kubectl delete」「我们的发布脚本」
    /// 这类意图，错了也能一眼看出来。
    #[serde(default)]
    pub danger_rules: Vec<String>,
}

impl Default for PermissionConfig {
    fn default() -> Self {
        Self {
            mode: PermissionMode::ReadWrite,
            danger_rules: Vec::new(),
        }
    }
}

/// 分类 + 决策的完整结论。
#[derive(Debug, Clone)]
pub struct Ruling {
    pub decision: Decision,
    pub risk: Risk,
    /// 风险类别（供「本会话允许此类」的记忆使用）。
    pub kind: Option<&'static str>,
    /// 说明文案：既要给用户看（确认卡片），也要给模型看（拒绝理由）。
    pub reason: String,
}

/// 档位 × 风险的决策表。
///
/// 顺序刻意写成「先看风险、再看档位」：硬底线与危险命令**优先于档位**，
/// 否则"完全静默"会连 `rm -rf /` 都放过去。
pub fn decide(mode: PermissionMode, risk: Risk) -> Decision {
    match risk {
        // 硬底线：与档位无关，永不执行、也不弹确认
        Risk::Forbidden => Decision::Deny,
        // 危险命令：静默档也照样问（探宝明确要求）
        Risk::Danger => match mode {
            PermissionMode::ReadOnly => Decision::Deny,
            _ => Decision::Ask,
        },
        // 只读动作：三档都放行
        Risk::Safe => Decision::Allow,
        Risk::NeedsConfirm => match mode {
            PermissionMode::ReadOnly => Decision::Deny,
            PermissionMode::ReadWrite => Decision::Ask,
            PermissionMode::Silent => Decision::Allow,
        },
    }
}

/// 风险类别名（用于“本会话允许此类”的记忆）。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RuleKind(pub &'static str);

pub const KIND_SUDO: RuleKind = RuleKind("sudo");
pub const KIND_WRITE_FS: RuleKind = RuleKind("write_fs");
pub const KIND_SERVICE: RuleKind = RuleKind("service");
pub const KIND_DOCKER_MUTATE: RuleKind = RuleKind("docker_mutate");
pub const KIND_PACKAGE: RuleKind = RuleKind("package");
pub const KIND_PROCESS: RuleKind = RuleKind("process");
pub const KIND_DB_WRITE: RuleKind = RuleKind("db_write");
pub const KIND_SHELL_PIPE: RuleKind = RuleKind("shell_pipe");
pub const KIND_PERM: RuleKind = RuleKind("perm");
pub const KIND_UNKNOWN: RuleKind = RuleKind("unknown");

/// 危险命令的类别名（静默档也照样问的那一类）。
pub const KIND_DANGER: &str = "danger";

/// 分类结果：风险级别 + 类别（供会话级放行记忆使用）。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Verdict {
    pub risk: Risk,
    pub kind: Option<&'static str>,
    /// 命中的规则说明（给确认卡片展示）。
    ///
    /// 是 `String` 而不是 `&'static str`：用户自定义规则命中时要带上规则原文，
    /// 那是运行期数据，静态串装不下。
    pub reason: String,
}

impl Verdict {
    fn safe() -> Self {
        Self {
            risk: Risk::Safe,
            kind: None,
            reason: "只读白名单命令".into(),
        }
    }
    fn confirm(kind: &'static str, reason: &'static str) -> Self {
        Self {
            risk: Risk::NeedsConfirm,
            kind: Some(kind),
            reason: reason.into(),
        }
    }
    fn danger(reason: &'static str) -> Self {
        Self {
            risk: Risk::Danger,
            kind: Some(KIND_DANGER),
            reason: reason.into(),
        }
    }
    fn danger_dyn(reason: String) -> Self {
        Self {
            risk: Risk::Danger,
            kind: Some(KIND_DANGER),
            reason,
        }
    }
    fn forbid(reason: &'static str) -> Self {
        Self {
            risk: Risk::Forbidden,
            kind: None,
            reason: reason.into(),
        }
    }
}

struct Rules {
    /// 硬底线：不可配置，任何档位都不执行。
    forbidden: Vec<(regex::Regex, &'static str)>,
    /// 危险命令（内置表）：任何档位都要问用户。
    danger: Vec<(regex::Regex, &'static str)>,
    confirm: Vec<(regex::Regex, &'static str, &'static str)>,
    safe_prefixes: Vec<&'static str>,
    safe_first_tokens: Vec<&'static str>,
}

/// 永不匹配的占位正则（`[^\s\S]` 逻辑上无字符可命中）。
///
/// **这是全项目唯一允许的 `unwrap` 落点**，依据 §0.3 明文例外「编译期常量解析」。
fn never_match_re() -> &'static regex::Regex {
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    RE.get_or_init(|| regex::Regex::new(r"[^\s\S]").unwrap())
}

/// 编译期常量正则的统一编译入口：不可编译时退化为「永不匹配」。
///
/// 传入的 pattern 全部是源码字面量，编译期即恒定，失败路径运行时不可达；
/// 收敛到一处是为了审查时一眼确认 §0.3 的例外范围。
/// 退化而非 panic，是为了绝不在安全关键路径（分类命令）上崩掉内核。
fn const_re(pattern: &str) -> regex::Regex {
    regex::Regex::new(pattern).unwrap_or_else(|_| never_match_re().clone())
}

fn word_boundary_re(pattern: &str) -> regex::Regex {
    // 危险规则对命令首段做宽松匹配：忽略 sudo/env 前缀后的主体
    const_re(pattern)
}

fn rules() -> &'static Rules {
    static RULES: OnceLock<Rules> = OnceLock::new();
    RULES.get_or_init(|| {
        // ── 硬底线：任何档位都不执行，也不弹确认，用户不可配置 ──
        //
        // 判据是「不可逆 + 大概率不是本意」：删库、格式化、块设备直写、fork 炸弹。
        // 这些东西弹确认没有意义 —— 用户手滑点一下的代价太大，不如连入口都不给。
        let hard_forbidden: &[(&str, &'static str)] = &[
            // rm 递归作用于根/家/关键目录
            (
                r#"(?i)\brm\b[^|;&]*(?:^|[\s])-{1,2}[a-z-]*(r|--recursive)[a-z]*[\s]+(?:['"])?(/~|/\*|/etc|/usr|/var|/boot|/bin|/sbin|/lib|/opt|/dev|/proc|/sys|[a-z]:\\|[a-z]:/|/)(?:['"])?(\s|/|\*|$)"#,
                "递归删除根/系统目录",
            ),
            (r"(?i)\bmkfs(\.\w+)?\b", "格式化文件系统"),
            (r"(?i)\bdd\b[^|;&]*\bof=/dev/", "dd 直写块设备"),
            (r"(?i)>\s*/dev/(sd[a-z]|nvme|hd[a-z])", "重定向覆写块设备"),
            (r"(?i)\bformat\b[^|;&]*[a-z]:|\bformat\s+/|\bformat\s+/q", "Windows format 格式化"),
            (
                r"(?i)\b(rd|rmdir)\b[^|;&]*\/s[^|;&]*\s+[a-z]:?(\\|/)?\s*$|\bdel\b[^|;&]*\/[sq][^|;&]*\s+[a-z]:\\",
                "Windows 递归删除盘根",
            ),
            (r"(?i)\bdrop\s+(database|schema)\b", "DROP DATABASE/SCHEMA"),
            (r":\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;", "fork 炸弹"),
        ];

        // ── 危险命令：用户可能真的要做，所以**问**而不是**拒** ──
        //
        // 静默档也照样问这一批 —— 这是「完全静默」这个档位的边界所在。
        let danger_patterns: &[(&str, &'static str)] = &[
            (
                r"(?i)\b(shutdown|poweroff|halt)\b|\breboot\b|\binit\s+[06]\b",
                "关机/重启",
            ),
            (
                r"(?i)\bdocker\s+rm\b[^|;&]*-(f|force)[^|;&]*(\$\(|--all|-a\b|\*)",
                "docker 强制删除全部容器",
            ),
            (
                r"(?i)\bdocker\s+(system\s+)?prune\b[^|;&]*-(a|all)\b",
                "docker prune 全量清理",
            ),
            (
                r"(?i)\bchmod\b[^|;&]*-R\b?[^|;&]*\s+777\s+/(?:\s|$)",
                "对根做 777",
            ),
            (r"(?i)\btruncate\s+table\b", "TRUNCATE TABLE"),
        ];

        // ── NeedsConfirm ──
        let confirm_patterns: &[(&str, &'static str, &'static str)] = &[
            (r"(?i)(^|\s)(sudo|doas)\b", "sudo", "sudo 提权执行"),
            (r"(?i)\bsu\b\s", "sudo", "切换用户身份"),
            (
                r"(?i)\bsystemctl\b\s+(restart|stop|start|reload|mask|kill|disable|enable)\b",
                "service",
                "systemd 服务变更",
            ),
            (
                r"(?i)\bservice\b\s+\S+\s+(restart|stop|start|reload)\b",
                "service",
                "init 服务变更",
            ),
            (
                r"(?i)\bdocker\s+(start|stop|restart|pause|unpause|kill|rm|rmi|tag|commit|cp)\b",
                "docker_mutate",
                "docker 容器/镜像变更",
            ),
            (r"(?i)\bdocker\s+volume\s+(rm|prune)\b", "docker_mutate", "docker 卷删除"),
            (
                r"(?i)\b(apt|apt-get|yum|dnf|zypper|pacman)\b\s+\S*(install|remove|purge|upgrade|autoremove|erase)",
                "package",
                "系统包管理变更",
            ),
            (r"(?i)\bdpkg\s+(--)?(install|remove|purge)\b|\brpm\s+(-e|-i|-U)", "package", "包安装/卸载"),
            (r"(?i)\b(kill|pkill|killall)\b", "process", "终止进程"),
            (r"(?i)\bcrontab\b", "perm", "修改计划任务"),
            (r"(?i)\b(useradd|userdel|usermod|groupadd|passwd|chpasswd)\b", "perm", "用户/口令管理"),
            (
                r"(?i)\b(chmod|chown|chattr|setfacl)\b",
                "perm",
                "权限/属主变更",
            ),
            (
                r"(?i)\b(mv|cp|rsync|tee|install)\b[^|;&]*\s+/(etc|usr|boot|bin|sbin|lib|opt|var)/",
                "write_fs",
                "写系统目录",
            ),
            (r"(?i)(>>?|tee\s+)\s*/(etc|usr|boot|bin|sbin)/", "write_fs", "重定向写系统目录"),
            (
                r"(?i)\b(curl|wget)\b[^|;&]*\|\s*(sudo\s+)?(ba|z|k)?sh\b",
                "shell_pipe",
                "下载脚本直接执行",
            ),
            (r"(?i)\b(curl|wget)\b[^|;&]*\|\s*bash\s", "shell_pipe", "下载脚本直接执行"),
            (
                r"(?i)\b(wget|curl)\b[^|;&]*\s+-O\s*/(etc|usr|bin|sbin|boot)/",
                "write_fs",
                "下载写入系统目录",
            ),
            (r"(?i)\bgit\s+push\b[^|;&]*--force\b|\bgit\s+push\s+-f\b", "write_fs", "git 强推"),
            (
                r"(?i)\b(iptables|ufw|firewall-cmd|nft)\b",
                "service",
                "防火墙规则变更",
            ),
            (
                r"(?i)\b(insert\s+into|update\s+\w+\s+set|delete\s+from|alter\s+table|create\s+(table|database|user)|grant|revoke|drop\s+table|drop\s+index|rename\s+table|call\s|merge\s+into|replace\s+into|load\s+data)\b",
                "db_write",
                "数据库写操作",
            ),
        ];

        // 只读白名单：首 token 完全匹配即 Safe
        let safe_first_tokens: &[&str] = &[
            "ls", "ll", "pwd", "cat", "head", "tail", "tac", "nl", "grep", "egrep", "fgrep",
            "rg", "awk", "sed", "-n", "cut", "sort", "uniq", "wc", "find", "stat", "file",
            "tree", "diff", "comm", "jq", "xxd", "hexdump", "od", "strings",
            "ps", "pidof", "df", "du", "free", "vmstat", "iostat", "lscpu", "lsblk",
            "uname", "whoami", "id", "hostname", "hostnamectl", "uptime", "date", "cal",
            "env", "printenv", "which", "whereis", "type", "alias", "echo", "printf",
            "dmesg", "journalctl", "ss", "netstat", "ip", "ifconfig", "arp", "route",
            "ping", "traceroute", "dig", "nslookup", "host", "curl", "wget", "nc", "ncat",
            "lsof", "systemctl", "service", "top", "htop", "atop", "sar", "w", "who",
            "last", "history", "docker", "kubectl", "git", "md5sum", "sha1sum", "sha256sum",
            "tar", "gunzip", "gzip", "zcat", "bzip2", "xz", "unzip", "zip", "zipinfo",
            "mysql", "redis-cli", "python3", "python", "node", "node --version", "bash",
            "sh", "vim", "vi", "nano", "less", "more", "clear", "true", "false", "sleep",
            "touch", "mkdir", "basename", "dirname", "readlink", "realpath", "getent",
            "ulimit", "tput", "resize", "lsb_release", "arch", "nproc",
            "select", "show", "describe", "desc", "explain", "use", "cd", "pushd", "popd",
        ];

        // 前缀匹配（带参数的复合只读形态）
        let safe_prefixes: &[&str] = &[
            "systemctl status ",
            "systemctl show ",
            "systemctl list-units",
            "systemctl list-unit-files",
            "systemctl is-active ",
            "systemctl is-enabled ",
            "systemctl --failed",
            "systemctl --type=service",
            "docker ps",
            "docker images",
            "docker image ls",
            "docker logs",
            "docker inspect",
            "docker stats --no-stream",
            "docker version",
            "docker info",
            "docker port",
            "docker top",
            "docker compose ps",
            "docker compose config",
            "docker compose logs",
            "docker compose ls",
            "docker network ls",
            "docker network inspect",
            "docker volume ls",
            "docker volume inspect",
            "git status",
            "git log",
            "git diff",
            "git show",
            "git branch",
            "git remote",
            "kubectl get",
            "kubectl describe",
            "kubectl logs",
            "kubectl top",
            "mysql --version",
            "redis-cli ping",
            "redis-cli info",
            "redis-cli --scan",
        ];

        Rules {
            forbidden: hard_forbidden
                .iter()
                .map(|(p, reason)| (word_boundary_re(p), *reason))
                .collect(),
            danger: danger_patterns
                .iter()
                .map(|(p, reason)| (word_boundary_re(p), *reason))
                .collect(),
            confirm: confirm_patterns
                .iter()
                .map(|(p, kind, reason)| (word_boundary_re(p), *kind, *reason))
                .collect(),
            safe_prefixes: safe_prefixes.to_vec(),
            safe_first_tokens: safe_first_tokens.to_vec(),
        }
    })
}

fn rules_pipe_exec() -> &'static regex::Regex {
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    RE.get_or_init(|| const_re(r"(?i)\b(curl|wget)\b[^|;&]*\|\s*(sudo\s+)?(ba|z|k)?sh\b"))
}

/// SQL 注释剥离正则（`--` 行注释与 `/* */` 块注释）。
fn rules_sql_comment() -> &'static regex::Regex {
    static RE: OnceLock<regex::Regex> = OnceLock::new();
    RE.get_or_init(|| const_re(r"(?s)(--[^\n]*|/\*.*?\*/)"))
}

/// 规范化命令：去掉首尾空白与前导环境变量赋值/免密前缀，压缩空白。
pub fn normalize_command(raw: &str) -> String {
    let mut s = raw.trim().to_string();
    // 去掉前导 env 赋值（如 FOO=bar cmd）
    loop {
        let trimmed = s.trim_start();
        if let Some(sp) = trimmed.find(char::is_whitespace) {
            let head = &trimmed[..sp];
            if head.contains('=') && !head.starts_with('-') && !head.starts_with('/') {
                s = trimmed[sp..].trim_start().to_string();
                continue;
            }
        }
        s = trimmed.to_string();
        break;
    }
    s
}

/// 对单条 shell 命令分级（第一层规则引擎，仅内置规则）。
pub fn classify_command(raw: &str) -> Verdict {
    classify_command_with(raw, &[])
}

/// 同上，但带上用户自定义的危险规则。
pub fn classify_command_with(raw: &str, extra_danger: &[String]) -> Verdict {
    let cmd = normalize_command(raw);
    if cmd.is_empty() {
        return Verdict::safe();
    }
    let r = rules();

    // 整行检测：下载管道执行（分段后 | 两侧信息会丢失，§6.5）
    let low_full = cmd.to_ascii_lowercase();
    let pipe_re = rules_pipe_exec();
    if pipe_re.is_match(&low_full) {
        return Verdict::confirm(KIND_SHELL_PIPE.0, "下载脚本直接执行");
    }

    // 段级检查：按 ; | && || 分隔逐段判（危险段决定整体）
    for seg in split_segments(&cmd) {
        if seg.is_empty() {
            continue;
        }
        if let Some(v) = classify_segment(&seg, r, extra_danger) {
            return v;
        }
    }
    // 没命中危险/确认规则时：白名单判定
    if is_whitelisted(&cmd, r) {
        Verdict::safe()
    } else {
        Verdict::confirm(KIND_UNKNOWN.0, "未在只读白名单中，默认需要确认")
    }
}

/// 把复合命令切成段，剥掉每段的 sudo/env 前缀。
fn split_segments(cmd: &str) -> Vec<String> {
    let mut segs = Vec::new();
    let mut cur = String::new();
    let mut chars = cmd.chars().peekable();
    let mut in_quote: Option<char> = None;
    while let Some(c) = chars.next() {
        match in_quote {
            Some(q) => {
                cur.push(c);
                if c == q {
                    in_quote = None;
                }
            }
            None => {
                match c {
                    '\'' | '"' => {
                        in_quote = Some(c);
                        cur.push(c);
                    }
                    ';' | '|' => {
                        // 处理 || 与 &&
                        if c == '|' {
                            if chars.peek() == Some(&'|') {
                                chars.next();
                            }
                        } else if chars.peek() == Some(&'&') {
                            chars.next();
                        }
                        segs.push(std::mem::take(&mut cur));
                    }
                    _ => cur.push(c),
                }
            }
        }
    }
    segs.push(cur);
    segs.into_iter()
        .map(|s| {
            let mut t = normalize_command(&s);
            // 剥 sudo 前缀，让主体命令也能进入后续判定
            loop {
                let low = t.to_ascii_lowercase();
                if low.starts_with("sudo ") || low.starts_with("doas ") {
                    // 上面已确认前缀带空格，此处 find 必命中；用 unwrap_or 兜底以满足 §0.3。
                    let cut = low.find(' ').unwrap_or(low.len());
                    t = t[cut..].trim_start().to_string();
                    continue;
                }
                break;
            }
            t
        })
        .collect()
}

fn classify_segment(seg: &str, r: &'static Rules, extra: &[String]) -> Option<Verdict> {
    let low = seg.to_ascii_lowercase();
    // 1) 硬底线：最高优先级
    for (re, reason) in &r.forbidden {
        if re.is_match(&low) {
            return Some(Verdict::forbid(reason));
        }
    }
    // 2) 用户自定义危险规则：**优先于内置规则** ——
    //    用户加规则就是为了盖过默认判断，排在后面等于白加。
    if let Some(hit) = matches_user_danger(&low, extra) {
        return Some(Verdict::danger_dyn(format!("命中自定义危险规则「{hit}」")));
    }
    // 3) 内置危险表
    for (re, reason) in &r.danger {
        if re.is_match(&low) {
            return Some(Verdict::danger(reason));
        }
    }
    // 4) 常规需确认
    for (re, kind, reason) in &r.confirm {
        if re.is_match(&low) {
            return Some(Verdict::confirm(kind, reason));
        }
    }
    None
}

/// 用户自定义危险规则：**子串匹配**（大小写不敏感），返回命中的那条原文。
///
/// 用子串而不是正则 —— 让用户写正则等于把"少个括号就整条失效"的风险
/// 交给非工程用户；子串足够表达「kubectl delete」「我们的发布脚本」这类意图，
/// 而且写错了能一眼看出来。
fn matches_user_danger(cmd_lower: &str, extra: &[String]) -> Option<String> {
    extra
        .iter()
        .map(|s| s.trim())
        .filter(|s| !s.is_empty())
        .find(|s| cmd_lower.contains(&s.to_ascii_lowercase()))
        .map(str::to_string)
}

fn is_whitelisted(cmd: &str, r: &'static Rules) -> bool {
    let low = cmd.to_ascii_lowercase();
    if r.safe_prefixes.iter().any(|p| low.starts_with(p)) {
        return true;
    }
    let first = low.split_whitespace().next().unwrap_or("");
    let base = first.rsplit(['/', '\\']).next().unwrap_or(first);
    r.safe_first_tokens.contains(&base)
}

/// 风险严重度排序，用于「复合命令取最严的那段」。
fn rank(r: Risk) -> u8 {
    match r {
        Risk::Safe => 0,
        Risk::NeedsConfirm => 1,
        Risk::Danger => 2,
        Risk::Forbidden => 3,
    }
}

/// 工具级分类（§8.3）：结合工具名与参数。
pub fn classify_tool(tool: &str, args: &serde_json::Value) -> Verdict {
    classify_tool_with(tool, args, &[])
}

/// 同上，但带上用户自定义的危险规则。
pub fn classify_tool_with(
    tool: &str,
    args: &serde_json::Value,
    extra_danger: &[String],
) -> Verdict {
    match tool {
        "exec_commands" => {
            let cmds = args
                .get("commands")
                .and_then(|v| v.as_array())
                .cloned()
                .unwrap_or_default();
            let mut worst = Verdict::safe();
            for c in cmds.iter().filter_map(|v| v.as_str()) {
                let v = classify_command_with(c, extra_danger);
                if rank(v.risk) > rank(worst.risk) {
                    worst = v;
                }
                if worst.risk == Risk::Forbidden {
                    return worst;
                }
            }
            worst
        }
        "read_file" | "list_dir" | "search_files" | "read_screen" | "wait_for" | "list_assets"
        | "docker_ps" | "docker_logs" | "docker_inspect" | "db_list_tables" | "db_describe"
        | "list_processes" | "list_ports" | "get_context" => Verdict::safe(),
        "write_file" => Verdict::confirm(KIND_WRITE_FS.0, "写入远端文件（保存前自动备份）"),
        // edit_file 与 write_file 同属「改远端文件」，用同一个 kind ——
        // 用户允许过写文件之后，编辑不该再问一遍。
        "edit_file" => Verdict::confirm(KIND_WRITE_FS.0, "编辑远端文件（保存前自动备份）"),
        // 这两条只动「本次任务自己的状态」，不碰远端一个字节。
        // 每次更新清单都弹确认卡，只会把用户训练成无脑点允许。
        "todo_write" | "exit_plan_mode" => Verdict::safe(),
        "send_keys" => {
            let keys = args.get("keys").and_then(|v| v.as_str()).unwrap_or("");
            if args.get("enter").and_then(|v| v.as_bool()).unwrap_or(false) {
                classify_command_with(keys, extra_danger)
            } else {
                // 不带回车的按键（Tab/方向键、往行内补字）视为低风险
                Verdict::safe()
            }
        }
        "docker_exec" => Verdict::confirm(KIND_DOCKER_MUTATE.0, "在容器内执行命令"),
        "docker_control" => {
            let action = args.get("action").and_then(|v| v.as_str()).unwrap_or("");
            match action {
                "start" | "stop" | "restart" | "pause" | "unpause" | "rename" => {
                    Verdict::confirm(KIND_DOCKER_MUTATE.0, "容器生命周期变更")
                }
                "remove" => Verdict::confirm(KIND_DOCKER_MUTATE.0, "删除容器"),
                _ => Verdict::confirm(KIND_DOCKER_MUTATE.0, "容器操作"),
            }
        }
        "db_query" => {
            let sql = args.get("sql").and_then(|v| v.as_str()).unwrap_or("");
            let v = classify_sql(sql);
            if v.risk == Risk::Safe {
                Verdict::safe()
            } else {
                v
            }
        }
        "redis_command" => {
            let args0 = args
                .get("args")
                .and_then(|v| v.as_array())
                .and_then(|a| a.first())
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_ascii_uppercase();
            match args0.as_str() {
                "FLUSHALL" | "FLUSHDB" => Verdict::forbid("Redis 全库清空"),
                "GET" | "MGET" | "SCAN" | "KEYS" | "TYPE" | "TTL" | "PTTL" | "EXISTS" | "HGET"
                | "HGETALL" | "HMGET" | "LRANGE" | "LLEN" | "SMEMBERS" | "SCARD" | "SISMEMBER"
                | "ZRANGE" | "ZCARD" | "ZSCORE" | "STRLEN" | "INFO" | "DBSIZE" | "PING"
                | "OBJECT" | "RANDOMKEY" | "MEMORY" | "CLIENT" | "XLEN" | "XRANGE" | "HLEN"
                | "LINDEX" | "BITCOUNT" | "GETRANGE" => Verdict::safe(),
                _ => Verdict::confirm(KIND_DB_WRITE.0, "Redis 写命令"),
            }
        }
        "ask_user" | "open_tab" => Verdict::safe(),
        _ => Verdict::confirm(KIND_UNKNOWN.0, "未知工具，默认需要确认"),
    }
}

/// 分类 + 决策一次完成：调用方不需要知道「风险 × 档位」怎么组合。
///
/// 抽成一个入口是为了让 agent 侧只有**一条**判断路径 ——
/// 散着写 `match risk` 的话，档位一多就会变成一坨互相缠绕的分支，
/// 而且每加一档都要回头改三处。
pub fn judge(
    mode: PermissionMode,
    cfg_danger: &[String],
    tool: &str,
    args: &serde_json::Value,
) -> Ruling {
    let v = classify_tool_with(tool, args, cfg_danger);
    let decision = decide(mode, v.risk);
    // 拒绝时把「为什么」说清楚：模型要据此换路子，用户要知道是档位还是底线拦的
    let reason = match decision {
        Decision::Deny => match v.risk {
            Risk::Forbidden => format!("{}（硬底线，任何档位都不执行）", v.reason),
            _ => format!("{}（当前是「只读」档，需先切换档位）", v.reason),
        },
        _ => v.reason.clone(),
    };
    Ruling {
        decision,
        risk: v.risk,
        kind: v.kind,
        reason,
    }
}

/// SQL 只读判定。
pub fn classify_sql(sql: &str) -> Verdict {
    let low = sql.trim().to_ascii_lowercase();
    // 正则在 OnceLock 里只编译一次（原先每次分类都重新编译）。
    let low = rules_sql_comment().replace_all(&low, "").to_string();
    let first = low.split_whitespace().next().unwrap_or("");
    match first {
        "select" | "show" | "describe" | "desc" | "explain" | "use" | "set" => {
            // SET 仅会话变量；SHOW/DESC/EXPLAIN 只读
            if first == "set" {
                Verdict::confirm(KIND_DB_WRITE.0, "SET 变更需确认")
            } else {
                Verdict::safe()
            }
        }
        "drop" => {
            if low.contains(" database ") || low.contains(" schema ") {
                Verdict::forbid("DROP DATABASE/SCHEMA")
            } else {
                Verdict::confirm(KIND_DB_WRITE.0, "DROP 语句")
            }
        }
        "" => Verdict::safe(),
        _ => {
            let v = classify_command(&low);
            if v.risk == Risk::Forbidden {
                v
            } else {
                Verdict::confirm(KIND_DB_WRITE.0, "非只读 SQL")
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn safe(cmd: &str) -> bool {
        classify_command(cmd).risk == Risk::Safe
    }
    fn confirm(cmd: &str) -> bool {
        classify_command(cmd).risk == Risk::NeedsConfirm
    }
    fn danger(cmd: &str) -> bool {
        classify_command(cmd).risk == Risk::Danger
    }
    fn forbid(cmd: &str) -> bool {
        classify_command(cmd).risk == Risk::Forbidden
    }

    #[test]
    fn t01_readonly_whitelist_is_safe() {
        assert!(safe("ls -la /var/log"));
        assert!(safe("cat /etc/hostname"));
        assert!(safe("grep -rn ERROR /var/log/app.log"));
        assert!(safe("ps aux | grep nginx"));
        assert!(safe("df -h"));
        assert!(safe("du -sh /data"));
        assert!(safe("uname -a"));
        assert!(safe("docker ps -a"));
        assert!(safe("docker logs --tail 100 web"));
        assert!(safe("journalctl -n 100 -u nginx"));
        assert!(safe("systemctl status nginx"));
        assert!(safe("SELECT * FROM users LIMIT 10"));
    }

    #[test]
    fn t02_rm_rf_root_is_forbidden() {
        assert!(forbid("rm -rf /"));
        assert!(forbid("rm -fr / "));
        assert!(forbid("sudo rm -rf /"));
        assert!(forbid("rm -r /"));
        assert!(forbid("rm -rf /*"));
        assert!(forbid("cd /tmp; rm -rf / --no-preserve-root"));
    }

    #[test]
    fn t03_rm_targeted_dirs_forbidden_but_home_file_ok() {
        assert!(forbid("rm -rf /etc/nginx"));
        assert!(forbid("rm -rf /usr/bin"));
        // 普通文件删除：不在白名单 -> 需确认（不禁止）
        assert!(confirm("rm /tmp/a.txt"));
    }

    #[test]
    fn t04_mkfs_dd_forbidden_shutdown_is_danger() {
        // 格式化 / 块设备直写：硬底线，永不执行
        assert!(forbid("mkfs.ext4 /dev/sda1"));
        assert!(forbid("dd if=/dev/zero of=/dev/sda"));
        // 关机重启：危险但用户可能真的要做 —— 问，不拒
        assert!(danger("shutdown -h now"));
        assert!(danger("reboot"));
        assert!(danger("init 0"));
        assert!(danger("init 6"));
    }

    #[test]
    fn t05_format_and_windows_danger() {
        assert!(forbid("format C: /q"));
        assert!(forbid("del /s /q C:\\Users"));
        assert!(forbid("rd /s /q C:\\"));
    }

    #[test]
    fn t06_drop_database_forbidden_drop_table_confirm() {
        assert!(forbid("DROP DATABASE prod"));
        assert!(forbid("drop schema app"));
        assert!(confirm("DROP TABLE users"));
    }

    #[test]
    fn t07_sudo_always_confirm() {
        assert!(confirm("sudo systemctl restart nginx"));
        assert!(confirm("sudo apt install curl"));
        assert!(confirm("sudo rm /tmp/a"));
    }

    #[test]
    fn t08_service_restart_confirm() {
        assert!(confirm("systemctl restart nginx"));
        assert!(confirm("systemctl stop firewalld"));
        assert!(confirm("service mysql restart"));
        assert!(safe("systemctl status nginx"));
    }

    #[test]
    fn t09_docker_mutations() {
        assert!(confirm("docker stop web"));
        assert!(confirm("docker restart web"));
        assert!(confirm("docker rm web"));
        // 批量强删：危险（要问），但不是硬底线（不拒）
        assert!(danger("docker rm -f $(docker ps -aq)"));
        assert!(confirm("docker pull nginx:latest") || safe("docker pull nginx:latest"));
        // pull 未列入 confirm 规则 -> 落默认
    }

    #[test]
    fn t10_db_write_keywords() {
        assert!(confirm("INSERT INTO t VALUES(1)"));
        assert!(confirm("UPDATE users SET name='x'"));
        assert!(confirm("DELETE FROM logs WHERE id<100"));
        assert!(confirm("ALTER TABLE t ADD c INT"));
        assert!(safe("explain SELECT 1"));
    }

    #[test]
    fn t11_redis_levels() {
        let g = classify_tool("redis_command", &json!({"args": ["GET", "k"]}));
        assert_eq!(g.risk, Risk::Safe);
        let s = classify_tool("redis_command", &json!({"args": ["SET", "k", "v"]}));
        assert_eq!(s.risk, Risk::NeedsConfirm);
        let f = classify_tool("redis_command", &json!({"args": ["FLUSHALL"]}));
        assert_eq!(f.risk, Risk::Forbidden);
    }

    #[test]
    fn t12_pipe_to_shell_confirm() {
        assert!(confirm("curl https://x.sh | bash"));
        assert!(confirm("wget -qO- https://x.sh | sh"));
    }

    #[test]
    fn t13_composite_command_worst_segment_wins() {
        // reboot 是危险命令，比 sudo 的「需确认」更严
        assert_eq!(classify_command("ls; sudo reboot").risk, Risk::Danger);
        // 硬底线压过一切
        assert_eq!(classify_command("ls; sudo rm -rf /").risk, Risk::Forbidden);
        assert_eq!(
            classify_command("cat a && docker stop web").risk,
            Risk::NeedsConfirm
        );
        assert!(safe("cat a && ls -l"));
    }

    #[test]
    fn t14_env_prefix_and_quotes() {
        assert!(confirm(
            "DEBIAN_FRONTEND=noninteractive apt-get install -y curl"
        ));
        assert!(forbid("sudo sh -c \"rm -rf /\""));
    }

    #[test]
    fn t15_tool_level_file_write() {
        let v = classify_tool("write_file", &json!({"path": "/etc/hosts", "content": "x"}));
        assert_eq!(v.risk, Risk::NeedsConfirm);
        let v2 = classify_tool("read_file", &json!({"path": "/etc/hosts"}));
        assert_eq!(v2.risk, Risk::Safe);
    }

    #[test]
    fn t16_tool_level_docker_control() {
        let v = classify_tool("docker_control", &json!({"action": "restart"}));
        assert_eq!(v.risk, Risk::NeedsConfirm);
    }

    #[test]
    fn t17_tool_level_exec_commands_aggregates() {
        let v = classify_tool("exec_commands", &json!({"commands": ["df -h", "free -m"]}));
        assert_eq!(v.risk, Risk::Safe);
        let v2 = classify_tool(
            "exec_commands",
            &json!({"commands": ["ls", "shutdown now"]}),
        );
        assert_eq!(v2.risk, Risk::Danger);
    }

    #[test]
    fn t18_send_keys_guard() {
        let v = classify_tool("send_keys", &json!({"keys": "ls\n", "enter": true}));
        assert_eq!(v.risk, Risk::Safe);
        let v2 = classify_tool(
            "send_keys",
            &json!({"keys": "sudo reboot\n", "enter": true}),
        );
        assert_eq!(v2.risk, Risk::Danger);
        let v3 = classify_tool("send_keys", &json!({"keys": "y"}));
        assert_eq!(v3.risk, Risk::Safe);
    }

    #[test]
    fn t19_unknown_defaults_to_confirm() {
        assert!(confirm("make install"));
        assert!(confirm("./deploy.sh"));
        // 但不禁止
        assert!(!forbid("./deploy.sh"));
    }

    #[test]
    fn t20_kill_and_perms_confirm() {
        assert!(confirm("kill -9 1234"));
        assert!(confirm("chmod 777 /data/app"));
        assert!(confirm("chown www-data /var/www"));
        assert!(confirm("crontab -e"));
    }

    #[test]
    fn t21_empty_and_whitespace() {
        assert!(safe(""));
        assert!(safe("   "));
        assert!(safe("cd /var/log"));
    }

    #[test]
    fn t22_case_insensitive() {
        assert!(forbid("RM -RF /"));
        assert!(confirm("SUDO apt install htop"));
    }

    /* ── 权限档位（本轮新增）────────────────────────────────────────── */

    #[test]
    fn t23_mode_readonly_denies_everything_mutating() {
        use PermissionMode::*;
        assert_eq!(decide(ReadOnly, Risk::Safe), Decision::Allow);
        // 只读档不是"多问几次"，是**直接不让做**
        assert_eq!(decide(ReadOnly, Risk::NeedsConfirm), Decision::Deny);
        assert_eq!(decide(ReadOnly, Risk::Danger), Decision::Deny);
        assert_eq!(decide(ReadOnly, Risk::Forbidden), Decision::Deny);
    }

    #[test]
    fn t24_silent_only_asks_on_danger() {
        use PermissionMode::*;
        // 读写：只读放行、有副作用要问
        assert_eq!(decide(ReadWrite, Risk::Safe), Decision::Allow);
        assert_eq!(decide(ReadWrite, Risk::NeedsConfirm), Decision::Ask);
        assert_eq!(decide(ReadWrite, Risk::Danger), Decision::Ask);
        // 静默：日常动作不再打扰……
        assert_eq!(decide(Silent, Risk::Safe), Decision::Allow);
        assert_eq!(decide(Silent, Risk::NeedsConfirm), Decision::Allow);
        // ……但危险命令照样问 —— 这是静默档唯一保留的那道刹车
        assert_eq!(decide(Silent, Risk::Danger), Decision::Ask);
    }

    #[test]
    fn t25_hard_forbidden_never_asks_in_any_mode() {
        use PermissionMode::*;
        // 硬底线在任何档位都是「拒绝」而不是「询问」——
        // 弹了确认就等于给了入口
        for m in [ReadOnly, ReadWrite, Silent] {
            assert_eq!(decide(m, Risk::Forbidden), Decision::Deny);
        }
    }

    /* ── 用户自定义危险规则 ─────────────────────────────────────────── */

    #[test]
    fn t26_user_rules_take_priority_and_are_case_insensitive() {
        let rules = vec!["kubectl delete".to_string()];
        let v = classify_command_with("kubectl delete pod web-1", &rules);
        assert_eq!(v.risk, Risk::Danger);
        assert!(v.reason.contains("kubectl delete"));
        assert_eq!(
            classify_command_with("KUBECTL DELETE pod x", &rules).risk,
            Risk::Danger
        );
        // 没命中的照旧走内置规则
        assert_eq!(
            classify_command_with("kubectl get pods", &rules).risk,
            Risk::Safe
        );
    }

    #[test]
    fn t27_empty_user_rule_must_not_match_everything() {
        // `contains("")` 恒为真 —— 面板上多留一行空输入框就够让每条命令
        // 都变危险命令。空规则必须在匹配这一层就被挡掉。
        let rules = vec!["".to_string(), "   ".to_string()];
        assert_eq!(classify_command_with("ls -la", &rules).risk, Risk::Safe);
        assert!(matches_user_danger("ls -la", &rules).is_none());
    }
}

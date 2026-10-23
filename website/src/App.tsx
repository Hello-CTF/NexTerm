import { useState, type ReactNode } from "react";
import {
  Activity, ArrowRight, Bot, CalendarClock, Container, Database, Download, Eye, FolderTree, Fingerprint, History,
  KeyRound, Laptop, Layers, Link, Lock, Monitor, Moon, Network, Radio, ScrollText, Server, Share2, ShieldCheck, Smartphone,
  SquareTerminal, Sun, Users, Workflow,
} from "lucide-react";
import { FeatureDemo, type DemoMode } from "./Demos";

const repository = "https://github.com/Hello-CTF/NexTerm";
const releases = `${repository}/releases/latest`;
const deployDocs = {
  zh: `${repository}/blob/master/README.md#部署服务端`,
  en: `${repository}/blob/master/README_EN.md#deploy-the-server`,
};
type Language = "zh" | "en";
type Theme = "dark" | "light";
type Item = [title: string, text: string];
type View = [name: DemoMode, label: string, title: string, alt: string];

const integrations = ["SSH", "WinRM", "SFTP", "Docker", "MySQL", "MariaDB", "PostgreSQL", "Redis", "SOCKS5", "OpenAI API", "DeepSeek", "Kimi", "GLM", "Ollama"];
const toolboxIcons = [FolderTree, Container, Database, Network, Activity, Link, Share2, Layers, SquareTerminal];
const trustIcons = [Lock, KeyRound, Users, ScrollText, ShieldCheck, Database];
const platformIcons = [Monitor, Laptop, Monitor, Server, Smartphone, Fingerprint];

const copy = {
  zh: {
    nav: ["AI 助手", "持久终端", "工具箱", "安全", "下载"], home: "NexTerm 首页", themeDark: "切换到浅色主题", themeLight: "切换到深色主题", language: "EN",
    eyebrow: "开源 · 跨平台 · 可自托管",
    heroLead: "一个工作台，", heroAccent: "管好每一台服务器",
    intro: "可接管的持久 SSH 与受管设备终端、WinRM 非交互终端，会动手的 AI 助手，再加上文件、Docker、数据库、设备监控和终端分享。桌面端开箱即用，也能部署成团队共用的服务端。",
    download: "下载 NexTerm", source: "GitHub 源码",
    views: [["shell", "终端", "deploy@web-01 — 终端与命令块", "NexTerm 的 Shell 终端与命令块界面"], ["ai", "AI 助手", "AI 助手 — 排查 API 延迟", "NexTerm 的 AI 助手对话与命令确认界面"], ["files", "文件", "web-01 — 远程文件", "NexTerm 的 SFTP 文件树与远程编辑界面"], ["replay", "回放", "终端历史 — 时间轴回放", "NexTerm 的终端历史时间轴回放界面"]] as View[],
    worksWith: "支持的连接与模型",
    aiKicker: "AI 助手", aiTitle: "不只给建议，而是真的动手", aiText: "AI 读取当前主机的终端、文件和容器上下文，直接执行排查与修复。每一步都看得见，写操作需要你点头。",
    aiPoints: [["20+ 内置工具", "执行命令、读写和编辑文件、查看 Docker 容器与日志、读取终端输出，可派生子任务并行处理。"], ["先计划，再执行", "计划模式先给出方案；写操作展示命令或文件 diff，可允许一次、本轮允许或拒绝，高危操作始终询问。"], ["按主机授权", "按资产授予执行命令、写文件、发送按键等权限，可限定路径（如 /etc/nginx/**），有效期 1 小时到 30 天。"], ["接管你的终端", "AI 可以在你正在使用的终端里直接输入，按 Esc 随时收回控制。"], ["定时任务与长期记忆", "用 cron 表达式让 AI 定时巡检；长期记忆保存环境约定，拒绝写入密钥类内容。"], ["任意模型", "OpenAI 兼容接口，内置 DeepSeek、Kimi、GLM、Ollama 预设，支持回退模型和代理；本地模型无需 API Key。"]] as Item[],
    sessionKicker: "持久终端", sessionTitle: "关掉窗口，持久终端还在", sessionText: "成功启用 durable/helper 的 SSH shell 和受管设备持久 shell 在后台持续运行。App 重启、网络中断或换一台设备，都能重新接管并补回错过的输出；WinRM 非交互终端随关闭结束。",
    sessionPoints: [["后台会话与接管", "关闭标签或断线后，持久会话转入后台，可从任意已登录设备一键接管。SSH helper 不可用时，降级后的普通 SSH PTY 在关闭或断开后无法恢复。"], ["多设备协同", "多台设备可同时观看同一终端，一台输入；接管后窗口尺寸自动适配。"], ["命令块与回放", "每条命令和输出折叠成块；完整录制支持搜索和时间轴回放。录制包含原始键盘输入，默认仅存本地。"], ["广播、分屏与快捷键", "一次输入发送到多个终端；分屏、多标签、命令面板和可自定义快捷键。"]] as Item[],
    toolboxKicker: "工具箱", toolboxTitle: "日常运维，不用再切换工具",
    toolbox: [["远程文件", "SFTP、WinRM 与设备文件浏览；内置编辑器保存前自动备份，大日志分页查看，可跟随终端目录。"], ["Docker", "容器启停、实时日志、资源统计、镜像拉取，一键 exec 进入容器。"], ["数据库", "MySQL / MariaDB、PostgreSQL 的 SQL 编辑器与表结构浏览；Redis 键浏览，危险命令有提醒。"], ["端口转发", "本地转发和 SOCKS5 代理，断线后自动重连。"], ["设备管理", "一行命令安装 agent，设备主动连回服务端；查看 CPU、内存、磁盘曲线并直接打开终端。"], ["终端分享", "为已有实时会话创建限时公开预览链接，默认只读；浏览器模式可显式授权写入，桌面中继预览目前只读；均可随时吊销，无需交出密码或私钥。"], ["主机分享", "把某台主机的终端会话分享给其他注册用户（仅终端，不含文件、转发、设备管理或 AI）。默认只读，可显式授权写入并随时吊销，无需交出密码或私钥。"], ["一键导入", "从 ~/.ssh/config 或 Termius 导入主机与密钥，先预览冲突；支持单跳 ProxyJump 映射，不自动带入来源分组、标签或 SSH 证书，多跳条目暂不支持自动导入。Termius 导入目前仅支持 macOS；其他桌面平台禁用入口，移动/Web 不显示入口，且没有手动 KeySource 替代路径。"], ["多种连接", "SSH（单跳跳板、代理、证书）、WinRM、本机终端、Docker 容器和受管设备。"]] as Item[],
    trustKicker: "安全与自托管", trustTitle: "数据留在你自己的机器上",
    trust: [["端到端加密同步", "桌面端的资产、片段和凭据跨设备同步，服务端只保存密文。"], ["凭据保险库", "桌面和移动端使用主密码或系统级保护（DPAPI / Keychain），移动端支持生物识别，支持空闲自动锁定；浏览器模式凭据由服务端 master key 保护的凭据库加密，与端到端加密同步不同。"], ["多用户与双因素", "服务端多账号、TOTP 双因素认证、登录设备管理。"], ["审计日志", "用户与 AI 的命令、文件写入、设备上下线都有记录。"], ["AI 护栏", "命令分级、输出脱敏、只读或可执行模式。"], ["选择你的数据库", "默认 SQLite，服务端支持 PostgreSQL、MySQL 和 MariaDB。"]] as Item[],
    platformKicker: "下载", platformTitle: "装在桌面，部署到服务器，或在手机上接着用",
    platforms: [["Windows", "10 / 11 · x64 / ARM64"], ["macOS", "Apple Silicon / Intel"], ["Linux 桌面", "deb · amd64 / arm64"], ["Linux 服务端", "浏览器访问 · 多用户"], ["移动端预览", "Android · iOS / iPadOS"], ["懒猫微服", "应用商店一键安装"]] as Item[],
    install: "部署服务端", footerIssue: "问题反馈", privacy: "隐私政策", license: "MIT License",
  },
  en: {
    nav: ["AI", "Sessions", "Toolbox", "Security", "Download"], home: "NexTerm home", themeDark: "Switch to light theme", themeLight: "Switch to dark theme", language: "中",
    eyebrow: "Open source · Cross-platform · Self-hostable",
    heroLead: "One workbench for ", heroAccent: "every server you run",
    intro: "Re-attachable persistent SSH and managed-device sessions, WinRM non-interactive terminals, an AI assistant that actually does the work, plus files, Docker, databases, device metrics, and terminal sharing. Run it on your desktop or deploy it as a shared server for your team.",
    download: "Download NexTerm", source: "View on GitHub",
    views: [["shell", "Terminal", "deploy@web-01 — terminal and command blocks", "NexTerm shell terminal and command blocks"], ["ai", "AI assistant", "AI assistant — investigating API latency", "NexTerm AI assistant and command confirmation"], ["files", "Files", "web-01 — remote files", "NexTerm SFTP file tree and remote editor"], ["replay", "Replay", "Terminal history — timeline replay", "NexTerm terminal history timeline"]] as View[],
    worksWith: "Works with",
    aiKicker: "AI assistant", aiTitle: "Not just advice. It does the work.", aiText: "The assistant reads the terminal, files, and containers of the current host, then investigates and fixes. Every step is visible, and writes wait for your approval.",
    aiPoints: [["20+ built-in tools", "Run commands, read and edit files, inspect Docker containers and logs, read terminal output, and spawn sub-tasks to work in parallel."], ["Plan, then execute", "Plan mode proposes first. Writes show the command or file diff: allow once, allow for this turn, or deny. High-risk actions always ask."], ["Per-host grants", "Grant command, file-write, or keystroke permissions per asset, scoped to paths like /etc/nginx/**, for 1 hour to 30 days."], ["Drives your live terminal", "The assistant can type into the terminal you are using. Press Esc to take control back at any time."], ["Scheduled jobs and memory", "Run AI checks on a cron schedule. Long-term memory keeps your environment conventions and rejects secrets."], ["Bring any model", "Any OpenAI-compatible endpoint, with DeepSeek, Kimi, GLM, and Ollama presets, fallback models, and proxies. Local models need no API key."]] as Item[],
    sessionKicker: "Persistent sessions", sessionTitle: "Close the window. Persistent shells stay.", sessionText: "SSH shells with the durable helper successfully enabled and persistent managed-device shells keep running in the background. After an app restart, a dropped network, or a device switch, take them over and catch up on missed output; WinRM non-interactive terminals end when closed.",
    sessionPoints: [["Background sessions", "Closed or disconnected persistent tabs move to the background, ready to take over from any signed-in device. When the SSH helper is unavailable, a fallback regular SSH PTY cannot be recovered after closing or disconnecting."], ["Multi-device", "Several devices can watch one terminal while one types. Taking over resizes the terminal to your window."], ["Command blocks and replay", "Each command and its output fold into a block. Full recordings support search and timeline replay. Recordings include raw keystrokes and stay local by default."], ["Broadcast, splits, shortcuts", "Send one input to many terminals. Split panes, tabs, a command palette, and rebindable shortcuts."]] as Item[],
    toolboxKicker: "Toolbox", toolboxTitle: "Everyday ops without switching tools",
    toolbox: [["Remote files", "Browse over SFTP, WinRM, or the device agent. The built-in editor backs up before saving, large logs page in, and the browser can follow the terminal directory."], ["Docker", "Start and stop containers, follow logs, view stats, pull images, and exec into a container in one click."], ["Databases", "SQL editor and schema browser for MySQL, MariaDB, and PostgreSQL. Redis key browser with warnings on dangerous commands."], ["Port forwarding", "Local forwards and SOCKS5 proxies that reconnect automatically."], ["Device fleet", "Install the agent with one command; devices connect out to your server. Watch CPU, memory, and disk charts and open a terminal directly."], ["Terminal sharing", "Create time-limited public preview links to existing live sessions. Links are read-only by default; browser mode allows explicit write grants, while desktop-relay previews are currently read-only. Links are revocable without handing over passwords or keys."], ["Host sharing with registered users", "Share a host's terminal session with another registered user (terminal only — no files, forwarding, device management, or AI). Recipients open the terminal through a dedicated share entry: read-only by default, writable only with an explicit grant, and revocable at any time. No host password or private key is required."], ["One-click import", "Import hosts and keys from ~/.ssh/config or Termius with a conflict preview. Single-hop ProxyJump is mapped; source groups, tags, and SSH certificates are not imported automatically, and entries using multi-hop ProxyJump cannot currently be imported automatically. Termius import is currently available only on macOS: other desktop platforms disable the entry, mobile and Web do not show it, and there is no manual KeySource fallback."], ["Every connection", "SSH with a single jump host, proxies, and certificates; WinRM; local shells; Docker containers; and managed devices."]] as Item[],
    trustKicker: "Security and self-hosting", trustTitle: "Your data stays on your machines",
    trust: [["End-to-end encrypted sync", "Desktop assets, snippets, and credentials sync across devices; the server only stores ciphertext."], ["Credential vault", "Desktop and mobile use a master password or OS-level protection (DPAPI / Keychain), with biometric unlock on mobile and idle auto-lock. Browser-mode credentials are instead encrypted by the server-side vault protected by its master key, not by end-to-end encrypted sync."], ["Multi-user and 2FA", "Server accounts, TOTP two-factor authentication, and signed-in device management."], ["Audit log", "Commands, file writes, and device online events from both users and the AI are recorded."], ["AI guardrails", "Command risk classification, output redaction, and read-only or execute modes."], ["Your database", "SQLite by default; the server also supports PostgreSQL, MySQL, and MariaDB."]] as Item[],
    platformKicker: "Download", platformTitle: "On your desktop, on your server, or on your phone",
    platforms: [["Windows", "10 / 11 · x64 / ARM64"], ["macOS", "Apple Silicon / Intel"], ["Linux desktop", "deb · amd64 / arm64"], ["Linux server", "Browser access · multi-user"], ["Mobile preview", "Android · iOS / iPadOS"], ["Lazycat", "Install from the app center"]] as Item[],
    install: "Deploy the server", footerIssue: "Issues", privacy: "Privacy Policy", license: "MIT License",
  },
};

function GithubIcon({ size = 19 }: { size?: number }) {
  return <svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M12 .5C5.65.5.5 5.65.5 12c0 5.08 3.29 9.39 7.86 10.91.58.11.79-.25.79-.55 0-.27-.01-1.17-.02-2.12-3.2.7-3.88-1.36-3.88-1.36-.52-1.33-1.28-1.68-1.28-1.68-1.04-.71.08-.7.08-.7 1.15.08 1.76 1.18 1.76 1.18 1.03 1.76 2.7 1.25 3.35.96.1-.75.4-1.25.72-1.54-2.55-.29-5.24-1.28-5.24-5.69 0-1.26.45-2.28 1.19-3.09-.12-.29-.52-1.46.11-3.05 0 0 .97-.31 3.17 1.18a11 11 0 0 1 5.77 0c2.2-1.49 3.17-1.18 3.17-1.18.63 1.59.23 2.76.11 3.05.74.81 1.19 1.83 1.19 3.09 0 4.42-2.7 5.4-5.27 5.68.41.36.78 1.06.78 2.14 0 1.55-.01 2.79-.01 3.17 0 .31.21.67.8.55A11.51 11.51 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5Z" /></svg>;
}

function Window({ title, children, className = "" }: { title: string; children: ReactNode; className?: string }) {
  return <div className={`window ${className}`}>
    <div className="window-bar" aria-hidden="true"><span /><span /><span /><div>{title}</div></div>
    {children}
  </div>;
}

function SectionHeading({ kicker, title, text }: { kicker: string; title: string; text?: string }) {
  return <div className="section-heading">
    <span className="kicker">{kicker}</span>
    <h2>{title}</h2>
    {text && <p>{text}</p>}
  </div>;
}

function App() {
  const browserLanguage = typeof navigator !== "undefined" && navigator.language.toLowerCase().startsWith("en") ? "en" : "zh";
  const [language, setLanguage] = useState<Language>(() => (localStorage.getItem("nexterm.site.language") as Language) || browserLanguage);
  const [theme, setTheme] = useState<Theme>(document.documentElement.dataset.theme === "light" ? "light" : "dark");
  const [view, setView] = useState(0);
  const t = copy[language];
  const current = t.views[view];

  const toggleTheme = () => {
    const next = theme === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    localStorage.setItem("nexterm.site.theme", next);
    setTheme(next);
  };
  const toggleLanguage = () => {
    const next = language === "zh" ? "en" : "zh";
    localStorage.setItem("nexterm.site.language", next);
    document.documentElement.lang = next === "zh" ? "zh-CN" : "en";
    setLanguage(next);
  };

  return <>
    <header className="site-header">
      <div className="container header-inner">
        <a className="brand" href="#top" aria-label={t.home}><img src="/brand/nexterm-mark-128.png" alt="" /><span>NexTerm</span></a>
        <nav className="nav-links" aria-label="Site navigation">
          {["ai", "sessions", "toolbox", "security", "download"].map((id, index) => <a key={id} href={`#${id}`}>{t.nav[index]}</a>)}
        </nav>
        <div className="header-actions">
          <a className="icon-link" href={repository} target="_blank" rel="noreferrer" aria-label="GitHub"><GithubIcon size={19} /></a>
          <button className="icon-link language-button" type="button" onClick={toggleLanguage} aria-label={t.language}>{t.language}</button>
          <button className="icon-link" type="button" onClick={toggleTheme} aria-label={theme === "dark" ? t.themeDark : t.themeLight}>{theme === "dark" ? <Sun size={18} /> : <Moon size={18} />}</button>
        </div>
      </div>
    </header>

    <main id="top">
      <section className="hero">
        <div className="hero-grid" aria-hidden="true" />
        <div className="container hero-inner">
          <span className="eyebrow">{t.eyebrow}</span>
          <h1>{t.heroLead}<span className="accent-text">{t.heroAccent}</span></h1>
          <p className="hero-copy">{t.intro}</p>
          <div className="hero-actions">
            <a className="button button-primary" href={releases} target="_blank" rel="noreferrer"><Download size={17} />{t.download}</a>
            <a className="button button-secondary" href={repository} target="_blank" rel="noreferrer"><GithubIcon size={17} />{t.source}</a>
          </div>
          <div className="view-tabs" role="tablist" aria-label={t.nav[0]}>
            {t.views.map(([name, label], index) => <button key={name} type="button" role="tab" aria-selected={index === view} className={index === view ? "active" : ""} onClick={() => setView(index)}>{label}</button>)}
          </div>
          <Window title={current[2]} className="hero-window">
            <div role="tabpanel" aria-label={current[3]}><FeatureDemo mode={current[0]} language={language} /></div>
          </Window>
        </div>
      </section>

      <section className="integrations" aria-label={t.worksWith}>
        <div className="container integrations-inner">
          <span>{t.worksWith}</span>
          <ul>{integrations.map((name) => <li key={name}>{name}</li>)}</ul>
        </div>
      </section>

      <section className="section" id="ai">
        <div className="container split">
          <div>
            <SectionHeading kicker={t.aiKicker} title={t.aiTitle} text={t.aiText} />
            <div className="point-grid">
              {t.aiPoints.map(([title, text], index) => {
                const Icon = [Bot, Workflow, KeyRound, Eye, CalendarClock, Radio][index];
                return <article className="point" key={title}>{Icon && <Icon size={18} />}<div><h3>{title}</h3><p>{text}</p></div></article>;
              })}
            </div>
          </div>
          <Window title={t.views[1][2]} className="split-window"><FeatureDemo mode="ai" language={language} /></Window>
        </div>
      </section>

      <section className="section section-muted" id="sessions">
        <div className="container split split-reverse">
          <div>
            <SectionHeading kicker={t.sessionKicker} title={t.sessionTitle} text={t.sessionText} />
            <div className="point-grid point-grid-single">
              {t.sessionPoints.map(([title, text], index) => {
                const Icon = [SquareTerminal, Smartphone, History, Layers][index];
                return <article className="point" key={title}>{Icon && <Icon size={18} />}<div><h3>{title}</h3><p>{text}</p></div></article>;
              })}
            </div>
          </div>
          <Window title={t.views[3][2]} className="split-window"><FeatureDemo mode="replay" language={language} /></Window>
        </div>
      </section>

      <section className="section" id="toolbox">
        <div className="container">
          <SectionHeading kicker={t.toolboxKicker} title={t.toolboxTitle} />
          <div className="tool-grid">
            {t.toolbox.map(([title, text], index) => {
              const Icon = toolboxIcons[index];
              return <article className="tool-card" key={title}><span className="tool-icon">{Icon && <Icon size={18} />}</span><h3>{title}</h3><p>{text}</p></article>;
            })}
          </div>
        </div>
      </section>

      <section className="section section-muted" id="security">
        <div className="container">
          <SectionHeading kicker={t.trustKicker} title={t.trustTitle} />
          <div className="trust-grid">
            {t.trust.map(([title, text], index) => {
              const Icon = trustIcons[index];
              return <article className="trust-item" key={title}>{Icon && <Icon size={18} />}<div><h3>{title}</h3><p>{text}</p></div></article>;
            })}
          </div>
        </div>
      </section>

      <section className="section" id="download">
        <div className="container">
          <SectionHeading kicker={t.platformKicker} title={t.platformTitle} />
          <div className="platform-grid">
            {t.platforms.map(([title, text], index) => {
              const Icon = platformIcons[index];
              return <div className="platform" key={title}>{Icon && <Icon size={20} />}<div><strong>{title}</strong><span>{text}</span></div></div>;
            })}
          </div>
          <div className="download-actions">
            <a className="button button-primary" href={releases} target="_blank" rel="noreferrer"><Download size={17} />{t.download}</a>
            <a className="button button-secondary" href={deployDocs[language]} target="_blank" rel="noreferrer">{t.install} <ArrowRight size={16} /></a>
          </div>
        </div>
      </section>
    </main>

    <footer className="site-footer">
      <div className="container footer-inner">
        <a className="brand" href="#top"><img src="/brand/nexterm-mark-128.png" alt="" /><span>NexTerm</span></a>
        <div className="footer-links">
          <a href={repository} target="_blank" rel="noreferrer">GitHub</a>
          <a href={releases} target="_blank" rel="noreferrer">Releases</a>
          <a href={`${repository}/issues`} target="_blank" rel="noreferrer">{t.footerIssue}</a>
          <a href={language === "en" ? "/privacy.html#en" : "/privacy.html#zh"}>{t.privacy}</a>
          <a href={`${repository}/blob/master/LICENSE`} target="_blank" rel="noreferrer">{t.license}</a>
        </div>
        <span className="copyright">© 2026 Hello-CTF</span>
      </div>
    </footer>
  </>;
}

export default App;

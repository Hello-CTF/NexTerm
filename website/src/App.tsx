import { useState, type ReactNode } from "react";
import {
  Activity, Bell, Bot, CalendarClock, Container, Database, Download, Eye, FolderTree, History,
  KeyRound, Layers, Lock, Moon, Network, Radio, ScrollText, Share2, ShieldCheck, Smartphone,
  SquareTerminal, Sun, Users, Workflow,
} from "lucide-react";
import { FeatureDemo, type DemoMode } from "./Demos";
import { DownloadPicker } from "./Download";

const repository = "https://github.com/Hello-CTF/NexTerm";
const releases = `${repository}/releases/latest`;
type Language = "zh" | "en";
type Theme = "dark" | "light";
type Item = [title: string, text: string];
type View = [name: DemoMode, label: string, title: string, alt: string];

const toolboxIcons = [FolderTree, Container, Database, Network, Activity, Share2, Layers, SquareTerminal];
const trustIcons = [Lock, KeyRound, Users, ScrollText, ShieldCheck, Database];

const copy = {
  zh: {
    nav: ["AI 助手", "持久终端", "工具箱", "安全", "下载"], home: "NexTerm 首页", themeDark: "切换到浅色主题", themeLight: "切换到深色主题", language: "EN",
    eyebrow: "开源 · 跨平台 · 可自托管",
    heroLead: "一个工作台", heroAccent: "管好每一台服务器",
    intro: "可接管的持久终端、会动手的 AI 助手，加上文件、容器、数据库与设备管理。桌面开箱即用，也能部署成团队服务端。",
    download: "下载 NexTerm", source: "GitHub 源码",
    views: [["shell", "终端", "deploy@web-01 — 终端与命令块", "NexTerm 的 Shell 终端与命令块界面"], ["files", "文件", "web-01 — 远程文件", "NexTerm 的 SFTP 文件树与远程编辑界面"], ["docker", "Docker", "web-01 — 容器", "NexTerm 的 Docker 容器列表、日志与启停界面"], ["db", "数据库", "app — SQL 查询", "NexTerm 的数据库表浏览与 SQL 查询界面"]] as View[],
    aiWindow: "AI 助手 — 排查 API 延迟", replayWindow: "终端历史 — 时间轴回放",
    aiKicker: "AI 助手", aiTitle: "不只给建议，而是真的动手", aiText: "AI 读取当前主机的终端、文件和容器上下文，直接执行排查与修复。每一步都看得见，写操作需要你点头。",
    aiPoints: [["20+ 内置工具", "执行命令、读写和编辑文件、查看 Docker 容器与日志、读取终端输出，可派生子任务并行处理。"], ["先计划，再执行", "计划模式先给出方案；写操作展示命令或文件 diff，可允许一次、本会话允许或拒绝，高危操作始终询问。"], ["按主机授权", "按资产授予执行命令、写文件、发送按键等权限，可限定路径（如 /etc/nginx/**），有效期 1 小时到 30 天。"], ["接管你的终端", "AI 可以在你正在使用的终端里直接输入，按 Esc 随时收回控制。"], ["定时任务与长期记忆", "用 cron 表达式让 AI 定时巡检；长期记忆保存环境约定，拒绝写入密钥类内容。"], ["任意模型", "OpenAI 兼容接口，内置 DeepSeek、Kimi、GLM、Ollama 预设，支持回退模型和代理；本地模型无需 API Key。"]] as Item[],
    sessionKicker: "持久终端", sessionTitle: "关掉窗口，持久终端还在", sessionText: "桌面端的本机终端默认运行在本机持久组件里，成功启用持久终端的 SSH shell 和受管设备 shell 也在后台持续运行。App 重启、网络中断或换一台设备，都能重新接管并补回错过的输出（主机端每个持久终端保留最近约 8 MB 输出）；WinRM 非交互终端随关闭结束。",
    sessionPoints: [["后台会话与接管", "关闭标签或断线后，持久会话转入后台，可在后台会话接管或终止。标签上的图标说明是否持久；组件准备中或不可用时新建的是普通终端并标明原因，普通终端关闭或断开后无法恢复。替换旧组件会结束其中的会话，替换前需确认。移动端没有本机终端。"], ["先看哪个会话需要我", "徽章、顶栏收件箱和桌面系统通知指出需要你的会话；检测到选项时可点按钮回答，未在本设备打开的会话显示「打开并回答」。手机首页是状态墙。「需要我」可能是推测，可一键标记并保存规则，规则可由 AI 起草（尽力脱敏、不保证全部识别），并按设置同步。"], ["多设备协同", "多台设备同时观看并输入同一终端，没有接管步骤；尺寸跟随最后输入的设备，其他设备显示缩放后的画面。手机只在桌面端运行（托盘常驻也算）并开启「向服务器账号开放本机终端」时能看到本机终端，这会把该电脑当前用户的命令行权限交给该服务器账号的管理方；纯 SSH 主机只能通过手机自己的 SSH 会话或已接入的 agent 看到。"], ["命令块与回放", "每条命令和输出折叠成块；完整录制支持搜索和时间轴回放。录制包含原始键盘输入，默认仅存本地。本机滚动默认 10000 行，持久终端可「加载更早历史」。"], ["跳转、分屏与重连", "跳转面板（Ctrl/Cmd+Shift+P）按需要程度排序并支持 Alt+数字直开；嵌套分屏，关闭窗格会并入相邻窗格；终端内搜索在非 macOS 为 Ctrl+Shift+F。工作区可一键重连，连接存疑时可手动重连；一次输入可广播到多个终端。"]] as Item[],
    toolboxKicker: "工具箱", toolboxTitle: "日常运维，不用再切换工具",
    toolbox: [["远程文件", "SFTP、WinRM 与设备文件浏览；内置编辑器保存前自动备份，大日志分页查看，可跟随终端目录。"], ["Docker", "容器启停、实时日志、资源统计、镜像拉取，一键 exec 进入容器。"], ["数据库", "MySQL / MariaDB、PostgreSQL 的 SQL 编辑器与表结构浏览；Redis 键浏览，危险命令有提醒。"], ["端口转发", "本地转发和 SOCKS5 代理，断线后自动重连。"], ["设备管理", "一行命令安装 agent，设备主动连回服务端；查看 CPU、内存、磁盘曲线并直接打开终端。"], ["终端分享", "为实时会话生成限时公开链接，或把主机终端分享给其他注册用户；默认只读，浏览器模式可授权写入（桌面端创建的公开链接只读），随时吊销，无需交出密码或私钥。"], ["一键导入", "从 ~/.ssh/config 或 Termius（仅 macOS）导入主机与密钥，导入前预览冲突，支持单跳 ProxyJump，多跳条目需手动添加。"], ["多种连接", "SSH（单跳跳板、代理、证书）、WinRM、本机终端、Docker 容器和受管设备。"]] as Item[],
    trustKicker: "安全与自托管", trustTitle: "数据留在你自己的机器上",
    trust: [["端到端加密同步", "桌面端的资产、片段和凭据跨设备同步，服务端只保存密文。"], ["凭据保险库", "桌面和移动端使用主密码或系统级保护（DPAPI / Keychain），移动端支持生物识别，支持空闲自动锁定；浏览器模式凭据由服务端 master key 保护的凭据库加密，与端到端加密同步不同。"], ["多用户与双因素", "服务端多账号、TOTP 双因素认证、登录设备管理。"], ["审计日志", "用户与 AI 的命令、文件写入、设备上下线都有记录。"], ["AI 护栏", "命令分级、输出脱敏、只读或可执行模式。"], ["选择你的数据库", "默认 SQLite，服务端支持 PostgreSQL、MySQL 和 MariaDB。"]] as Item[],
    platformKicker: "下载", platformTitle: "装在桌面，部署到服务器，或在手机上接着用",
    footerIssue: "问题反馈", privacy: "隐私政策", license: "MIT License",
  },
  en: {
    nav: ["AI", "Sessions", "Toolbox", "Security", "Download"], home: "NexTerm home", themeDark: "Switch to light theme", themeLight: "Switch to dark theme", language: "中",
    eyebrow: "Open source · Cross-platform · Self-hostable",
    heroLead: "One workbench for", heroAccent: "every server you run",
    intro: "Re-attachable persistent terminals and an AI assistant that does the work, plus files, containers, databases, and devices. Ready on your desktop, or deploy it as a server for your team.",
    download: "Download NexTerm", source: "View on GitHub",
    views: [["shell", "Terminal", "deploy@web-01 — terminal and command blocks", "NexTerm shell terminal and command blocks"], ["files", "Files", "web-01 — remote files", "NexTerm SFTP file tree and remote editor"], ["docker", "Docker", "web-01 — containers", "NexTerm Docker container list, logs and controls"], ["db", "Database", "app — SQL query", "NexTerm database table browser and SQL query"]] as View[],
    aiWindow: "AI assistant — investigating API latency", replayWindow: "Terminal history — timeline replay",
    aiKicker: "AI assistant", aiTitle: "Not just advice. It does the work.", aiText: "The assistant reads the terminal, files, and containers of the current host, then investigates and fixes. Every step is visible, and writes wait for your approval.",
    aiPoints: [["20+ built-in tools", "Run commands, read and edit files, inspect Docker containers and logs, read terminal output, and spawn sub-tasks to work in parallel."], ["Plan, then execute", "Plan mode proposes first. Writes show the command or file diff: allow once, allow for this conversation, or deny. High-risk actions always ask."], ["Per-host grants", "Grant command, file-write, or keystroke permissions per asset, scoped to paths like /etc/nginx/**, for 1 hour to 30 days."], ["Drives your live terminal", "The assistant can type into the terminal you are using. Press Esc to take control back at any time."], ["Scheduled jobs and memory", "Run AI checks on a cron schedule. Long-term memory keeps your environment conventions and rejects secrets."], ["Bring any model", "Any OpenAI-compatible endpoint, with DeepSeek, Kimi, GLM, and Ollama presets, fallback models, and proxies. Local models need no API key."]] as Item[],
    sessionKicker: "Persistent sessions", sessionTitle: "Close the window. Persistent shells stay.", sessionText: "On desktop, local terminals run by default in a local persistent helper, and SSH shells with persistent terminals enabled and managed-device shells keep running in the background too. After an app restart, a dropped network, or a device switch, take them over and catch up on missed output (the host keeps the most recent 8 MB or so per persistent terminal); WinRM non-interactive terminals end when closed.",
    sessionPoints: [["Background sessions", "Closed or disconnected persistent tabs move to the background, where you can take them over or end them. A tab icon shows whether a terminal is persistent; one opened while the helper is preparing or unavailable is a regular terminal with the reason shown, and a regular terminal cannot be recovered after closing or disconnecting. Replacing an older helper ends its sessions and asks first. Mobile has no local terminals."], ["See which session needs me", "Badges, the header inbox, and desktop system notifications point to sessions that need you; when a choice prompt is detected you can answer with a button, and a session not open on this device offers \"打开并回答\" (open and answer). The phone home is a status wall. \"Needs me\" can be inferred, so mark it once to save a rule, optionally AI-drafted (redacted on a best-effort basis, not guaranteed) and synced per your settings."], ["Multi-device", "Several devices watch and type into one terminal at once, with no take-control step; the size follows the last device that typed and the others show a scaled view. The phone sees a desktop's local terminals only while the desktop app runs (the tray counts) and Expose local terminals to the server account is on, which hands that computer's command line, as the current user, to the account's server operators; SSH-only hosts show on the phone only through its own SSH session or enrolled agents."], ["Command blocks and replay", "Each command and its output fold into a block. Full recordings support search and timeline replay. Recordings include raw keystrokes and stay local by default. Local scrollback defaults to 10,000 lines, and persistent terminals can load earlier history."], ["Jump, splits, reconnect", "The jump palette (Ctrl/Cmd+Shift+P) ranks sessions by need and opens the Nth with Alt+digit. Nested splits merge tabs into the adjacent pane when a pane closes; terminal search is Ctrl+Shift+F on non-macOS. Reconnect a whole workspace in one step, or manually while a connection is only suspect; broadcast one input to many terminals."]] as Item[],
    toolboxKicker: "Toolbox", toolboxTitle: "Everyday ops without switching tools",
    toolbox: [["Remote files", "Browse over SFTP, WinRM, or the device agent. The built-in editor backs up before saving, large logs page in, and the browser can follow the terminal directory."], ["Docker", "Start and stop containers, follow logs, view stats, pull images, and exec into a container in one click."], ["Databases", "SQL editor and schema browser for MySQL, MariaDB, and PostgreSQL. Redis key browser with warnings on dangerous commands."], ["Port forwarding", "Local forwards and SOCKS5 proxies that reconnect automatically."], ["Device fleet", "Install the agent with one command; devices connect out to your server. Watch CPU, memory, and disk charts and open a terminal directly."], ["Terminal sharing", "Share a live session as a time-limited public link, or share a host's terminal with registered users. Read-only by default, writable in browser mode on request (public links created on desktop are read-only), revocable anytime, no passwords or keys handed over."], ["One-click import", "Import hosts and keys from ~/.ssh/config or Termius (macOS only) with a conflict preview and single-hop ProxyJump; add multi-hop entries manually."], ["Every connection", "SSH with a single jump host, proxies, and certificates; WinRM; local shells; Docker containers; and managed devices."]] as Item[],
    trustKicker: "Security and self-hosting", trustTitle: "Your data stays on your machines",
    trust: [["End-to-end encrypted sync", "Desktop assets, snippets, and credentials sync across devices; the server only stores ciphertext."], ["Credential vault", "Desktop and mobile use a master password or OS-level protection (DPAPI / Keychain), with biometric unlock on mobile and idle auto-lock. Browser-mode credentials are instead encrypted by the server-side vault protected by its master key, not by end-to-end encrypted sync."], ["Multi-user and 2FA", "Server accounts, TOTP two-factor authentication, and signed-in device management."], ["Audit log", "Commands, file writes, and device online events from both users and the AI are recorded."], ["AI guardrails", "Command risk classification, output redaction, and read-only or execute modes."], ["Your database", "SQLite by default; the server also supports PostgreSQL, MySQL, and MariaDB."]] as Item[],
    platformKicker: "Download", platformTitle: "On your desktop, on your server, or on your phone",
    footerIssue: "Issues", privacy: "Privacy Policy", license: "MIT License",
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
          <h1>{t.heroLead}<br /><span className="accent-text">{t.heroAccent}</span></h1>
          <p className="hero-copy">{t.intro}</p>
          <div className="hero-actions">
            <a className="button button-primary" href="#download"><Download size={17} />{t.download}</a>
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
          <Window title={t.aiWindow} className="split-window"><FeatureDemo mode="ai" language={language} /></Window>
        </div>
      </section>

      <section className="section section-muted" id="sessions">
        <div className="container split split-reverse">
          <div>
            <SectionHeading kicker={t.sessionKicker} title={t.sessionTitle} text={t.sessionText} />
            <div className="point-grid point-grid-single">
              {t.sessionPoints.map(([title, text], index) => {
                const Icon = [SquareTerminal, Bell, Smartphone, History, Layers][index];
                return <article className="point" key={title}>{Icon && <Icon size={18} />}<div><h3>{title}</h3><p>{text}</p></div></article>;
              })}
            </div>
          </div>
          <Window title={t.replayWindow} className="split-window"><FeatureDemo mode="replay" language={language} /></Window>
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
          <DownloadPicker language={language} />
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

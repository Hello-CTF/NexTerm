import { useState } from "react";
import { Bot, CalendarClock, Container, Download, History, ListChecks, Moon, Network, Play, Search, Server, Share2, SquareTerminal, Sun } from "lucide-react";
import { FeatureDemos, type DemoMode } from "./Demos";
import { DownloadPicker } from "./Download";

const repository = "https://github.com/Hello-CTF/NexTerm";
const releases = `${repository}/releases/latest`;
const readme = { zh: `${repository}#readme`, en: `${repository}/blob/master/README_EN.md` };
type Language = "zh" | "en";
type Theme = "dark" | "light";
type Item = [title: string, text: string];

const featureIcons = [SquareTerminal, Bot, Container, Network, Share2, Server];

const copy = {
  zh: {
    nav: ["功能", "下载", "FAQ"], home: "NexTerm 首页", themeDark: "切换到浅色主题", themeLight: "切换到深色主题", language: "EN",
    eyebrow: "开源 · 可自托管",
    heroLead: "一个工作台", heroAccent: "管好每一台服务器",
    intro: "断线不丢的持久终端，能动手排查的 AI 助手，文件、容器和数据库都在同一个窗口里。",
    download: "下载 NexTerm", source: "GitHub 源码",
    featuresKicker: "功能", featuresTitle: "日常运维需要的，都在一个窗口里",
    features: [
      ["持久终端", "关闭窗口或网络中断后，持久终端在后台继续运行，回来就能接管并补回错过的输出。持久终端组件不可用时，新标签会标明是临时终端。"],
      ["AI 助手", "读取终端、文件和容器上下文，直接排查和修复问题。写操作默认先展示命令或 diff，你确认后才执行。支持任意 OpenAI 兼容模型。"],
      ["日常运维", "远程文件编辑、Docker 容器与日志、MySQL / PostgreSQL / Redis、端口转发，不用在多个工具之间来回切换。"],
      ["所有连接", "SSH、WinRM、本机终端、Docker 容器、受管设备，以及 RDP / VNC 远程桌面。"],
      ["设备与协作", "一行命令接入设备，查看指标并接收告警。同一终端可多端同时查看，也可分享为限时链接，默认只读。"],
      ["自托管", "支持多用户、双因素认证和审计日志，凭据加密保存。数据库可选 SQLite、PostgreSQL、MySQL 或 MariaDB。"],
    ] as Item[],
    seeDemo: "在演示中查看",
    aiKicker: "AI 助手", aiTitle: "不止给出建议，还能动手排查", aiText: "AI 读取当前主机的上下文，执行排查与修复，每一步都可查看。",
    aiPoints: [["先确认，再执行", "命令和文件改动先给你看；也可按主机和路径限时授权。"], ["计划、定时与记忆", "计划模式先出方案，可按 cron 定时巡检，长期记忆保存环境约定。"], ["任意模型", "OpenAI 兼容接口，内置 DeepSeek、Kimi、GLM、Ollama 预设。"]] as Item[],
    replayKicker: "命令块与回放", replayTitle: "回看任意时刻的终端输出", replayText: "每条命令和输出折叠成块，完整录制可以搜索并按时间轴回放。",
    replayPoints: [["命令块", "按命令查看输出，快速定位哪一步出了问题。"], ["时间轴回放", "拖动时间轴，回到任意时刻的终端画面。"], ["历史搜索", "跨会话搜索命令历史和主机上已录制的输出。"]] as Item[],
    platformKicker: "下载", platformTitle: "桌面安装、服务器部署，也可在手机上预览",
    faqKicker: "FAQ", faqTitle: "常见问题",
    faq: [
      ["关闭 App 后终端还在吗？", "持久终端还在：桌面本机终端，以及已启用持久终端的 SSH 主机和受管设备，会在后台继续运行，重新打开后可在「后台会话」接管并补回输出。持久终端组件不可用时新建的是临时终端，标签会标明，断开即结束；WinRM 终端随关闭结束。"],
      ["AI 会擅自改动服务器吗？", "不会。执行命令、写文件等操作默认先展示命令或 diff，由你允许或拒绝；只有你按主机和路径授予的权限，在有效期内才免于逐次确认。"],
      ["同步是端到端加密吗？", "不是。同步数据在客户端加密后上传，服务端只保存密文；但登录时密码会发送给服务端，服务端运营者可以据此解开同步数据。请只登录你信任的服务端，并使用 HTTPS。"],
      ["支持手机吗？", "Android（arm64）和 iOS / iPadOS（TestFlight）目前是预览版，完成真机签名验收前不作为正式支持平台。手机上没有本机终端，可以连接 SSH 主机和受管设备。"],
    ] as Item[],
    footerDocs: "完整功能说明", footerIssue: "问题反馈", privacy: "隐私政策", license: "MIT License",
  },
  en: {
    nav: ["Features", "Download", "FAQ"], home: "NexTerm home", themeDark: "Switch to light theme", themeLight: "Switch to dark theme", language: "中",
    eyebrow: "Open source · Self-hostable",
    heroLead: "One workbench for", heroAccent: "every server you run",
    intro: "Persistent terminals that survive disconnects, an AI assistant that investigates and fixes, and files, containers, and databases in one window.",
    download: "Download NexTerm", source: "View on GitHub",
    featuresKicker: "Features", featuresTitle: "Everything day-to-day ops needs, in one window",
    features: [
      ["Persistent terminals", "When the window closes or the network drops, persistent terminals keep running in the background; come back, take over, and catch up on missed output. When the persistent terminal helper is unavailable, new tabs are marked as temporary terminals."],
      ["AI assistant", "Reads terminal, file, and container context to investigate and fix problems. Writes show the command or diff first and run only after you approve by default. Works with any OpenAI-compatible model."],
      ["Everyday ops", "Remote file editing, Docker containers and logs, MySQL / PostgreSQL / Redis, and port forwarding, without switching between tools."],
      ["Every connection", "SSH, WinRM, local shells, Docker containers, managed devices, and RDP / VNC remote desktops."],
      ["Devices and collaboration", "Enroll a device with one command, watch its metrics, and get alerts. Several devices can view one terminal at once, or share it as a time-limited link, read-only by default."],
      ["Self-hosted", "Multi-user accounts, two-factor authentication, and audit logs, with credentials stored encrypted. Choose SQLite, PostgreSQL, MySQL, or MariaDB."],
    ] as Item[],
    seeDemo: "See it in the demo",
    aiKicker: "AI assistant", aiTitle: "Not just advice. It investigates.", aiText: "The assistant reads the current host's context, then investigates and fixes, with every step visible.",
    aiPoints: [["Approve, then run", "Commands and file changes are shown to you first, or granted per host and path for a set time."], ["Plans, schedules, memory", "Plan mode proposes first, cron jobs run checks, and long-term memory keeps your conventions."], ["Bring any model", "Any OpenAI-compatible endpoint, with DeepSeek, Kimi, GLM, and Ollama presets."]] as Item[],
    replayKicker: "Command blocks and replay", replayTitle: "See the terminal at any moment", replayText: "Each command and its output fold into a block, and full recordings can be searched and replayed on a timeline.",
    replayPoints: [["Command blocks", "Read output command by command to find the step that failed."], ["Timeline replay", "Drag the timeline back to any moment of the session."], ["History search", "Search command history and a host's recorded output across sessions."]] as Item[],
    platformKicker: "Download", platformTitle: "On your desktop, on your server, and in preview on your phone",
    faqKicker: "FAQ", faqTitle: "Frequently asked questions",
    faq: [
      ["Do terminals survive closing the app?", "Persistent terminals do: desktop local terminals, and SSH hosts and managed devices with persistent terminals enabled, keep running in the background, and you take them over from Background sessions with missed output replayed. When the persistent terminal helper is unavailable, new tabs are temporary terminals, marked as such, that end on disconnect; WinRM terminals end when closed."],
      ["Will the AI change my servers on its own?", "No. Running commands, writing files, and similar actions show the command or diff first and wait for you to allow or deny them; only permissions you grant per host and path skip that prompt, and only until they expire."],
      ["Is sync end-to-end encrypted?", "No. Synced data is encrypted on the client and the server stores only ciphertext, but the password reaches the server at sign-in, so the server operator can use it to decrypt synced data. Sign in only to servers you trust, over HTTPS."],
      ["Does it run on phones?", "Android (arm64) and iOS / iPadOS (TestFlight) are previews and are not officially supported until real-device signing is verified. Phones have no local terminals; they connect to SSH hosts and managed devices."],
    ] as Item[],
    footerDocs: "Full feature details", footerIssue: "Issues", privacy: "Privacy Policy", license: "MIT License",
  },
};

function GithubIcon({ size = 19 }: { size?: number }) {
  return <svg width={size} height={size} viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M12 .5C5.65.5.5 5.65.5 12c0 5.08 3.29 9.39 7.86 10.91.58.11.79-.25.79-.55 0-.27-.01-1.17-.02-2.12-3.2.7-3.88-1.36-3.88-1.36-.52-1.33-1.28-1.68-1.28-1.68-1.04-.71.08-.7.08-.7 1.15.08 1.76 1.18 1.76 1.18 1.03 1.76 2.7 1.25 3.35.96.1-.75.4-1.25.72-1.54-2.55-.29-5.24-1.28-5.24-5.69 0-1.26.45-2.28 1.19-3.09-.12-.29-.52-1.46.11-3.05 0 0 .97-.31 3.17 1.18a11 11 0 0 1 5.77 0c2.2-1.49 3.17-1.18 3.17-1.18.63 1.59.23 2.76.11 3.05.74.81 1.19 1.83 1.19 3.09 0 4.42-2.7 5.4-5.27 5.68.41.36.78 1.06.78 2.14 0 1.55-.01 2.79-.01 3.17 0 .31.21.67.8.55A11.51 11.51 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5Z" /></svg>;
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
  const [view, setView] = useState<DemoMode>("terminal");
  const t = copy[language];

  // showDemo switches the hero demo and brings it into view, with the selected tab focused.
  const showDemo = (mode: DemoMode) => {
    setView(mode);
    const reduced = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches === true;
    document.getElementById("demo")?.scrollIntoView({ behavior: reduced ? "auto" : "smooth", block: "start" });
    window.requestAnimationFrame(() => document.getElementById(`demo-tab-${mode}`)?.focus({ preventScroll: true }));
  };

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
          {["features", "download", "faq"].map((id, index) => <a key={id} href={`#${id}`}>{t.nav[index]}</a>)}
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
          <FeatureDemos language={language} mode={view} onChange={setView} />
        </div>
      </section>

      <section className="section" id="features">
        <div className="container">
          <SectionHeading kicker={t.featuresKicker} title={t.featuresTitle} />
          <div className="tool-grid">
            {t.features.map(([title, text], index) => {
              const Icon = featureIcons[index];
              return <article className="tool-card" key={title}><span className="tool-icon">{Icon && <Icon size={18} />}</span><h3>{title}</h3><p>{text}</p></article>;
            })}
          </div>
        </div>
      </section>

      <section className="section section-muted" id="ai">
        <div className="container split">
          <div>
            <SectionHeading kicker={t.aiKicker} title={t.aiTitle} text={t.aiText} />
            <button type="button" className="button button-secondary" onClick={() => showDemo("ai")}><Play size={16} />{t.seeDemo}</button>
          </div>
          <div className="point-grid point-grid-single">
            {t.aiPoints.map(([title, text], index) => {
              const Icon = [ListChecks, CalendarClock, Bot][index];
              return <article className="point" key={title}>{Icon && <Icon size={18} />}<div><h3>{title}</h3><p>{text}</p></div></article>;
            })}
          </div>
        </div>
      </section>

      <section className="section" id="replay">
        <div className="container split">
          <div>
            <SectionHeading kicker={t.replayKicker} title={t.replayTitle} text={t.replayText} />
            <button type="button" className="button button-secondary" onClick={() => showDemo("history")}><Play size={16} />{t.seeDemo}</button>
          </div>
          <div className="point-grid point-grid-single">
            {t.replayPoints.map(([title, text], index) => {
              const Icon = [SquareTerminal, History, Search][index];
              return <article className="point" key={title}>{Icon && <Icon size={18} />}<div><h3>{title}</h3><p>{text}</p></div></article>;
            })}
          </div>
        </div>
      </section>

      <section className="section section-muted" id="download">
        <div className="container">
          <SectionHeading kicker={t.platformKicker} title={t.platformTitle} />
          <DownloadPicker language={language} />
        </div>
      </section>

      <section className="section" id="faq">
        <div className="container">
          <SectionHeading kicker={t.faqKicker} title={t.faqTitle} />
          <div className="faq-list">
            {t.faq.map(([question, answer]) => <details className="faq-item" key={question}><summary>{question}</summary><p>{answer}</p></details>)}
          </div>
        </div>
      </section>
    </main>

    <footer className="site-footer">
      <div className="container footer-inner">
        <a className="brand" href="#top"><img src="/brand/nexterm-mark-128.png" alt="" /><span>NexTerm</span></a>
        <div className="footer-links">
          <a href={readme[language]} target="_blank" rel="noreferrer">{t.footerDocs}</a>
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

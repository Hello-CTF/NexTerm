import { useState } from "react";
import { ArrowRight, CodeXml, Download, Monitor, Moon, Server, Sun } from "lucide-react";

const repository = "https://github.com/Hello-CTF/NexTerm";
const releases = `${repository}/releases/latest`;

type Theme = "dark" | "light";

const views = [
  {
    name: "shell",
    title: "Shell 工作区",
    text: "终端、命令块和后台会话集中在同一视图，不用在多个窗口之间切换。",
    alt: "NexTerm 的 Shell 终端与命令块界面",
  },
  {
    name: "ai",
    title: "AI 助手",
    text: "围绕当前主机提问和排障，执行命令、修改文件前都能检查具体操作。",
    alt: "NexTerm 的 AI 助手对话与命令确认界面",
  },
  {
    name: "files",
    title: "远程文件",
    text: "在文件树和编辑器之间直接处理远端文件，上传下载无需另开工具。",
    alt: "NexTerm 的 SFTP 文件树与远程编辑界面",
  },
  {
    name: "replay",
    title: "终端回放",
    text: "沿时间轴复现终端输出，看清故障前后执行了什么、系统返回了什么。",
    alt: "NexTerm 的终端历史时间轴回放界面",
  },
];

function Picture({ name, alt, eager = false }: { name: string; alt: string; eager?: boolean }) {
  return (
    <>
      <img
        className="shot shot-dark"
        src={`/images/${name}-dark.jpg`}
        alt={alt}
        loading={eager ? "eager" : "lazy"}
      />
      <img
        className="shot shot-light"
        src={`/images/${name}-light.jpg`}
        alt={alt}
        loading={eager ? "eager" : "lazy"}
      />
    </>
  );
}

function App() {
  const [theme, setTheme] = useState<Theme>(
    document.documentElement.dataset.theme === "light" ? "light" : "dark",
  );

  const toggleTheme = () => {
    const next = theme === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    localStorage.setItem("nexterm.site.theme", next);
    setTheme(next);
  };

  return (
    <>
      <header className="site-header">
        <div className="container header-inner">
          <a className="brand" href="#top" aria-label="NexTerm 首页">
            <img src="/brand/nexterm-mark-128.png" alt="" />
            <span>NexTerm</span>
          </a>
          <nav className="nav-links" aria-label="页面导航">
            <a href="#showcase">界面</a>
            <a href="#download">下载</a>
          </nav>
          <div className="header-actions">
            <a className="icon-link" href={repository} target="_blank" rel="noreferrer" aria-label="GitHub 仓库">
              <CodeXml size={19} />
            </a>
            <button className="icon-link" type="button" onClick={toggleTheme} aria-label={theme === "dark" ? "切换到浅色主题" : "切换到深色主题"}>
              {theme === "dark" ? <Sun size={18} /> : <Moon size={18} />}
            </button>
          </div>
        </div>
      </header>

      <main id="top">
        <section className="hero">
          <div className="hero-glow" aria-hidden="true" />
          <div className="container hero-inner">
            <h1>让服务器管理更顺手</h1>
            <p className="hero-copy">
              NexTerm 支持终端、文件、Docker、数据库和 AI 助手。
              <br />
              安装在桌面，或部署到自己的服务器后用浏览器访问。
            </p>
            <div className="hero-actions">
              <a className="button button-primary" href={releases} target="_blank" rel="noreferrer">
                <Download size={17} />
                下载 NexTerm
              </a>
              <a className="button button-secondary" href={repository} target="_blank" rel="noreferrer">
                <CodeXml size={17} />
                查看源码
              </a>
            </div>
            <div className="platforms" aria-label="支持的平台">
              <span><Monitor size={15} /> Windows</span>
              <span><Monitor size={15} /> macOS</span>
              <span><Monitor size={15} /> Linux</span>
              <span><Server size={15} /> 浏览器版</span>
            </div>
            <div className="window hero-window">
              <div className="window-bar" aria-hidden="true">
                <span /><span /><span />
                <div>web-01 - NexTerm</div>
              </div>
              <Picture name="shell" alt="NexTerm 的 Shell 终端与命令块界面" eager />
            </div>
          </div>
        </section>

        <section className="section section-muted" id="showcase">
          <div className="container">
            <div className="section-heading">
              <span className="eyebrow">界面</span>
              <h2>产品界面</h2>
            </div>
            <div className="showcase-grid">
              {views.map((view) => (
                <article className="showcase-card" key={view.name}>
                  <div className="showcase-copy">
                    <h3>{view.title}</h3>
                    <p>{view.text}</p>
                  </div>
                  <div className="showcase-image">
                    <Picture name={view.name} alt={view.alt} />
                  </div>
                </article>
              ))}
            </div>
          </div>
        </section>

        <section className="section download-section" id="download">
          <div className="container">
            <div className="download-panel">
              <div>
                <h2>下载 NexTerm</h2>
                <p>桌面安装包和 Linux 服务端都在 GitHub Releases 提供，源代码采用 MIT 协议。</p>
              </div>
              <div className="download-actions">
                <a className="button button-primary" href={releases} target="_blank" rel="noreferrer">
                  <Download size={17} /> 前往下载
                </a>
                <a className="button button-secondary" href={`${repository}#下载与安装`} target="_blank" rel="noreferrer">
                  查看安装说明 <ArrowRight size={16} />
                </a>
              </div>
            </div>
          </div>
        </section>
      </main>

      <footer className="site-footer">
        <div className="container footer-inner">
          <a className="brand" href="#top">
            <img src="/brand/nexterm-mark-128.png" alt="" />
            <span>NexTerm</span>
          </a>
          <div className="footer-links">
            <a href={repository} target="_blank" rel="noreferrer">GitHub</a>
            <a href={releases} target="_blank" rel="noreferrer">Releases</a>
            <a href={`${repository}/issues`} target="_blank" rel="noreferrer">问题反馈</a>
            <a href={`${repository}/blob/master/LICENSE`} target="_blank" rel="noreferrer">MIT License</a>
          </div>
          <span className="copyright">© 2026 Hello-CTF</span>
        </div>
      </footer>
    </>
  );
}

export default App;

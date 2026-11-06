import { lazy, Suspense, useRef, type ComponentType, type KeyboardEvent } from "react";

export type Language = "zh" | "en";
export const DEMO_MODES = ["terminal", "ai", "background", "history", "files", "docker", "diagnostics", "database"] as const;
export type DemoMode = (typeof DEMO_MODES)[number];

type DemoComponent = ComponentType<{ language: Language }>;

// Each demo loads on first activation; only the history and background demos pull in xterm.
const demos: Record<DemoMode, DemoComponent> = {
  terminal: lazy(() => import("./demos/TerminalDemo")),
  ai: lazy(() => import("./demos/AiDemo")),
  background: lazy(() => import("./demos/BackgroundDemo")),
  history: lazy(() => import("./demos/HistoryDemo")),
  files: lazy(() => import("./demos/FilesDemo")),
  docker: lazy(() => import("./demos/DockerDemo")),
  diagnostics: lazy(() => import("./demos/DiagnosticsDemo")),
  database: lazy(() => import("./demos/DatabaseDemo")),
};

// [tab label, window title, one sentence on what the demo shows]
type DemoCopy = Record<DemoMode, [label: string, title: string, caption: string]>;

const copy: Record<Language, { tabs: string; sample: string; loading: string; demos: DemoCopy }> = {
  zh: {
    tabs: "功能演示", sample: "示例数据，不连接真实主机", loading: "正在加载演示…",
    demos: {
      terminal: ["终端", "deploy@web-01 — 终端", "命令列表记录每条命令的时间、耗时和退出码；断线后自动重连，也可以立即重试。"],
      ai: ["AI 助手", "AI 助手 — web-01", "AI 执行修改前默认先给你看命令，由你允许或拒绝。"],
      background: ["后台会话", "web-01 — 后台会话", "持久终端在后台继续运行，接管时补上离开期间的输出。"],
      history: ["终端历史", "web-01 — 终端历史", "终端输出按记录保存，可以搜索文本或按时间轴回放。"],
      files: ["文件", "web-01 — 文件", "直接编辑远程文件，未保存的修改会标出；文件树可以跟随终端目录。"],
      docker: ["Docker", "web-01 — Docker", "按 Compose 项目分组查看容器，跟随日志，或合并查看多个容器的日志。"],
      diagnostics: ["主机诊断", "web-01 — 主机", "查看监听端口和进程树，从端口跳到对应进程，结束进程前先确认。"],
      database: ["数据库", "app — PostgreSQL", "选择表名填入查询，运行后显示行数和耗时，可复制为 CSV。"],
    },
  },
  en: {
    tabs: "Feature demos", sample: "Sample data, no live connection", loading: "Loading demo…",
    demos: {
      terminal: ["Terminal", "deploy@web-01 — terminal", "The command list records each command's time, duration and exit code; a dropped connection reconnects on its own, or retry right away."],
      ai: ["AI assistant", "AI assistant — web-01", "By default, before the AI changes anything it shows you the command, and you allow or deny it."],
      background: ["Background sessions", "web-01 — background sessions", "Persistent terminals keep running in the background; taking one over catches up on the output you missed."],
      history: ["Terminal history", "web-01 — terminal history", "Terminal output is kept as recordings you can search as text or replay on a timeline."],
      files: ["Files", "web-01 — files", "Edit remote files in place with unsaved changes marked; the file tree can follow the terminal's directory."],
      docker: ["Docker", "web-01 — Docker", "Containers grouped by Compose project, with followed logs or several containers' logs merged."],
      diagnostics: ["Host diagnostics", "web-01 — host", "Listening ports and the process tree, with a jump from a port to its process and a confirmation before ending one."],
      database: ["Database", "app — PostgreSQL", "Pick a table to fill a query, run it to see the row count and timing, and copy the result as CSV."],
    },
  },
};


export function FeatureDemos({ language, mode, onChange }: { language: Language; mode: DemoMode; onChange: (mode: DemoMode) => void }) {
  const t = copy[language];
  const tabRefs = useRef<Partial<Record<DemoMode, HTMLButtonElement | null>>>({});
  const [, title, caption] = t.demos[mode];
  const Demo = demos[mode];

  const select = (next: DemoMode, focus: boolean) => {
    onChange(next);
    const tab = tabRefs.current[next];
    if (!tab) return;
    if (focus) tab.focus();
    tab.scrollIntoView({ inline: "nearest", block: "nearest" });
  };
  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    const index = DEMO_MODES.indexOf(mode);
    const next =
      event.key === "ArrowRight" ? DEMO_MODES[(index + 1) % DEMO_MODES.length]
      : event.key === "ArrowLeft" ? DEMO_MODES[(index - 1 + DEMO_MODES.length) % DEMO_MODES.length]
      : event.key === "Home" ? DEMO_MODES[0]
      : event.key === "End" ? DEMO_MODES[DEMO_MODES.length - 1]
      : null;
    if (!next) return;
    event.preventDefault();
    select(next, true);
  };

  return <div className="demo-showcase" id="demo">
    <div className="view-tabs" role="tablist" aria-label={t.tabs}>
      {DEMO_MODES.map((name) => <button
        key={name}
        ref={(element) => { tabRefs.current[name] = element; }}
        type="button"
        role="tab"
        id={`demo-tab-${name}`}
        aria-controls="demo-panel"
        aria-selected={name === mode}
        tabIndex={name === mode ? 0 : -1}
        className={name === mode ? "active" : ""}
        onClick={() => select(name, false)}
        onKeyDown={onKeyDown}
      >{t.demos[name][0]}</button>)}
    </div>
    <div className="window demo-window">
      <div className="window-bar" aria-hidden="true"><span /><span /><span /><div>{title}</div></div>
      <div className="feature-demo" id="demo-panel" role="tabpanel" aria-labelledby={`demo-tab-${mode} demo-sample`} aria-describedby="demo-caption" data-mode={mode}>
        <Suspense fallback={<div className="demo-loading">{t.loading}</div>}>
          <Demo language={language} />
        </Suspense>
      </div>
    </div>
    <p className="demo-note"><span id="demo-caption">{caption}</span><span id="demo-sample" className="demo-sample">{t.sample}</span></p>
  </div>;
}

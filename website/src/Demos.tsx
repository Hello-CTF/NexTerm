import { useEffect, useMemo, useRef, useState } from "react";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import { Check, FileCode2, Folder, Pause, Play, RotateCcw, Server, SquareTerminal } from "lucide-react";
import { formatReplayClock, TranscriptReplay, type ReplayChunk, type ReplaySink } from "../../src/features/terminal/transcriptReplay";
import "@xterm/xterm/css/xterm.css";

export type DemoMode = "shell" | "ai" | "files" | "replay";
type Language = "zh" | "en";

const copy = {
  zh: {
    sample: "交互演示 · 示例数据，不连接真实主机",
    terminal: "终端", ai: "AI 助手", files: "文件", replay: "回放",
    connected: "已连接", commands: "命令块", healthy: "服务正常运行",
    prompt: "api-server 最近响应变慢，帮我看看容器状态和资源占用。",
    reasoning: "我先检查容器资源，再决定是否需要重启。",
    diagnosis: "api-server 的内存使用率较高。重启操作需要确认。",
    approval: "需要你确认", allow: "模拟允许一次", deny: "拒绝",
    approved: "已模拟执行重启，健康检查通过。", denied: "已拒绝，没有执行任何操作。",
    reset: "重置演示", readOnly: "只读示例 · 点击文件切换预览",
    modified: "预览修改", original: "原始文件",
    replayHint: "拖动时间轴查看任意时刻的终端输出",
    play: "播放", pause: "暂停", speed: "回放倍速", progress: "回放进度",
  },
  en: {
    sample: "Interactive demo · Sample data, no live connection",
    terminal: "Terminal", ai: "AI assistant", files: "Files", replay: "Replay",
    connected: "Connected", commands: "Command blocks", healthy: "Service healthy",
    prompt: "api-server has slowed down. Check its container and resource usage.",
    reasoning: "I'll inspect container resources before considering a restart.",
    diagnosis: "api-server is close to its memory limit. A restart needs approval.",
    approval: "Your approval is required", allow: "Simulate allow once", deny: "Deny",
    approved: "Restart simulated. The health check passes.", denied: "Restart denied. Nothing was run.",
    reset: "Reset demo", readOnly: "Read-only sample · Select a file to preview",
    modified: "Preview change", original: "Original file",
    replayHint: "Drag the timeline to inspect terminal output at any point",
    play: "Play", pause: "Pause", speed: "Replay speed", progress: "Replay position",
  },
} as const;

const commands = [
  { name: "systemctl status api-server", output: [
    "● api-server.service - API service",
    "   Loaded: loaded (/etc/systemd/system/api-server.service; enabled)",
    "   Active: active (running) since 09:12 UTC; 6h ago",
    " Main PID: 842 (api-server)",
    "    Tasks: 12    Memory: 84.2M",
    "   CGroup: /system.slice/api-server.service",
  ] },
  { name: "docker ps", output: [
    "NAME          STATUS        PORTS                    IMAGE",
    "api-server    Up 6 hours    127.0.0.1:8080->8080/tcp   nexterm/api:2.4",
    "postgres      Up 6 hours    127.0.0.1:5432->5432/tcp   postgres:16",
    "redis         Up 6 hours    127.0.0.1:6379->6379/tcp   redis:7",
  ] },
  { name: "docker logs --tail 8 api-server", output: [
    "15:12:34 INFO  GET /healthz 200 8ms",
    "15:12:35 INFO  GET /api/projects 200 42ms",
    "15:12:36 WARN  GET /api/reports 200 612ms",
    "15:12:38 INFO  worker queue=3 active=2",
    "15:12:40 INFO  GET /healthz 200 7ms",
    "15:12:42 INFO  GET /api/projects 200 39ms",
    "15:12:44 WARN  GET /api/reports 200 584ms",
    "15:12:45 INFO  GET /healthz 200 7ms",
  ] },
  { name: "df -h / && free -h", output: [
    "Filesystem      Size  Used Avail Use% Mounted on",
    "/dev/vda1        80G   28G   48G  37% /",
    "               total   used   free  available",
    "Mem:           7.7Gi  3.1Gi  2.8Gi      4.2Gi",
    "Swap:          2.0Gi     0B  2.0Gi",
  ] },
];

const fileContents = {
  "docker-compose.yml": [
    "services:", "  api:", "    image: nexterm/api:2.4", "    restart: unless-stopped",
    "    ports:", "      - \"127.0.0.1:8080:8080\"", "    environment:",
    "      DATABASE_URL: postgres://postgres:5432/app", "      REDIS_URL: redis://redis:6379/0",
    "      LOG_LEVEL: info", "    depends_on:", "      postgres:",
    "        condition: service_healthy", "      redis:",
    "        condition: service_started", "    healthcheck:",
    "      test: [\"CMD\", \"curl\", \"-f\", \"http://localhost:8080/healthz\"]",
    "      interval: 30s", "      timeout: 5s", "      retries: 3", "",
    "  postgres:", "    image: postgres:16", "    volumes:",
    "      - postgres-data:/var/lib/postgresql/data", "",
    "  redis:", "    image: redis:7", "",
    "volumes:", "  postgres-data:",
  ],
  "nginx.conf": [
    "upstream api_backend {", "    server 127.0.0.1:8080;", "    keepalive 32;", "}", "",
    "server {", "    listen 443 ssl;", "    server_name api.example.test;", "",
    "    location /api/ {", "        proxy_pass http://api_backend;",
    "        proxy_connect_timeout 5s;", "        proxy_read_timeout 30s;", "    }", "",
    "    location /healthz {", "        proxy_pass http://api_backend;", "    }", "}",
  ],
  "README.md": [
    "# API service", "", "Example project used by the NexTerm demo.",
    "No files or hosts shown here are real.", "", "## Deploy", "",
    "1. Review docker-compose.yml", "2. Check the health endpoint",
    "3. Inspect logs and resource usage", "", "## Services", "",
    "- api-server: HTTP API", "- postgres: primary database", "- redis: cache and jobs",
  ],
} as const;
type DemoFile = keyof typeof fileContents;

const encoder = new TextEncoder();
const replaySource: [number, string][] = [
  [0, "\x1b[32mdeploy@web-01\x1b[0m:\x1b[34m~/apps/api-server\x1b[0m$ ls\r\n"],
  [900, "README.md  docker-compose.yml  nginx.conf\r\n"],
  [2600, "\x1b[32mdeploy@web-01\x1b[0m:\x1b[34m~/apps/api-server\x1b[0m$ systemctl status api-server\r\n"],
  [3500, "● api-server.service - API service\r\n   Loaded: loaded; enabled\r\n   Active: \x1b[32mactive (running)\x1b[0m since 09:12 UTC\r\n   Main PID: 842    Memory: 84.2M\r\n"],
  [7800, "\x1b[32mdeploy@web-01\x1b[0m:\x1b[34m~/apps/api-server\x1b[0m$ docker ps\r\n"],
  [9000, "NAME         STATUS       PORTS\r\napi-server   Up 6 hours   127.0.0.1:8080->8080/tcp\r\npostgres     Up 6 hours   127.0.0.1:5432->5432/tcp\r\nredis        Up 6 hours   127.0.0.1:6379->6379/tcp\r\n"],
  [14200, "\x1b[32mdeploy@web-01\x1b[0m:\x1b[34m~/apps/api-server\x1b[0m$ curl -s localhost:8080/healthz\r\n"],
  [15500, "{\"status\":\"ok\",\"uptime\":\"6h 12m\"}\r\n"],
  [20100, "\x1b[32mdeploy@web-01\x1b[0m:\x1b[34m~/apps/api-server\x1b[0m$ docker logs --tail 3 api-server\r\n"],
  [21400, "15:12:40 INFO  GET /healthz 200 7ms\r\n15:12:42 INFO  GET /api/projects 200 39ms\r\n15:12:45 INFO  GET /healthz 200 7ms\r\n"],
  [25000, "\x1b[32mdeploy@web-01\x1b[0m:\x1b[34m~/apps/api-server\x1b[0m$ "],
];
const replayChunks: ReplayChunk[] = replaySource.map(([ts, data], index) => ({
  seq: index + 1, ts, kind: 0, data: encoder.encode(data),
}));
const duration = replaySource[replaySource.length - 1][0] - replaySource[0][0];

function Header({ language, title, host = "web-01" }: { language: Language; title: string; host?: string }) {
  return <div className="demo-header"><strong>{title}</strong><span className="demo-host"><i />{host}</span><span className="demo-sample">{copy[language].sample}</span></div>;
}

function ShellDemo({ language }: { language: Language }) {
  const [selected, setSelected] = useState(2);
  const terminalRef = useRef<HTMLDivElement>(null);
  const t = copy[language];
  const selectCommand = (index: number) => {
    setSelected(index);
    const container = terminalRef.current;
    const block = container?.querySelectorAll<HTMLElement>(".demo-terminal-block")[index];
    if (!container || !block) return;
    const top = block.offsetTop - container.offsetTop;
    if (top < container.scrollTop) container.scrollTop = top;
    else if (top + block.offsetHeight > container.scrollTop + container.clientHeight) container.scrollTop = top + block.offsetHeight - container.clientHeight;
  };
  return <div className="demo-scene demo-shell">
    <Header language={language} title={t.terminal} host={"web-01 · " + t.connected} />
    <div className="demo-shell-grid">
      <div className="demo-terminal-copy" ref={terminalRef}>
        {commands.map((command, index) => <div key={command.name} className={`demo-terminal-block${selected === index ? " selected" : ""}`}>
          <div className="demo-prompt">deploy@web-01:~/apps/api-server$ <span>{command.name}</span></div>
          <pre>{command.output.join("\n")}</pre>
        </div>)}
        <div className="demo-prompt">deploy@web-01:~/apps/api-server$ <span className="demo-cursor" /></div>
      </div>
      <div className="demo-command-list"><div className="demo-command-title">{t.commands}<small>{commands.length}</small></div>
        {commands.map((command, index) => <button key={command.name} type="button" className={selected === index ? "selected" : ""} onClick={() => selectCommand(index)} aria-pressed={selected === index}><span>#{index + 1}</span><span>{command.name}</span></button>)}
      </div>
    </div>
    <div className="demo-footer"><Check size={13} />{t.healthy}<span>SSH · web-01</span></div>
  </div>;
}

function AiDemo({ language }: { language: Language }) {
  const [decision, setDecision] = useState<"pending" | "approved" | "denied">("pending");
  const t = copy[language];
  return <div className="demo-scene demo-ai">
    <Header language={language} title={t.ai} />
    <div className="demo-ai-conversation">
      <div className="demo-ai-user">{t.prompt}</div>
      <div className="demo-ai-message"><span className="demo-avatar">N</span><span>{t.reasoning}</span></div>
      <div className="demo-tool-call"><div><SquareTerminal size={14} /><code>docker stats --no-stream</code><Check size={13} /></div><pre>{"NAME          CPU %   MEM USAGE / LIMIT\napi-server    4.8%    812MiB / 1GiB\npostgres      0.7%    126MiB / 2GiB"}</pre></div>
      <div className="demo-ai-message"><span className="demo-avatar">N</span><span>{t.diagnosis}</span></div>
      <div className="demo-approval"><strong>{t.approval}</strong><code>docker restart api-server</code>
        {decision === "pending" ? <div className="demo-approval-actions"><button type="button" onClick={() => setDecision("approved")}>{t.allow}</button><button type="button" onClick={() => setDecision("denied")}>{t.deny}</button></div> : <div className="demo-ai-result" role="status"><span>{decision === "approved" ? t.approved : t.denied}</span><button type="button" onClick={() => setDecision("pending")}><RotateCcw size={12} />{t.reset}</button></div>}
      </div>
    </div>
  </div>;
}

function FilesDemo({ language }: { language: Language }) {
  const [file, setFile] = useState<DemoFile>("docker-compose.yml");
  const [changed, setChanged] = useState(false);
  const t = copy[language];
  const content: string[] = [...fileContents[file]];
  if (changed && file === "nginx.conf") content[12] = "        proxy_read_timeout 45s;";
  return <div className="demo-scene demo-files">
    <Header language={language} title={t.files} />
    <div className="demo-files-grid">
      <nav className="demo-file-tree" aria-label={t.files}><div><Folder size={14} />apps / api-server</div>
        {(Object.keys(fileContents) as DemoFile[]).map((name) => <button key={name} type="button" className={file === name ? "selected" : ""} onClick={() => { setFile(name); setChanged(false); }} aria-current={file === name ? "page" : undefined}><FileCode2 size={14} />{name}</button>)}
      </nav>
      <div className="demo-editor"><div className="demo-editor-bar"><span>{file}</span><span>{t.readOnly}</span></div>
        <div className="demo-code" aria-label={file}>{content.map((line, index) => <div key={index}><span>{index + 1}</span><code>{line || "\u00a0"}</code></div>)}</div>
      </div>
    </div>
    <div className="demo-footer"><Server size={13} />SFTP · web-01<button type="button" onClick={() => { setFile("nginx.conf"); setChanged((value) => !value); }}>{changed && file === "nginx.conf" ? t.original : t.modified}</button></div>
  </div>;
}

function sink(terminal: Terminal): ReplaySink {
  return { reset: () => terminal.reset(), write: (data) => terminal.write(data), resize: (cols, rows) => terminal.resize(cols, rows) };
}

function ReplayDemo({ language }: { language: Language }) {
  const t = copy[language];
  const replay = useMemo(() => new TranscriptReplay(replayChunks), []);
  const containerRef = useRef<HTMLDivElement>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const positionRef = useRef(18000);
  const [position, setPosition] = useState(18000);
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState(1);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const terminal = new Terminal({ disableStdin: true, cursorBlink: false, fontFamily: '"Cascadia Mono", "JetBrains Mono", monospace', fontSize: 12, lineHeight: 1.2, scrollback: 100, theme: { background: "#101217", foreground: "#c6cbd6", cursor: "#101217" } });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(container);
    terminalRef.current = terminal;
    fit.fit();
    replay.seek(sink(terminal), positionRef.current);
    const observer = new ResizeObserver(() => fit.fit());
    observer.observe(container);
    return () => { observer.disconnect(); terminal.dispose(); terminalRef.current = null; };
  }, [replay]);

  useEffect(() => {
    if (!playing) return;
    let previous = performance.now();
    const timer = window.setInterval(() => {
      const terminal = terminalRef.current;
      if (!terminal) return;
      const now = performance.now();
      positionRef.current = replay.playStep(sink(terminal), positionRef.current + (now - previous) * speed);
      previous = now;
      setPosition(positionRef.current);
      if (positionRef.current >= duration) setPlaying(false);
    }, 50);
    return () => window.clearInterval(timer);
  }, [playing, replay, speed]);

  const seek = (value: number) => {
    const terminal = terminalRef.current;
    if (!terminal) return;
    positionRef.current = replay.seek(sink(terminal), value);
    setPosition(positionRef.current);
  };
  const togglePlay = () => {
    if (!playing && positionRef.current >= duration) seek(0);
    setPlaying(!playing);
  };

  return <div className="demo-scene demo-replay">
    <Header language={language} title={t.replay} host="web-01 · SSH" />
    <div className="demo-replay-controls"><button type="button" onClick={togglePlay} aria-label={playing ? t.pause : t.play}>{playing ? <Pause size={14} /> : <Play size={14} />}{playing ? t.pause : t.play}</button>
      <select aria-label={t.speed} value={speed} onChange={(event) => setSpeed(Number(event.target.value))}><option value="1">1×</option><option value="2">2×</option><option value="4">4×</option></select>
      <input type="range" aria-label={t.progress} min="0" max={duration} step="100" value={position} onChange={(event) => seek(Number(event.target.value))} />
      <span>{formatReplayClock(position)} / {formatReplayClock(duration)}</span>
    </div>
    <div className="demo-replay-terminal" ref={containerRef} aria-label={t.replayHint} />
    <div className="demo-footer"><span>{t.replayHint}</span><span>deploy@web-01</span></div>
  </div>;
}

export function FeatureDemo({ mode, language }: { mode: DemoMode; language: Language }) {
  return <div className="feature-demo" data-mode={mode}>
    {mode === "shell" && <ShellDemo language={language} />}
    {mode === "ai" && <AiDemo language={language} />}
    {mode === "files" && <FilesDemo language={language} />}
    {mode === "replay" && <ReplayDemo language={language} />}
  </div>;
}

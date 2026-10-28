import { useEffect, useMemo, useRef, useState } from "react";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import { Box, Check, Database, FileCode2, Folder, Pause, Play, RotateCcw, Server, SquareTerminal, Table2, X } from "lucide-react";
import { formatReplayClock, TranscriptReplay, type ReplayChunk, type ReplaySink } from "../../src/features/terminal/transcriptReplay";
import "@xterm/xterm/css/xterm.css";

export type DemoMode = "shell" | "ai" | "files" | "replay" | "docker" | "db";
type Language = "zh" | "en";

const copy = {
  zh: {
    sample: "交互演示 · 示例数据，不连接真实主机",
    terminal: "终端", ai: "AI 助手", files: "文件", replay: "回放",
    connected: "已连接", commands: "命令块", healthy: "服务正常运行",
    prompt: "api-server 最近响应变慢，请检查容器状态和资源占用。",
    reasoning: "我先检查容器资源，再决定是否需要重启。",
    diagnosis: "api-server 的内存使用率较高。重启操作需要确认。",
    approval: "需要你确认", allow: "模拟允许一次", deny: "拒绝",
    approved: "重启完成，健康检查通过，内存回落到 214MiB。", denied: "已拒绝，没有执行任何操作。容器保持原状，可以改用其他排查方式。",
    skipped: "未执行",
    reset: "重置演示", readOnly: "只读示例 · 选择文件以切换预览",
    modified: "预览修改", original: "原始文件",
    replayHint: "拖动时间轴查看任意时刻的终端输出",
    play: "播放", pause: "暂停", speed: "回放倍速", progress: "回放进度",
    docker: "容器", containers: "容器", logs: "日志", restart: "重启", stop: "停止", start: "启动",
    running: "运行中", stopped: "已停止", restarting: "重启中",
    database: "数据库", tables: "表", run: "运行", rows: "行", dangerQuery: "尝试 DELETE",
    dangerNote: "DELETE / DROP 等危险语句执行前会要求确认。示例中不会执行。",
  },
  en: {
    sample: "Interactive demo · Sample data, no live connection",
    terminal: "Terminal", ai: "AI assistant", files: "Files", replay: "Replay",
    connected: "Connected", commands: "Command blocks", healthy: "Service healthy",
    prompt: "api-server has slowed down. Check its container and resource usage.",
    reasoning: "I'll inspect container resources before considering a restart.",
    diagnosis: "api-server is close to its memory limit. A restart needs approval.",
    approval: "Your approval is required", allow: "Simulate allow once", deny: "Deny",
    approved: "Restart finished. The health check passes and memory dropped to 214MiB.", denied: "Restart denied. Nothing was run, and the container is unchanged.",
    skipped: "Not run",
    reset: "Reset demo", readOnly: "Read-only sample · Select a file to preview",
    modified: "Preview change", original: "Original file",
    replayHint: "Drag the timeline to inspect terminal output at any point",
    play: "Play", pause: "Pause", speed: "Replay speed", progress: "Replay position",
    docker: "Containers", containers: "Containers", logs: "Logs", restart: "Restart", stop: "Stop", start: "Start",
    running: "Running", stopped: "Stopped", restarting: "Restarting",
    database: "Database", tables: "Tables", run: "Run", rows: "rows", dangerQuery: "Try DELETE",
    dangerNote: "Dangerous statements such as DELETE or DROP ask for confirmation first. The sample never runs them.",
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
      {decision === "pending" ? <div className="demo-approval"><strong>{t.approval}</strong><code>docker restart api-server</code>
        <div className="demo-approval-actions"><button type="button" onClick={() => setDecision("approved")}>{t.allow}</button><button type="button" onClick={() => setDecision("denied")}>{t.deny}</button></div>
      </div> : decision === "approved" ? <>
        <div className="demo-tool-call"><div><SquareTerminal size={14} /><code>docker restart api-server</code><Check size={13} /></div><pre>api-server</pre></div>
        <div className="demo-tool-call"><div><SquareTerminal size={14} /><code>curl -s localhost:8080/healthz && docker stats --no-stream api-server</code><Check size={13} /></div><pre>{"{\"status\":\"ok\"}\nNAME          CPU %   MEM USAGE / LIMIT\napi-server    1.2%    214MiB / 1GiB"}</pre></div>
        <div className="demo-ai-message"><span className="demo-avatar">N</span><span>{t.approved}</span></div>
      </> : <>
        <div className="demo-tool-call demo-tool-skipped"><div><SquareTerminal size={14} /><code>docker restart api-server</code><X size={13} /></div><pre>{t.skipped}</pre></div>
        <div className="demo-ai-message"><span className="demo-avatar">N</span><span>{t.denied}</span></div>
      </>}
      {decision !== "pending" && <div className="demo-ai-result"><button type="button" onClick={() => setDecision("pending")}><RotateCcw size={12} />{t.reset}</button></div>}
    </div>
  </div>;
}

type Token = [string, string | null];

// highlightLine colors the sample files by their syntax; it covers only what the samples use.
function highlightLine(file: DemoFile, line: string): Token[] {
  if (file === "README.md") {
    if (/^#/.test(line)) return [[line, "tok-heading"]];
    const list = /^(\s*(?:-|\d+\.)\s)(.*)$/.exec(line);
    if (list) return [[list[1], "tok-punct"], ...highlightInline(list[2])];
    return highlightInline(line);
  }
  const comment = line.indexOf("#");
  if (comment >= 0 && !/["']/.test(line.slice(0, comment))) return [...highlightLine(file, line.slice(0, comment)), [line.slice(comment), "tok-comment"]];
  if (file === "docker-compose.yml") {
    const key = /^(\s*-?\s*)([\w.-]+)(:)(.*)$/.exec(line);
    if (key) return [[key[1], "tok-punct"], [key[2], "tok-key"], [key[3], "tok-punct"], ...highlightValue(key[4])];
    const item = /^(\s*-\s)(.*)$/.exec(line);
    if (item) return [[item[1], "tok-punct"], ...highlightValue(item[2])];
    return highlightValue(line);
  }
  const directive = /^(\s*)([\w_]+)(.*)$/.exec(line);
  if (directive) return [[directive[1], null], [directive[2], "tok-key"], ...highlightValue(directive[3])];
  return [[line, "tok-punct"]];
}

function highlightValue(text: string): Token[] {
  return text.split(/("[^"]*"|\b\d+(?:\.\d+)*[a-z]*\b|[{}[\];,])/).filter(Boolean).map((part) => [
    part,
    part.startsWith("\"") ? "tok-string" : /^\d/.test(part) ? "tok-number" : /^[{}[\];,]$/.test(part) ? "tok-punct" : null,
  ]);
}

function highlightInline(text: string): Token[] {
  return text.split(/(\S+\.(?:yml|conf|md))/).filter(Boolean).map((part) => [part, /\.(?:yml|conf|md)$/.test(part) ? "tok-string" : null]);
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
        <div className="demo-code" aria-label={file}>{content.map((line, index) => <div key={index}><span>{index + 1}</span><code>{line ? highlightLine(file, line).map(([text, kind], part) => kind ? <span key={part} className={kind}>{text}</span> : text) : "\u00a0"}</code></div>)}</div>
      </div>
    </div>
    <div className="demo-footer"><Server size={13} />SFTP · web-01<button type="button" onClick={() => { setFile("nginx.conf"); setChanged((value) => !value); }}>{changed && file === "nginx.conf" ? t.original : t.modified}</button></div>
  </div>;
}

function sink(terminal: Terminal): ReplaySink {
  return { reset: () => terminal.reset(), write: (data, callback) => terminal.write(data, callback), resize: (cols, rows) => terminal.resize(cols, rows) };
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

type ContainerState = "running" | "stopped" | "restarting";

const containerRows = [
  { name: "api-server", image: "nexterm/api:2.4", cpu: "4.8%", mem: "812MiB", logs: ["15:12:40 INFO  GET /healthz 200 7ms", "15:12:42 INFO  GET /api/projects 200 39ms", "15:12:44 WARN  GET /api/reports 200 584ms", "15:12:45 INFO  GET /healthz 200 7ms"] },
  { name: "postgres", image: "postgres:16", cpu: "0.7%", mem: "126MiB", logs: ["LOG:  checkpoint starting: time", "LOG:  checkpoint complete: wrote 42 buffers", "LOG:  database system is ready to accept connections"] },
  { name: "redis", image: "redis:7", cpu: "0.2%", mem: "18MiB", logs: ["1:M * Ready to accept connections tcp", "1:M * DB saved on disk", "1:M * Background saving terminated with success"] },
  { name: "nginx", image: "nginx:1.27", cpu: "0.1%", mem: "9MiB", logs: ["10.0.0.8 - - \"GET / HTTP/1.1\" 200 612", "10.0.0.8 - - \"GET /api/projects HTTP/1.1\" 200 1840"] },
];

function DockerDemo({ language }: { language: Language }) {
  const t = copy[language];
  const [selected, setSelected] = useState(0);
  const [states, setStates] = useState<ContainerState[]>(containerRows.map(() => "running"));
  const container = containerRows[selected];
  const state = states[selected];
  const setState = (index: number, next: ContainerState) => setStates((current) => current.map((value, i) => (i === index ? next : value)));
  const restart = () => {
    setState(selected, "restarting");
    const index = selected;
    window.setTimeout(() => setState(index, "running"), 900);
  };
  const label = (value: ContainerState) => (value === "running" ? t.running : value === "stopped" ? t.stopped : t.restarting);
  return <div className="demo-scene demo-docker">
    <Header language={language} title={t.docker} host="web-01 · Docker" />
    <div className="demo-docker-grid">
      <div className="demo-docker-table" role="listbox" aria-label={t.containers}>
        {containerRows.map((row, index) => <button key={row.name} type="button" role="option" aria-selected={selected === index} className={selected === index ? "selected" : ""} onClick={() => setSelected(index)}>
          <i className={`demo-dot ${states[index]}`} />
          <span className="demo-docker-name">{row.name}</span>
          <span className="demo-docker-image">{row.image}</span>
          <span className="demo-docker-stat">{states[index] === "stopped" ? "—" : row.cpu}</span>
          <span className="demo-docker-stat">{states[index] === "stopped" ? "—" : row.mem}</span>
        </button>)}
      </div>
      <div className="demo-docker-logs">
        <div className="demo-editor-bar"><span><Box size={12} /> {container.name} · {label(state)}</span>
          <span className="demo-docker-actions">
            <button type="button" disabled={state !== "running"} onClick={restart}><RotateCcw size={12} />{t.restart}</button>
            {state === "stopped"
              ? <button type="button" onClick={() => setState(selected, "running")}><Play size={12} />{t.start}</button>
              : <button type="button" disabled={state !== "running"} onClick={() => setState(selected, "stopped")}><Pause size={12} />{t.stop}</button>}
          </span>
        </div>
        <pre aria-label={t.logs}>{state === "stopped" ? "" : container.logs.join("\n")}</pre>
      </div>
    </div>
    <div className="demo-footer"><Server size={13} />{containerRows.length} {t.containers}<span>docker · web-01</span></div>
  </div>;
}

const tableData: Record<string, { columns: string[]; rows: string[][] }> = {
  orders: { columns: ["id", "customer", "total", "status"], rows: [["1042", "acme", "¥ 1,280", "paid"], ["1041", "globex", "¥ 860", "shipped"], ["1040", "initech", "¥ 2,400", "paid"], ["1039", "acme", "¥ 320", "refunded"]] },
  users: { columns: ["id", "name", "role", "last_login"], rows: [["7", "alice", "admin", "2026-10-23"], ["8", "bob", "ops", "2026-10-22"], ["9", "carol", "dev", "2026-10-20"]] },
  products: { columns: ["sku", "name", "stock"], rows: [["NX-01", "Terminal Pro", "128"], ["NX-02", "Sync Seat", "64"], ["NX-03", "Device Agent", "512"]] },
};

function DatabaseDemo({ language }: { language: Language }) {
  const t = copy[language];
  const [table, setTable] = useState("orders");
  const [query, setQuery] = useState("SELECT * FROM orders ORDER BY id DESC LIMIT 4;");
  const [result, setResult] = useState<string | null>("orders");
  const [warning, setWarning] = useState(false);
  const pick = (name: string) => {
    setTable(name);
    setQuery(`SELECT * FROM ${name} LIMIT 4;`);
    setResult(name);
    setWarning(false);
  };
  const run = () => {
    if (/^\s*(delete|drop|truncate|update)\b/i.test(query)) {
      setWarning(true);
      return;
    }
    setWarning(false);
    setResult(table);
  };
  const data = result ? tableData[result] : null;
  return <div className="demo-scene demo-db">
    <Header language={language} title={t.database} host="app · PostgreSQL" />
    <div className="demo-files-grid">
      <nav className="demo-file-tree" aria-label={t.tables}><div><Database size={14} />app / public</div>
        {Object.keys(tableData).map((name) => <button key={name} type="button" className={table === name ? "selected" : ""} onClick={() => pick(name)} aria-current={table === name ? "page" : undefined}><Table2 size={14} />{name}</button>)}
      </nav>
      <div className="demo-editor">
        <div className="demo-db-query">
          <textarea aria-label="SQL" value={query} spellCheck={false} onChange={(event) => setQuery(event.target.value)} />
          <div>
            <button type="button" className="primary" onClick={run}><Play size={12} />{t.run}</button>
            <button type="button" onClick={() => { setQuery(`DELETE FROM ${table};`); setWarning(false); }}>{t.dangerQuery}</button>
          </div>
        </div>
        {warning ? <div className="demo-db-warning" role="status">{t.dangerNote}</div> : data && <div className="demo-db-result">
          <table><thead><tr>{data.columns.map((column) => <th key={column}>{column}</th>)}</tr></thead>
            <tbody>{data.rows.map((row) => <tr key={row[0]}>{row.map((cell, index) => <td key={index}>{cell}</td>)}</tr>)}</tbody></table>
        </div>}
      </div>
    </div>
    <div className="demo-footer"><Check size={13} />{data && !warning ? `${data.rows.length} ${t.rows}` : "—"}<span>PostgreSQL 16</span></div>
  </div>;
}

export function FeatureDemo({ mode, language }: { mode: DemoMode; language: Language }) {
  return <div className="feature-demo" data-mode={mode}>
    {mode === "shell" && <ShellDemo language={language} />}
    {mode === "ai" && <AiDemo language={language} />}
    {mode === "files" && <FilesDemo language={language} />}
    {mode === "replay" && <ReplayDemo language={language} />}
    {mode === "docker" && <DockerDemo language={language} />}
    {mode === "db" && <DatabaseDemo language={language} />}
  </div>;
}

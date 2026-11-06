import { useEffect, useMemo, useRef, useState } from "react";
import { formatReplayClock, REPLAY_SPEEDS, REPLAY_TICK_MS, TranscriptReplay } from "../../../src/features/terminal/transcriptReplay";
import { formatTranscriptBytes, formatTranscriptTime, stripAnsi } from "../../../src/features/terminal/transcriptText";
import { IconArrowLeft, IconHistory, IconPlay, IconSearch } from "../../../src/ui/icons";
import { pick, type DemoProps, type Language } from "./common";
import { chunksOf, sinkOf, useReplayTerminal } from "./replayTerminal";

const copy = {
  zh: {
    title: "终端历史", host: "web-01", records: "终端记录", back: "记录列表",
    badges: { active: "进行中", ended: "已结束", aborted: "异常结束" },
    views: { text: "文本", replay: "回放" }, viewLabel: "记录视图",
    searchPlaceholder: "在选中的记录里搜索…", searchLabel: "搜索终端记录", search: "搜索", noMatch: "没有匹配的内容",
    replayHint: "按录制时间轴回放终端输出，期间不可输入",
    play: "播放", pause: "暂停", playLabel: "开始回放", pauseLabel: "暂停回放", speed: "回放倍速", progress: "回放进度",
  },
  en: {
    title: "Terminal history", host: "web-01", records: "Recordings", back: "Recordings",
    badges: { active: "Recording", ended: "Ended", aborted: "Ended abnormally" },
    views: { text: "Text", replay: "Replay" }, viewLabel: "Recording view",
    searchPlaceholder: "Search the selected recording…", searchLabel: "Search terminal recordings", search: "Search", noMatch: "No matches",
    replayHint: "Replays the terminal output on the recorded timeline; input is disabled",
    play: "Play", pause: "Pause", playLabel: "Start replay", pauseLabel: "Pause replay", speed: "Replay speed", progress: "Replay position",
  },
};

const P = "\x1b[32mdeploy@web-01\x1b[0m:\x1b[34m~/apps/api-server\x1b[0m$ ";
type Status = "active" | "ended" | "aborted";
interface Recording {
  id: string;
  startedAt: number;
  durationMs: number | null;
  status: Status;
  initial: number;
  source: [number, string][];
}

const recordings: Recording[] = [
  { id: "deploy", startedAt: Date.UTC(2026, 10, 6, 7, 10), durationMs: 25000, status: "ended", initial: 18000, source: [
    [0, `${P}ls\r\n`], [900, "README.md  docker-compose.yml  nginx.conf\r\n"],
    [2600, `${P}systemctl status api-server\r\n`],
    [3500, "● api-server.service - API service\r\n   Loaded: loaded; enabled\r\n   Active: \x1b[32mactive (running)\x1b[0m since 09:12 UTC\r\n   Main PID: 842    Memory: 84.2M\r\n"],
    [7800, `${P}docker ps\r\n`],
    [9000, "NAME         STATUS       PORTS\r\napi-server   Up 6 hours   127.0.0.1:8080->8080/tcp\r\npostgres     Up 6 hours   127.0.0.1:5432->5432/tcp\r\nredis        Up 6 hours   127.0.0.1:6379->6379/tcp\r\n"],
    [14200, `${P}curl -s localhost:8080/healthz\r\n`], [15500, "{\"status\":\"ok\",\"uptime\":\"6h 12m\"}\r\n"],
    [20100, `${P}docker logs --tail 3 api-server\r\n`],
    [21400, "15:12:40 INFO  GET /healthz 200 7ms\r\n15:12:42 INFO  GET /api/projects 200 39ms\r\n15:12:45 WARN  GET /api/reports 200 584ms\r\n"],
    [25000, P],
  ] },
  { id: "live", startedAt: Date.UTC(2026, 10, 6, 7, 31), durationMs: null, status: "active", initial: 6000, source: [
    [0, `${P}journalctl -u api-server -f\r\n`],
    [1200, "Nov 06 15:31:02 web-01 api-server[842]: GET /healthz 200 7ms\r\n"],
    [3400, "Nov 06 15:31:04 web-01 api-server[842]: WARN GET /api/reports 200 612ms\r\n"],
    [6000, "Nov 06 15:31:07 web-01 api-server[842]: worker queue=388 active=8\r\n"],
  ] },
  { id: "upgrade", startedAt: Date.UTC(2026, 10, 5, 14, 47), durationMs: 9000, status: "aborted", initial: 9000, source: [
    [0, `${P}sudo apt-get upgrade -y\r\n`], [1500, "Reading package lists... Done\r\nBuilding dependency tree... Done\r\n"],
    [4200, "Unpacking openssl (3.0.13-0ubuntu3.6) over (3.0.13-0ubuntu3.5) ...\r\n"], [9000, "Setting up openssl (3.0.13-0ubuntu3.6) ...\r\n"],
  ] },
];

const encoder = new TextEncoder();

function durationText(language: Language, ms: number): string {
  const seconds = Math.round(ms / 1000);
  if (language === "zh") return seconds < 60 ? `${seconds} 秒` : `${Math.floor(seconds / 60)} 分钟`;
  return seconds < 60 ? `${seconds} s` : `${Math.floor(seconds / 60)} min`;
}

function bytesOf(recording: Recording): number {
  return recording.source.reduce((sum, [, data]) => sum + encoder.encode(data).byteLength, 0);
}

function ReplayView({ recording, t }: { recording: Recording; t: (typeof copy)["zh"] | (typeof copy)["en"] }) {
  const replay = useMemo(() => new TranscriptReplay(chunksOf(recording.source)), [recording]);
  const duration = replay.durationMs();
  const containerRef = useRef<HTMLDivElement>(null);
  const positionRef = useRef(recording.initial);
  const [position, setPosition] = useState(recording.initial);
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState<number>(REPLAY_SPEEDS[0]);
  const terminalRef = useReplayTerminal(containerRef, replay, recording.initial);

  useEffect(() => {
    if (!playing) return;
    let previous = performance.now();
    const timer = window.setInterval(() => {
      const terminal = terminalRef.current;
      if (!terminal) return;
      const now = performance.now();
      positionRef.current = replay.playStep(sinkOf(terminal), positionRef.current + (now - previous) * speed);
      previous = now;
      setPosition(positionRef.current);
      if (positionRef.current >= duration) setPlaying(false);
    }, REPLAY_TICK_MS);
    return () => window.clearInterval(timer);
  }, [playing, replay, speed, duration, terminalRef]);

  const seek = (value: number) => {
    const terminal = terminalRef.current;
    if (!terminal) return;
    positionRef.current = replay.seek(sinkOf(terminal), value);
    setPosition(positionRef.current);
  };
  const togglePlay = () => {
    if (!playing && positionRef.current >= duration) seek(0);
    setPlaying(!playing);
  };

  return <div className="demo-replay">
    <div className="demo-replay-controls">
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={togglePlay} aria-label={playing ? t.pauseLabel : t.playLabel}>
        {playing ? null : <IconPlay size={12} />}{playing ? t.pause : t.play}
      </button>
      <select className="nx-input demo-select" aria-label={t.speed} value={speed} onChange={(event) => setSpeed(Number(event.target.value))}>
        {REPLAY_SPEEDS.map((value) => <option key={value} value={value}>{value}x</option>)}
      </select>
      <input type="range" aria-label={t.progress} min={0} max={Math.max(1, duration)} step={100} value={Math.min(position, duration)} onChange={(event) => seek(Number(event.target.value))} />
      <span className="demo-replay-clock">{formatReplayClock(position)} / {formatReplayClock(duration)}</span>
    </div>
    <div className="demo-xterm" ref={containerRef} />
  </div>;
}

function TextView({ recording, needle, emptyText, matches }: { recording: Recording; needle: string; emptyText: string; matches: number[] | null }) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [focused, setFocused] = useState<number | null>(null);
  const lines = recording.source.map(([, data], seq) => ({ seq, text: stripAnsi(data).replace(/\r/g, "") }));
  const jump = (seq: number) => {
    setFocused(seq);
    const container = containerRef.current;
    const target = container?.querySelector<HTMLElement>(`[data-chunk-seq="${seq}"]`);
    if (container && target) container.scrollTop = Math.max(0, target.offsetTop - container.offsetTop - container.clientHeight / 3);
  };
  return <>
    {matches && <div className="demo-matches">
      {matches.length === 0 && <div className="nx-hint">{emptyText}</div>}
      {matches.map((seq) => <button key={seq} type="button" onClick={() => jump(seq)}>{lines[seq].text.trim().split("\n").find((line) => line.toLowerCase().includes(needle)) ?? lines[seq].text}</button>)}
    </div>}
    <div className="demo-transcript-text" ref={containerRef}>
      {lines.map((line) => <div key={line.seq} data-chunk-seq={line.seq} className={focused === line.seq ? "is-focused" : undefined}>{line.text}</div>)}
    </div>
  </>;
}

export default function HistoryDemo({ language }: DemoProps) {
  const t = pick(language, copy);
  const [selectedId, setSelectedId] = useState(recordings[0].id);
  const [mobileList, setMobileList] = useState(false);
  const [mode, setMode] = useState<"text" | "replay">("replay");
  const [query, setQuery] = useState("");
  const [matches, setMatches] = useState<{ query: string; seqs: number[] } | null>(null);
  const selected = recordings.find((r) => r.id === selectedId) ?? recordings[0];

  const runSearch = () => {
    const needle = query.trim().toLowerCase();
    if (!needle) {
      setMatches(null);
      return;
    }
    const seqs = selected.source.flatMap(([, data], seq) => (stripAnsi(data).toLowerCase().includes(needle) ? [seq] : []));
    setMatches({ query: needle, seqs });
  };
  const select = (id: string) => {
    setSelectedId(id);
    setMatches(null);
    setMobileList(false);
  };

  return <div className="nx-pane demo-history" data-mobile-view={mobileList ? "list" : "detail"}>
    <div className="nx-toolbar">
      <IconHistory size={14} className="demo-muted" />
      <span className="nx-toolbar-title">{t.title}</span>
      <span className="nx-hint">{t.host}</span>
    </div>
    <div className="demo-history-body">
      <div className="demo-history-list">
        <table className="nx-table">
          <thead><tr><th>{t.records}</th><th /></tr></thead>
          <tbody>
            {recordings.map((r) => <tr key={r.id} className={r.id === selected.id ? "is-selected" : undefined} onClick={() => select(r.id)}>
              <td>
                <button type="button" className="demo-row-button" aria-current={r.id === selected.id ? "true" : undefined} onClick={(event) => { event.stopPropagation(); select(r.id); }}>{formatTranscriptTime(r.startedAt)}</button>
                <div className="demo-subtle">{r.durationMs === null ? t.badges.active : durationText(language, r.durationMs)} · {formatTranscriptBytes(bytesOf(r))}</div>
              </td>
              <td className="nx-right"><span className={`nx-badge ${r.status === "active" ? "nx-badge-green" : r.status === "aborted" ? "nx-badge-amber" : "nx-badge-blue"}`}><span className="nx-dot" />{t.badges[r.status]}</span></td>
            </tr>)}
          </tbody>
        </table>
      </div>
      <div className="demo-history-detail">
        <div className="demo-history-mobile-bar">
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setMobileList(true)}><IconArrowLeft size={13} />{t.back}</button>
          <span className="nx-hint">{formatTranscriptTime(selected.startedAt)}</span>
        </div>
        <div className="demo-history-tools">
          {mode === "text" ? <>
            <div className="demo-search">
              <IconSearch size={12} />
              <input className="nx-input" placeholder={t.searchPlaceholder} aria-label={t.searchLabel} value={query} spellCheck={false}
                onChange={(event) => setQuery(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); runSearch(); } }} />
            </div>
            <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={runSearch}><IconSearch size={12} />{t.search}</button>
          </> : <span className="nx-hint demo-grow">{t.replayHint}</span>}
          <div className="demo-mini-tabs" role="tablist" aria-label={t.viewLabel}>
            {(["text", "replay"] as const).map((view) => <button key={view} type="button" role="tab" aria-selected={mode === view} className={mode === view ? "is-active" : undefined} onClick={() => setMode(view)}>{t.views[view]}</button>)}
          </div>
        </div>
        {mode === "replay"
          ? <ReplayView key={selected.id} recording={selected} t={t} />
          : <TextView key={selected.id} recording={selected} needle={matches?.query ?? ""} emptyText={t.noMatch} matches={matches?.seqs ?? null} />}
      </div>
    </div>
  </div>;
}

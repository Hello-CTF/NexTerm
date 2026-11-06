import { useEffect, useMemo, useRef, useState } from "react";
import { REPLAY_SPEEDS, REPLAY_TICK_MS, TranscriptReplay } from "../../../src/features/terminal/transcriptReplay";
import { STATE_HINTS, STATE_LABELS, type BackgroundAttentionState } from "../../../src/features/terminal/backgroundStates";
import { IconActivity, IconArrowLeft, IconBell, IconCheck, IconGamepad, IconRefresh, IconTerminal, IconTrash } from "../../../src/ui/icons";
import { ConfirmDialog, pick, usePrefersReducedMotion, useToasts, type DemoProps } from "./common";
import { chunksOf, sinkOf, useReplayTerminal } from "./replayTerminal";

const copy = {
  zh: {
    title: "后台会话", host: "web-01", count: (n: number) => `${n} 个可恢复`, refresh: "刷新", refreshTitle: "列表会自动更新，也可以立即刷新",
    columns: ["名称", "状态", "大小", "时间"], actions: "操作",
    takeover: "接管", takeoverTitle: "回到该终端，并补上离开期间的输出", kill: "结束进程", killTitle: "结束该终端里运行的程序，无法恢复",
    labels: STATE_LABELS, hints: STATE_HINTS,
    needsMeNote: "「需要处理」表示程序正在等待回应：响铃、发出通知或显示了需要选择的提示",
    footnote: "持久终端由目标主机保持运行，退出应用或换一台设备后仍能找到。「接管」会回到原来的终端，并补上离开期间的输出，不会新开终端。",
    empty: "该主机上没有在后台运行的终端",
    tookOver: "已接管「web-01」的终端", back: "返回后台会话", connected: "已连接",
    killConfirm: (name: string) => `结束「web-01」上的终端「${name}」？\n\n终端里运行的程序会被终止，进程结束后无法恢复。只想离开时不必结束，它会在后台继续运行。`,
    killed: "已结束终端中的程序", cancel: "取消", ok: "确定",
    inbox: (n: number) => (n === 0 ? "收件箱：暂无需要关注的会话" : `收件箱：${n} 个会话需要关注`), inboxTitle: "需要关注的会话",
    justFinished: "命令刚结束",
  },
  en: {
    title: "Background sessions", host: "web-01", count: (n: number) => `${n} recoverable`, refresh: "Refresh", refreshTitle: "The list updates on its own; refresh now if you like",
    columns: ["Name", "State", "Size", "Time"], actions: "Actions",
    takeover: "Take over", takeoverTitle: "Return to this terminal and catch up on the output you missed", kill: "End process", killTitle: "End the program running in this terminal; this cannot be undone",
    labels: { running: "Running", needs_me: "Needs you", just_finished: "Just finished", idle: "Idle", exited: "Exited" } as Record<BackgroundAttentionState, string>,
    hints: { running: "The terminal has been producing output", needs_me: "The program is waiting for a response: a bell, a notification or a prompt", just_finished: "A command just finished", idle: "No new output and not waiting for a response", exited: "The program in the terminal has exited" } as Record<BackgroundAttentionState, string>,
    needsMeNote: "\"Needs you\" means a program is waiting for a response: a bell, a notification or a prompt that needs a choice",
    footnote: "The target host keeps persistent terminals running, so they are still here after you quit the app or switch devices. Take over returns to the same terminal with the output you missed; it does not open a new one.",
    empty: "No terminals are running in the background on this host",
    tookOver: "Took over the terminal on web-01", back: "Back to background sessions", connected: "Connected",
    killConfirm: (name: string) => `End terminal "${name}" on web-01?\n\nThe program running in it is terminated and cannot be recovered. To just step away, leave it running in the background.`,
    killed: "Ended the program in the terminal", cancel: "Cancel", ok: "OK",
    inbox: (n: number) => (n === 0 ? "Inbox: no sessions need attention" : `Inbox: ${n} sessions need attention`), inboxTitle: "Sessions that need attention",
    justFinished: "Command just finished",
  },
};

interface Session {
  id: string;
  name: string;
  state: BackgroundAttentionState;
  size: string;
  output: { zh: string; en: string };
  detached: { zh: string; en: string };
  missed: [number, string][];
}

const P = "\x1b[32mdeploy@web-01\x1b[0m:\x1b[34m~/apps/api-server\x1b[0m$ ";

const sessions: Session[] = [
  { id: "build", name: "npm run build", state: "running", size: "120×32", output: { zh: "3 秒前有输出", en: "output 3 s ago" }, detached: { zh: "18 分钟前转入后台", en: "in background 18 min" }, missed: [
    [0, `${P}npm run build\r\n`], [400, "\r\n> api@2.4.1 build\r\n> tsc -p . && vite build\r\n\r\n"], [1800, "vite v6.3.5 building for production...\r\n"],
    [3200, "transforming (812) src/routes/reports.ts\r\n"], [5200, "\x1b[32m✓\x1b[0m 1294 modules transformed.\r\n"], [6400, "rendering chunks (14)...\r\n"],
  ] },
  { id: "migrate", name: "db-migrate", state: "needs_me", size: "100×30", output: { zh: "1 分钟前有输出", en: "output 1 min ago" }, detached: { zh: "26 分钟前转入后台", en: "in background 26 min" }, missed: [
    [0, `${P}./scripts/db-migrate up\r\n`], [600, "Connected to postgres://app@127.0.0.1:5432/app\r\n"], [1500, "Pending migrations:\r\n  0042_add_report_index\r\n  0043_orders_status_enum\r\n  0044_drop_legacy_sessions\r\n"],
    [3000, "\r\n\x1b[33mApply 3 migrations to app? [y/N]\x1b[0m "],
  ] },
  { id: "backup", name: "backup.sh", state: "just_finished", size: "120×32", output: { zh: "2 分钟前有输出", en: "output 2 min ago" }, detached: { zh: "1 小时前转入后台", en: "in background 1 h" }, missed: [
    [0, `${P}./backup.sh /srv/data\r\n`], [900, "sending incremental file list\r\nuploads/2026/11/\r\n"], [2400, "sent 1.84G bytes  received 2.31K bytes  13.7M bytes/sec\r\n"],
    [3600, "snapshot data-20261106-1510 created\r\n\x1b[32mbackup finished in 2m14s\x1b[0m\r\n"], [4200, P],
  ] },
  { id: "logs", name: "tail -f error.log", state: "idle", size: "120×32", output: { zh: "42 分钟前有输出", en: "output 42 min ago" }, detached: { zh: "3 小时前转入后台", en: "in background 3 h" }, missed: [
    [0, `${P}tail -f /var/log/nginx/error.log\r\n`], [700, "2026/11/06 14:28:03 [warn] 812#812: *4411 upstream response is buffered to a temporary file\r\n"],
    [1600, "2026/11/06 14:29:41 [error] 812#812: *4502 upstream timed out (110: Connection timed out) while reading response header\r\n"],
  ] },
];

type Copy = (typeof copy)["zh"] | (typeof copy)["en"];

function StateBadge({ session, t }: { session: Session; t: Copy }) {
  if (session.state === "needs_me") return <span className="demo-attn demo-attn-needs" role="img" aria-label={t.labels.needs_me} title={t.labels.needs_me}>!</span>;
  if (session.state === "just_finished") return <span className="demo-attn demo-attn-done" role="img" aria-label={t.justFinished} title={t.justFinished}><IconCheck size={11} /></span>;
  if (session.state === "running") return <span className="demo-attn demo-attn-running" role="img" aria-label={t.labels.running} title={t.labels.running} />;
  return null;
}

// TakenOver replays the output the session produced while nobody watched it, quickly, then stays on the live screen.
function TakenOver({ session, t, onBack }: { session: Session; t: Copy; onBack: () => void }) {
  const reduced = usePrefersReducedMotion();
  const replay = useMemo(() => new TranscriptReplay(chunksOf(session.missed)), [session]);
  const containerRef = useRef<HTMLDivElement>(null);
  const terminalRef = useReplayTerminal(containerRef, replay, 0);
  useEffect(() => {
    const terminal = terminalRef.current;
    const end = replay.durationMs();
    if (!terminal) return;
    if (reduced) {
      replay.seek(sinkOf(terminal), end);
      return;
    }
    const speed = REPLAY_SPEEDS[REPLAY_SPEEDS.length - 1];
    let position = 0;
    const timer = window.setInterval(() => {
      position = replay.playStep(sinkOf(terminal), position + REPLAY_TICK_MS * speed);
      if (position >= end) window.clearInterval(timer);
    }, REPLAY_TICK_MS);
    return () => window.clearInterval(timer);
  }, [replay, reduced, terminalRef]);
  return <div className="nx-pane demo-takeover">
    <div className="nx-toolbar">
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={onBack}><IconArrowLeft size={13} />{t.back}</button>
      <span className="nx-toolbar-title">{session.name}</span>
      <span className="nx-badge nx-badge-green"><span className="nx-dot" />{t.connected}</span>
    </div>
    <div className="demo-xterm" ref={containerRef} />
  </div>;
}

export default function BackgroundDemo({ language }: DemoProps) {
  const t = pick(language, copy);
  const { push: pushToast, view: toastView } = useToasts(language);
  const [list, setList] = useState(sessions);
  const [busy, setBusy] = useState<string | null>(null);
  const [active, setActive] = useState<Session | null>(null);
  const [killing, setKilling] = useState<Session | null>(null);
  const [inboxOpen, setInboxOpen] = useState(false);
  const attention = list.filter((s) => s.state === "needs_me" || s.state === "just_finished");
  const needsMe = attention.some((s) => s.state === "needs_me");

  useEffect(() => {
    if (!busy) return;
    const timer = window.setTimeout(() => {
      const session = list.find((s) => s.id === busy);
      setBusy(null);
      if (!session) return;
      setList((current) => current.filter((s) => s.id !== session.id));
      setActive(session);
      pushToast("info", t.tookOver);
    }, 600);
    return () => window.clearTimeout(timer);
  }, [busy, list, t.tookOver, pushToast]);

  const takeOver = (session: Session) => {
    setInboxOpen(false);
    setBusy(session.id);
  };

  if (active) return <div className="demo-root">
    <TakenOver key={active.id} session={active} t={t} onBack={() => setActive(null)} />
    {toastView}
  </div>;

  return <div className="nx-pane demo-background">
    <div className="nx-toolbar">
      <IconActivity size={14} className="demo-muted" />
      <span className="nx-toolbar-title">{t.title}</span>
      <span className="nx-hint">{t.host}</span>
      <span className="nx-hint">{t.count(list.length)}</span>
      <span className="nx-spacer" />
      <div className="demo-inbox">
        <button type="button" className={`nx-icon-btn${inboxOpen ? " is-active" : ""}`} title={t.inbox(attention.length)} aria-label={t.inbox(attention.length)} aria-expanded={inboxOpen} aria-haspopup="dialog" onClick={() => setInboxOpen((open) => !open)}>
          <IconBell size={15} />
          {attention.length > 0 && <span className={`demo-inbox-count ${needsMe ? "is-needs" : "is-done"}`}>{attention.length}</span>}
        </button>
        {inboxOpen && <div className="demo-inbox-panel" role="dialog" aria-label={t.inboxTitle}>
          <div className="demo-inbox-title">{t.inboxTitle}</div>
          {attention.length === 0 && <div className="nx-hint">{t.inbox(0)}</div>}
          {attention.map((s) => <button key={s.id} type="button" className="demo-inbox-item" onClick={() => takeOver(s)}>
            <StateBadge session={s} t={t} />
            <span className="demo-inbox-name">{s.name}</span>
            <span className="nx-hint">{s.state === "needs_me" ? t.labels.needs_me : t.justFinished}</span>
          </button>)}
        </div>}
      </div>
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" title={t.refreshTitle}><IconRefresh size={13} />{t.refresh}</button>
    </div>
    <div className="demo-scroll">
      <table className="nx-table nx-table-fixed demo-bg-table">
        <colgroup><col /><col style={{ width: 96 }} /><col style={{ width: 76 }} /><col style={{ width: 150 }} /><col style={{ width: 196 }} /></colgroup>
        <thead><tr>{t.columns.map((c) => <th key={c} scope="col">{c}</th>)}<th scope="col"><span className="demo-sr">{t.actions}</span></th></tr></thead>
        <tbody>
          {list.length === 0 && <tr><td colSpan={5} className="demo-empty">{t.empty}</td></tr>}
          {list.map((s) => <tr key={s.id}>
            <td className="demo-bg-name" data-label={t.columns[0]}><span>{s.name}</span></td>
            <td className={`demo-bg-state${s.state === "needs_me" ? " is-accent" : ""}`} title={t.hints[s.state]} data-label={t.columns[1]}>
              <span className="demo-bg-state-inner"><StateBadge session={s} t={t} /><span>{t.labels[s.state]}</span></span>
            </td>
            <td className="nx-mono" data-label={t.columns[2]}>{s.size}</td>
            <td data-label={t.columns[3]}><span className="demo-bg-time">{pick(language, s.output)}</span><span className="demo-bg-detached">{pick(language, s.detached)}</span></td>
            <td className="demo-bg-actions">
              <button type="button" className="nx-btn nx-btn-primary nx-btn-xs" disabled={busy !== null} title={t.takeoverTitle} onClick={() => takeOver(s)}>
                {busy === s.id ? <IconRefresh size={11} className="demo-spin" /> : <IconGamepad size={11} />}{t.takeover}
              </button>
              <button type="button" className="nx-btn nx-btn-danger nx-btn-xs" disabled={busy !== null} title={t.killTitle} onClick={() => setKilling(s)}><IconTrash size={11} />{t.kill}</button>
            </td>
          </tr>)}
        </tbody>
      </table>
    </div>
    <div className="demo-bg-foot">
      {list.some((s) => s.state === "needs_me") && <p>{t.needsMeNote}</p>}
      <IconTerminal size={11} /> {t.footnote}
    </div>
    {killing && <ConfirmDialog title={t.kill} message={t.killConfirm(killing.name)} cancel={t.cancel} ok={t.ok} danger onClose={(confirmed) => {
      if (confirmed) {
        setList((current) => current.filter((s) => s.id !== killing.id));
        pushToast("success", t.killed);
      }
      setKilling(null);
    }} />}
    {toastView}
  </div>;
}

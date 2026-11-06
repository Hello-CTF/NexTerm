import { useEffect, useRef, useState } from "react";
import { reconnectDialing, reconnectingText, RETRY_DIALING_LABEL, RETRY_NOW_LABEL, type ReconnectSchedule } from "../../../src/app/shell/reconnectText";
import { IconArrowDown, IconArrowUp, IconChevronDown, IconChevronRight, IconCopy, IconList, IconLocate, IconRefresh, IconSearch, IconTrash } from "../../../src/ui/icons";
import { copyToClipboard, errorText, pick, useToasts, type DemoProps, type Language } from "./common";

const copy = {
  zh: {
    connected: "已连接", reconnecting: "重新连接中", status: "连接状态",
    simulate: "模拟断线", simulateTitle: "演示控制：断开示例连接",
    search: "搜索终端内容", prev: "上一条命令", next: "下一条命令", list: "命令列表：按命令查看、复制或定位输出",
    title: "命令列表", clear: "清空块记录（不影响终端内容）",
    expand: "展开输出预览", collapse: "收起输出预览", locate: "定位到此命令", copyBlock: "复制该块（命令 + 输出）",
    copied: (index: number) => `已复制块 #${index}`, copyFailed: (reason: string) => `复制失败：${reason}`,
    retryNow: RETRY_NOW_LABEL, dialing: RETRY_DIALING_LABEL, cleared: "暂无命令记录。",
  },
  en: {
    connected: "Connected", reconnecting: "Reconnecting", status: "Connection status",
    simulate: "Simulate drop", simulateTitle: "Demo control: drop the sample connection",
    search: "Search terminal output", prev: "Previous command", next: "Next command", list: "Command list: view, copy or locate output by command",
    title: "Command list", clear: "Clear block records (terminal content is kept)",
    expand: "Expand output preview", collapse: "Collapse output preview", locate: "Locate this command", copyBlock: "Copy this block (command + output)",
    copied: (index: number) => `Copied block #${index}`, copyFailed: (reason: string) => `Copy failed: ${reason}`,
    retryNow: "Retry now", dialing: "Connecting", cleared: "No commands recorded yet.",
  },
};

interface Block {
  command: string;
  startedAt: string;
  durationMs: number;
  exitCode: number;
  output: string[];
}

const blocks: Block[] = [
  { command: "systemctl status api-server", startedAt: "15:12:02", durationMs: 184, exitCode: 0, output: [
    "● api-server.service - API service",
    "     Loaded: loaded (/etc/systemd/system/api-server.service; enabled)",
    "     Active: active (running) since Fri 09:12:40 UTC; 6h ago",
    "   Main PID: 842 (api-server)",
    "     Memory: 84.2M",
  ] },
  { command: "docker ps --format 'table {{.Names}}\\t{{.Status}}'", startedAt: "15:12:09", durationMs: 412, exitCode: 0, output: [
    "NAMES         STATUS",
    "api-server    Up 6 hours (healthy)",
    "postgres      Up 6 hours",
    "redis         Up 6 hours",
  ] },
  { command: "curl -sf localhost:8080/readyz", startedAt: "15:12:21", durationMs: 1630, exitCode: 22, output: [
    "curl: (22) The requested URL returned error: 503",
  ] },
  { command: "docker logs --tail 4 api-server", startedAt: "15:12:30", durationMs: 96, exitCode: 0, output: [
    "15:12:24 WARN  readyz: worker queue backlog 412 > 400",
    "15:12:25 INFO  GET /healthz 200 7ms",
    "15:12:27 WARN  GET /api/reports 200 612ms",
    "15:12:29 INFO  worker queue=388 active=8",
  ] },
];

const PROMPT = "deploy@web-01:~/apps/api-server$";

// durationOf follows the command list: milliseconds below one second, otherwise seconds with one decimal.
function durationOf(ms: number): string {
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`;
}

type Link = "connected" | "reconnecting";

function reconnectingTextEn(session: ReconnectSchedule, now: number, retrying: boolean): string {
  const attempt = Math.max(1, session.attempt ?? 1);
  if (reconnectDialing(session, now, retrying)) return `Reconnecting (attempt ${attempt + 1} in progress…)`;
  const seconds = Math.max(0, Math.ceil((Number(session.nextRetryAt) - now) / 1000));
  return `Reconnecting (attempt ${attempt}, retrying in ${seconds} s)`;
}

function barText(language: Language, session: ReconnectSchedule, now: number, retrying: boolean): string {
  return language === "zh" ? reconnectingText(session, now, retrying) : reconnectingTextEn(session, now, retrying);
}

export default function TerminalDemo({ language }: DemoProps) {
  const t = pick(language, copy);
  const { push: pushToast, view: toastView } = useToasts(language);
  const screenRef = useRef<HTMLDivElement>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [expanded, setExpanded] = useState<number | null>(null);
  const [panel, setPanel] = useState(true);
  const [records, setRecords] = useState(true);
  const [link, setLink] = useState<Link>("connected");
  const [schedule, setSchedule] = useState<ReconnectSchedule>({ attempt: 1, nextRetryAt: 0 });
  const [retrying, setRetrying] = useState(false);
  const [now, setNow] = useState(() => Date.now());

  const reconnecting = link !== "connected";
  const dialing = reconnecting && reconnectDialing(schedule, now, retrying);

  useEffect(() => {
    if (!reconnecting) return;
    const timer = window.setInterval(() => setNow(Date.now()), 500);
    return () => window.clearInterval(timer);
  }, [reconnecting]);

  // A dialing attempt in the demo always succeeds after a short wait.
  useEffect(() => {
    if (!dialing) return;
    const timer = window.setTimeout(() => {
      setLink("connected");
      setRetrying(false);
    }, 1400);
    return () => window.clearTimeout(timer);
  }, [dialing]);

  const simulateDrop = () => {
    const start = Date.now();
    setNow(start);
    setSchedule({ attempt: 1, nextRetryAt: start + 5000 });
    setRetrying(false);
    setLink("reconnecting");
  };

  const locate = (index: number) => {
    setSelected(index);
    const screen = screenRef.current;
    const block = screen?.querySelector<HTMLElement>(`[data-command-block="${index}"]`);
    if (!screen || !block) return;
    screen.scrollTop = Math.max(0, block.offsetTop - screen.offsetTop - 8);
  };
  const step = (delta: number) => {
    const current = selected ?? (delta > 0 ? 0 : blocks.length + 1);
    const nextIndex = Math.min(blocks.length, Math.max(1, current + delta));
    locate(nextIndex);
  };
  const copyBlock = async (index: number) => {
    const block = blocks[index - 1];
    try {
      await copyToClipboard([`${PROMPT} ${block.command}`, ...block.output].join("\n"));
      pushToast("success", t.copied(index));
    } catch (error) {
      pushToast("error", t.copyFailed(errorText(error)));
    }
  };

  return <div className="nx-pane demo-terminal">
    {reconnecting && <div className="demo-connection-bar" role="status" data-state="reconnecting">
      <span className="demo-bar-dot" aria-hidden="true" />
      <span className="demo-bar-text">{barText(language, schedule, now, retrying)}</span>
      <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" disabled={dialing} onClick={() => setRetrying(true)}>
        <IconRefresh size={13} className={dialing ? "demo-spin" : undefined} />
        {dialing ? t.dialing : t.retryNow}
      </button>
    </div>}
    <div className="nx-toolbar">
      <span className="nx-toolbar-title">deploy@web-01</span>
      <span className={`nx-badge ${reconnecting ? "" : "nx-badge-green"}`} aria-label={`${t.status}: ${reconnecting ? t.reconnecting : t.connected}`}>
        <span className="nx-dot" />{reconnecting ? t.reconnecting : t.connected}
      </span>
      <span className="nx-spacer" />
      <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm demo-control" title={t.simulateTitle} disabled={reconnecting} onClick={simulateDrop}>{t.simulate}</button>
      <button type="button" className="nx-icon-btn" title={t.search} aria-label={t.search} disabled><IconSearch size={13} /></button>
      <button type="button" className="nx-icon-btn" title={t.prev} aria-label={t.prev} disabled={!records} onClick={() => step(-1)}><IconArrowUp size={13} /></button>
      <button type="button" className="nx-icon-btn" title={t.next} aria-label={t.next} disabled={!records} onClick={() => step(1)}><IconArrowDown size={13} /></button>
      <button type="button" className={`nx-icon-btn${panel ? " is-active" : ""}`} title={t.list} aria-label={t.list} aria-pressed={panel} onClick={() => setPanel((open) => !open)}><IconList size={13} /></button>
    </div>
    <div className="demo-term-body">
      <div className="demo-term-screen" ref={screenRef}>
        {blocks.map((block, i) => <div key={block.command} data-command-block={i + 1} className={`demo-term-block${selected === i + 1 ? " is-selected" : ""}`}>
          <div className="demo-prompt"><span>{PROMPT}</span> {block.command}</div>
          {block.output.map((line, n) => <div key={n} className="demo-term-line">{line}</div>)}
        </div>)}
        <div className="demo-prompt"><span>{PROMPT}</span> <span className="demo-cursor" /></div>
      </div>
      {panel && <aside className="demo-blocks" aria-label={t.title}>
        <div className="demo-blocks-head">
          <IconList size={13} />
          <span className="demo-blocks-title">{t.title}</span>
          <span className="nx-count">{records ? blocks.length : 0}</span>
          <span className="nx-spacer" />
          <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t.clear} aria-label={t.clear} disabled={!records} onClick={() => { setRecords(false); setExpanded(null); setSelected(null); }}><IconTrash size={12} /></button>
        </div>
        {!records ? <div className="nx-hint demo-blocks-empty">{t.cleared}</div> : <div className="demo-blocks-list">
          {blocks.map((block, i) => {
            const index = i + 1;
            const open = expanded === index;
            return <div key={block.command} className="demo-block-row">
              <div className="demo-block-main">
                <button type="button" className="demo-block-toggle" title={open ? t.collapse : t.expand} aria-label={open ? t.collapse : t.expand} aria-expanded={open} onClick={() => setExpanded(open ? null : index)}>
                  {open ? <IconChevronDown size={12} /> : <IconChevronRight size={12} />}
                </button>
                <span className="demo-block-index">#{index}</span>
                <div className="demo-block-text">
                  <div className="demo-block-command" title={block.command}>{block.command}</div>
                  <div className="demo-block-meta">{block.startedAt} · {durationOf(block.durationMs)}<span className={block.exitCode === 0 ? "is-ok" : "is-failed"}> · exit {block.exitCode}</span></div>
                </div>
                <span className="demo-block-actions">
                  <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t.locate} aria-label={`${t.locate} #${index}`} onClick={() => locate(index)}><IconLocate size={11} /></button>
                  <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t.copyBlock} aria-label={`${t.copyBlock} #${index}`} onClick={() => void copyBlock(index)}><IconCopy size={11} /></button>
                </span>
              </div>
              {open && <pre className="demo-block-preview">{block.output.join("\n")}</pre>}
            </div>;
          })}
        </div>}
      </aside>}
    </div>
    {toastView}
  </div>;
}

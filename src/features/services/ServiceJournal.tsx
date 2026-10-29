import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { SessionInfo } from "../../ipc/commands";
import { createJsonChannel, disposeChannel, type IpcChannel } from "../../ipc/events";
import {
  createLogTailRunId,
  logTailApi,
  type LogTailEvent,
  type LogTailSourceStatus,
} from "../../ipc/logTailApi";
import { describeError } from "../../ui/errorText";
import { IconChevronDown, IconClose } from "../../ui/icons";
import {
  compileLogFilters,
  filterMergedLines,
  highlightSegments,
  hostColor,
  LOG_STATUS_LABELS,
  MergedLogBuffer,
  regexErrorNotice,
} from "../logs/mergedLog";

const JOURNAL_CAPACITY = 5000;

interface ActiveJournal {
  id: string;
  channel: IpcChannel<unknown>;
  closed: boolean;
}

function isNarrowViewport(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia("(max-width: 560px)").matches;
}

const FOLLOW_SLACK_PX = 24;

export function ServiceJournal({
  session,
  host,
  unit,
  onClose,
}: {
  session: SessionInfo;
  host: string;
  unit: string;
  onClose: () => void;
}) {
  const [include, setInclude] = useState("");
  const [exclude, setExclude] = useState("");
  const [paused, setPaused] = useState(false);
  const [status, setStatus] = useState<LogTailSourceStatus | "idle">("idle");
  const [error, setError] = useState<string | null>(null);
  const [, setRevision] = useState(0);
  const [wrap, setWrap] = useState(isNarrowViewport);
  const [follow, setFollow] = useState(true);
  const logRef = useRef<HTMLDivElement | null>(null);
  const bufferRef = useRef(new MergedLogBuffer(JOURNAL_CAPACITY));
  const activeRef = useRef<ActiveJournal | null>(null);

  useEffect(() => {
    const id = createLogTailRunId();
    const run = {} as ActiveJournal;
    const finish = () => {
      if (run.closed) return;
      run.closed = true;
      disposeChannel(run.channel);
      if (activeRef.current === run) activeRef.current = null;
    };
    run.id = id;
    run.closed = false;
    run.channel = createJsonChannel((raw) => {
      const event = raw as unknown as LogTailEvent;
      if (run.closed || event.runId !== id) return;
      if (event.type === "status") {
        if (event.status) setStatus(event.status);
        setError(event.error || null);
      } else if (event.type === "line") {
        bufferRef.current.append({
          sequence: event.sequence,
          sourceId: "journal",
          sessionId: session.id,
          host,
          text: event.text ?? "",
          stderr: event.stderr === true,
          timeMs: event.timeMs,
        });
      } else if (event.type === "done") {
        finish();
      }
      setRevision((current) => current + 1);
    });
    activeRef.current = run;
    setStatus("starting");
    void logTailApi.start({
      runId: id,
      sources: [{ sourceId: "journal", sessionId: session.id, kind: "journal", value: unit }],
      maxConcurrent: 1,
    }, run.channel).catch((cause) => {
      if (run.closed) return;
      setStatus("failed");
      setError(describeError(cause));
      finish();
    });
    return () => {
      if (run.closed) return;
      run.closed = true;
      activeRef.current = null;
      void logTailApi.cancel(id).catch(() => undefined).finally(() => disposeChannel(run.channel));
    };
  }, [host, session.id, unit]);

  const filters = useMemo(() => compileLogFilters(include, exclude), [include, exclude]);
  const lines = filterMergedLines(bufferRef.current.visibleLines(), filters);

  useLayoutEffect(() => {
    const el = logRef.current;
    if (follow && el) el.scrollTop = el.scrollHeight;
  });

  return (
    <section className="flex min-h-[320px] flex-1 flex-col border-t border-neutral-800/70" aria-label={`服务 journal ${unit}`}>
      <div className="flex flex-wrap items-center gap-2 border-b border-neutral-800/70 px-3 py-2">
        <strong className="min-w-0 truncate text-[12px] text-neutral-200" title={unit}>journal：{unit}</strong>
        <span className={`text-[11px] ${status === "failed" ? "text-red-300" : "text-neutral-500"}`}>{LOG_STATUS_LABELS[status]}</span>
        <span className="nx-spacer" />
        <button type="button" className="nx-icon-btn nx-icon-btn-sm" aria-label="关闭 journal" title="关闭 journal" onClick={onClose}>
          <IconClose size={12} />
        </button>
      </div>
      <div className="flex flex-wrap items-center gap-2 border-b border-neutral-800/70 px-3 py-2">
        <input className="nx-input nx-input-sm min-w-0 flex-1 basis-40 font-mono sm:w-52 sm:flex-none" value={include} placeholder="只显示匹配的行（正则表达式）" aria-label="journal 包含正则" onChange={(event) => setInclude(event.target.value)} />
        <input className="nx-input nx-input-sm min-w-0 flex-1 basis-40 font-mono sm:w-44 sm:flex-none" value={exclude} placeholder="隐藏匹配的行（正则表达式）" aria-label="journal 排除正则" onChange={(event) => setExclude(event.target.value)} />
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" aria-pressed={paused} onClick={() => {
          const next = !paused;
          bufferRef.current.setPaused(next);
          setPaused(next);
          setRevision((current) => current + 1);
        }}>{paused ? "继续显示" : "暂停显示"}</button>
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" aria-pressed={wrap} onClick={() => setWrap((current) => !current)}>自动换行</button>
        <span className="nx-hint">保留 {bufferRef.current.retainedLines().length} / {JOURNAL_CAPACITY} · 丢弃 {bufferRef.current.droppedCount}</span>
      </div>
      {filters.error && <div role="alert" className="px-3 py-1 text-[11px] text-amber-300">{regexErrorNotice(filters.error)}</div>}
      {error && <div role="alert" className="px-3 py-1.5 text-[11px] text-red-300">{error}</div>}
      <div className="relative flex min-h-0 flex-1 flex-col">
        <div
          ref={logRef}
          role="log"
          aria-label="服务 journal 内容"
          data-wrap={wrap ? "true" : "false"}
          className="min-h-0 flex-1 overflow-auto overscroll-contain bg-term px-2 py-2 font-mono text-[12px] leading-[1.55]"
          onScroll={(event) => {
            const el = event.currentTarget;
            const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight <= FOLLOW_SLACK_PX;
            setFollow((current) => (current === atBottom ? current : atBottom));
          }}
        >
          {lines.length === 0 ? (
            <div className="px-2 py-4 text-center text-[11px] text-neutral-500">等待 journal 输出…</div>
          ) : lines.map((line) => (
            <div key={line.sequence} className={`flex gap-2 rounded px-1 ${wrap ? "min-w-0" : "min-w-max"}`}>
              <span className={`shrink-0 select-none ${hostColor(line.sessionId)}`}>[{line.host}]</span>
              <span className={`select-text ${wrap ? "min-w-0 whitespace-pre-wrap break-all" : "whitespace-pre"} ${line.stderr ? "text-rose-300" : "text-neutral-300"}`}>
                {highlightSegments(line.text, filters.highlight).map((segment, index) => segment.matched ? (
                  <mark key={index} className="rounded-[2px] bg-amber-300/40 px-0 text-inherit">{segment.text}</mark>
                ) : <span key={index}>{segment.text}</span>)}
              </span>
            </div>
          ))}
        </div>
        {!follow && lines.length > 0 && (
          <button type="button" className="nx-btn nx-btn-primary nx-btn-sm absolute right-3 bottom-3 min-h-9 shadow-lg" onClick={() => setFollow(true)}>
            <IconChevronDown size={13} />
            跳到最新
          </button>
        )}
      </div>
    </section>
  );
}

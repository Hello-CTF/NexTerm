import { Fragment, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { dockerApi, terminalApi } from "../../ipc/commands";
import { createBinaryChannel, disposeChannel } from "../../ipc/events";
import { describeError } from "../../ui/errorText";
import { IconClose, IconPlay, IconSearch } from "../../ui/icons";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { useUi } from "../../app/store";
import { createLogLineDecoder } from "./DockerLogStream";
import { MERGED_LOG_LINE_LIMIT, createMergedLogBuffer, type MergedLogLine } from "./mergedLogs";
import { t } from "../../i18n";

const PALETTE = [
  "text-sky-300",
  "text-green-300",
  "text-amber-300",
  "text-fuchsia-300",
  "text-orange-300",
  "text-cyan-300",
  "text-rose-300",
  "text-lime-300",
];

const TAIL = 200;

function clock(ts: number): string {
  const d = new Date(ts);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function highlight(text: string, needle: string): ReactNode {
  if (!needle) return text;
  const lower = text.toLowerCase();
  const key = needle.toLowerCase();
  const parts: ReactNode[] = [];
  let from = 0;
  for (let at = lower.indexOf(key); at >= 0; at = lower.indexOf(key, from)) {
    if (at > from) parts.push(text.slice(from, at));
    parts.push(<mark key={at} className="rounded-sm bg-yellow-400/40 text-inherit">{text.slice(at, at + key.length)}</mark>);
    from = at + key.length;
  }
  if (from === 0) return text;
  if (from < text.length) parts.push(text.slice(from));
  return <Fragment>{parts}</Fragment>;
}

export function DockerMergedLogs({
  sessionId,
  containers,
  onClose,
}: {
  sessionId: string;
  containers: { id: string; name: string }[];
  onClose: () => void;
}) {
  const buffer = useRef(createMergedLogBuffer());
  const [generation, setGeneration] = useState(0);
  const [wasDown, setWasDown] = useState(false);
  const [lines, setLines] = useState<MergedLogLine[]>([]);
  const [failed, setFailed] = useState<Record<number, string>>({});
  const [query, setQuery] = useState("");
  const [onlyMatches, setOnlyMatches] = useState(false);
  const [showTime, setShowTime] = useState(true);
  // Paused freezes the displayed snapshot while the buffer keeps filling.
  const [frozen, setFrozen] = useState<MergedLogLine[] | null>(null);
  const [newOutput, setNewOutput] = useState(false);
  const scroller = useRef<HTMLDivElement>(null);
  const followRef = useRef(true);
  const lastHeightRef = useRef(0);
  const modalRef = useRef<HTMLDivElement>(null);
  const layer = useOverlayFocus(true, modalRef);
  const sessionStatus = useUi((state) => state.sessions.find((entry) => entry.id === sessionId)?.status);
  const key = containers.map((c) => c.id).join("\n");
  const down = sessionStatus !== undefined && sessionStatus !== "connected";

  useEffect(() => {
    if (down) setWasDown(true);
  }, [down]);

  const refollow = () => {
    buffer.current = createMergedLogBuffer();
    setLines([]);
    setFrozen(null);
    setFailed({});
    setNewOutput(false);
    setWasDown(false);
    setGeneration((value) => value + 1);
  };

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.repeat || isImeKeyEvent(event) || !layer.isTopmost()) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      onClose();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [layer, onClose]);

  useEffect(() => {
    let cancelled = false;
    let timer: number | undefined;
    const handles: { id: string; channel: unknown }[] = [];
    const schedule = () => {
      if (timer !== undefined) return;
      timer = window.setTimeout(() => {
        timer = undefined;
        if (!cancelled) setLines(buffer.current.snapshot());
      }, 50);
    };
    const release = (id: string, channel: unknown) => {
      void terminalApi.closeTab(id, "kill", sessionId).catch(() => undefined);
      disposeChannel(channel as never);
    };
    containers.forEach((container, index) => {
      const decoder = createLogLineDecoder();
      const channel = createBinaryChannel((bytes) => {
        if (cancelled) return;
        const next = decoder.write(bytes);
        if (next.length) {
          buffer.current.push(index, next);
          schedule();
        }
      });
      dockerApi.logsAttach(sessionId, container.id, TAIL, channel, { timestamps: true }).then(
        (id) => {
          if (cancelled) release(id, channel);
          else handles.push({ id, channel });
        },
        (cause) => {
          disposeChannel(channel);
          if (!cancelled) setFailed((prev) => ({ ...prev, [index]: describeError(cause) }));
        },
      );
    });
    return () => {
      cancelled = true;
      if (timer !== undefined) window.clearTimeout(timer);
      for (const handle of handles) release(handle.id, handle.channel);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, key, generation]);

  const needle = query.trim();
  const visible = useMemo(() => {
    const base = frozen ?? lines;
    if (!onlyMatches || !needle) return base;
    const lower = needle.toLowerCase();
    return base.filter((line) => line.text.toLowerCase().includes(lower));
  }, [frozen, lines, onlyMatches, needle]);

  useEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const onScroll = () => {
      const near = el.scrollHeight - el.scrollTop - el.clientHeight <= 32;
      followRef.current = near;
      if (near) setNewOutput(false);
      lastHeightRef.current = el.scrollHeight;
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, []);

  useEffect(() => {
    const el = scroller.current;
    if (!el) return;
    if (followRef.current) el.scrollTop = el.scrollHeight;
    else if (el.scrollHeight > lastHeightRef.current) setNewOutput(true);
    lastHeightRef.current = el.scrollHeight;
  }, [visible]);

  const jumpToLatest = () => {
    const el = scroller.current;
    followRef.current = true;
    setNewOutput(false);
    if (el) {
      el.scrollTop = el.scrollHeight;
      lastHeightRef.current = el.scrollHeight;
    }
  };

  return createPortal(
    <div className="nx-overlay" onClick={onClose}>
      <div
        ref={modalRef}
        className="nx-modal flex h-[86dvh] w-[94vw] max-w-[1200px] flex-col"
        role="dialog"
        aria-modal="true"
        aria-label={t("合并日志")}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={(event) => {
          event.stopPropagation();
          if (layer.isTopmost()) trapOverlayTab(event, modalRef.current);
        }}
      >
        <div className="nx-modal-header">
          <span className="min-w-0 truncate">{t("合并日志 · {length} 个容器 · 仅保留最近 {MERGED_LOG_LINE_LIMIT} 行", { length: containers.length, MERGED_LOG_LINE_LIMIT })}</span>
          <div className="nx-spacer" />
          <button type="button" className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-9 pointer-coarse:w-9" aria-label={t("关闭合并日志")} onClick={onClose}>
            <IconClose size={12} />
          </button>
        </div>
        <div className="flex shrink-0 flex-wrap gap-x-3 gap-y-1 border-b border-neutral-800/70 px-3 py-1.5 text-[11px]">
          {containers.map((container, index) => (
            <span key={container.id} className={`font-mono ${PALETTE[index % PALETTE.length]}`} title={failed[index]}>
              {container.name}{failed[index] !== undefined && t("（附加失败）")}
            </span>
          ))}
        </div>
        {(down || wasDown) && (
          <div role="alert" className="flex shrink-0 items-center gap-2 border-b border-amber-900/60 bg-amber-950/30 px-3 py-1.5 text-xs text-amber-300">
            <span className="min-w-0 flex-1">{down ? t("主机连接中断，日志跟随已停止") : t("连接已恢复，日志跟随已停止")}</span>
            {!down && (
              <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm pointer-coarse:min-h-9" onClick={refollow}>
                {t("重新跟随")}
              </button>
            )}
          </div>
        )}
        <div className="nx-docker-log-bar flex shrink-0 flex-wrap items-center gap-1.5 border-b border-neutral-800/70 bg-neutral-950/60 px-2 py-1">
          <div className="nx-field min-w-0 flex-1">
            <span className="nx-field-icon"><IconSearch size={12} /></span>
            <input
              className="nx-input nx-input-sm min-w-0 font-mono"
              type="search"
              placeholder={t("高亮关键字")}
              aria-label={t("高亮关键字")}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          </div>
          <label className="nx-hint inline-flex items-center gap-1">
            <input type="checkbox" checked={onlyMatches} onChange={(e) => setOnlyMatches(e.target.checked)} />
            {t("只显示匹配行")}
          </label>
          <label className="nx-hint inline-flex items-center gap-1">
            <input type="checkbox" checked={showTime} onChange={(e) => setShowTime(e.target.checked)} />
            {t("显示时间")}
          </label>
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm pointer-coarse:min-h-9"
            aria-pressed={frozen !== null}
            onClick={() => setFrozen((cur) => (cur === null ? buffer.current.snapshot() : null))}
          >
            {frozen !== null && <IconPlay size={11} />}
            {frozen !== null ? t("继续") : t("暂停")}
          </button>
        </div>
        <div className="relative min-h-0 flex-1">
          <div
            ref={scroller}
            role="log"
            aria-label={t("合并日志内容")}
            className="h-full overflow-auto overscroll-contain bg-term px-3 py-2 font-mono text-[11.5px] leading-relaxed text-neutral-300 [overflow-wrap:anywhere]"
          >
            {visible.length === 0 ? (
              <span className="text-neutral-500">{lines.length === 0 ? t("等待日志…") : t("没有匹配的日志行")}</span>
            ) : (
              visible.map((line) => (
                <div key={line.seq} className="whitespace-pre-wrap">
                  <span className={PALETTE[line.source % PALETTE.length]}>[{containers[line.source]?.name ?? line.source}]</span>{" "}
                  {showTime && Number.isFinite(line.ts) && <span className="text-neutral-500">{clock(line.ts)} </span>}
                  {highlight(line.text, needle)}
                </div>
              ))
            )}
          </div>
          {newOutput && (
            <button
              type="button"
              className="absolute bottom-3 left-1/2 z-10 -translate-x-1/2 rounded-full border border-neutral-700 bg-neutral-800/95 px-3.5 py-1 text-[11px] text-neutral-200 shadow-lg hover:bg-neutral-700 pointer-coarse:min-h-10"
              aria-label={t("回到最新日志")}
              onClick={jumpToLatest}
            >
              {t("↓ 新输出")}
            </button>
          )}
        </div>
      </div>
    </div>,
    document.body,
  );
}

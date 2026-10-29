import { useEffect, useMemo, useRef, useState } from "react";
import { copyText } from "../../ui/clipboard";
import { IconCopy, IconPlay, IconSearch } from "../../ui/icons";

export interface DockerBinarySink {
  onBytes: ((bytes: Uint8Array) => void) | null;
}

export function createLogLineDecoder() {
  const decoder = new TextDecoder();
  let tail = "";
  return {
    write(bytes: Uint8Array): string[] {
      const lines = (tail + decoder.decode(bytes, { stream: true })).split("\n");
      tail = lines.pop() ?? "";
      return lines;
    },
    close(): string[] {
      const rest = tail + decoder.decode();
      tail = "";
      return rest ? [rest] : [];
    },
  };
}

export function DockerLogStream({ sink, ariaLabel = "容器日志" }: { sink: DockerBinarySink; ariaLabel?: string }) {
  const [lines, setLines] = useState<string[]>([]);
  const [newOutput, setNewOutput] = useState(false);
  const scroller = useRef<HTMLDivElement>(null);
  const followRef = useRef(true);
  const lastHeightRef = useRef(0);
  const [query, setQuery] = useState("");
  // Paused keeps the visible lines frozen while the stream keeps buffering.
  const [frozen, setFrozen] = useState<number | null>(null);
  const [copied, setCopied] = useState(false);
  const visibleLines = useMemo(() => {
    const base = frozen === null ? lines : lines.slice(0, frozen);
    const needle = query.trim().toLowerCase();
    return needle ? base.filter((line) => line.toLowerCase().includes(needle)) : base;
  }, [lines, frozen, query]);
  const copyVisible = () => {
    void copyText(visibleLines.join("\n")).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    }).catch(() => undefined);
  };

  useEffect(() => {
    const decoder = createLogLineDecoder();
    const append = (next: string[]) => {
      if (next.length) setLines((prev) => [...prev, ...next].slice(-4000));
    };
    sink.onBytes = (bytes) => append(decoder.write(bytes));
    return () => {
      sink.onBytes = null;
      append(decoder.close());
    };
  }, [sink]);

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
    if (followRef.current) {
      el.scrollTop = el.scrollHeight;
    } else if (el.scrollHeight > lastHeightRef.current) {
      setNewOutput(true);
    }
    lastHeightRef.current = el.scrollHeight;
  }, [visibleLines]);

  const jumpToLatest = () => {
    const el = scroller.current;
    followRef.current = true;
    setNewOutput(false);
    if (el) {
      el.scrollTop = el.scrollHeight;
      lastHeightRef.current = el.scrollHeight;
    }
  };

  return (
    <div className="relative flex h-full min-h-0 flex-col">
      <div className="nx-docker-log-bar flex shrink-0 items-center gap-1.5 border-b border-neutral-800/70 bg-neutral-950/60 px-2 py-1">
        <div className="nx-field min-w-0 flex-1">
          <span className="nx-field-icon"><IconSearch size={12} /></span>
          <input
            className="nx-input nx-input-sm min-w-0 font-mono"
            type="search"
            enterKeyHint="search"
            placeholder="过滤日志"
            aria-label="过滤日志"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm pointer-coarse:min-h-9"
          aria-pressed={frozen !== null}
          onClick={() => setFrozen((cur) => (cur === null ? lines.length : null))}
        >
          {frozen !== null && <IconPlay size={11} />}
          {frozen !== null ? "继续" : "暂停"}
        </button>
        <button
          type="button"
          className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-9 pointer-coarse:w-9"
          title="复制当前显示的日志"
          aria-label="复制日志"
          disabled={visibleLines.length === 0}
          onClick={copyVisible}
        >
          {copied ? <span aria-hidden="true" className="text-[11px]">✓</span> : <IconCopy size={13} />}
        </button>
      </div>
      <div
        ref={scroller}
        role="log"
        aria-label={ariaLabel}
        className="min-h-0 flex-1 overflow-auto overscroll-contain bg-term px-3 py-2 font-mono text-[11.5px] leading-relaxed text-neutral-300 [overflow-wrap:anywhere] [padding-bottom:calc(0.5rem+var(--nx-safe-bottom,0px))]"
      >
        {lines.length === 0 ? (
          <span className="text-neutral-500">等待日志…</span>
        ) : visibleLines.length === 0 ? (
          <span className="text-neutral-500">没有匹配的日志行</span>
        ) : (
          visibleLines.map((line, index) => (
            <div key={index} className="whitespace-pre-wrap">
              {highlightLog(line)}
            </div>
          ))
        )}
      </div>
      {newOutput && (
        <button
          type="button"
          className="absolute bottom-3 left-1/2 z-10 -translate-x-1/2 rounded-full border border-neutral-700 bg-neutral-800/95 px-3.5 py-1 text-[11px] pointer-coarse:min-h-10 pointer-coarse:px-4 pointer-coarse:text-xs text-neutral-200 shadow-lg hover:bg-neutral-700"
          aria-label="回到最新日志"
          onClick={jumpToLatest}
        >
          ↓ 新输出
        </button>
      )}
    </div>
  );
}

function highlightLog(line: string) {
  if (/\b(ERROR|FATAL|panic)\b/i.test(line)) return <span className="text-red-300">{line}</span>;
  if (/\b(WARN|Warning)\b/i.test(line)) return <span className="text-amber-300">{line}</span>;
  if (/^\s*#/.test(line)) return <span className="text-neutral-500">{line}</span>;
  return line;
}

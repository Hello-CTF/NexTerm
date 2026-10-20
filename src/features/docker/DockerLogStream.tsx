import { useEffect, useRef, useState } from "react";

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
  }, [lines]);

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
    <div className="relative h-full min-h-0">
      <div
        ref={scroller}
        role="log"
        aria-label={ariaLabel}
        className="h-full overflow-auto bg-term px-3 py-2 font-mono text-[11.5px] leading-relaxed text-neutral-300"
      >
        {lines.length === 0 ? (
          <span className="text-neutral-500">等待日志…</span>
        ) : (
          lines.map((line, index) => (
            <div key={index} className="whitespace-pre-wrap">
              {highlightLog(line)}
            </div>
          ))
        )}
      </div>
      {newOutput && (
        <button
          type="button"
          className="absolute bottom-2 left-1/2 z-10 -translate-x-1/2 rounded-full border border-neutral-700 bg-neutral-800/95 px-3 py-1 text-[11px] text-neutral-200 shadow-lg hover:bg-neutral-700"
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

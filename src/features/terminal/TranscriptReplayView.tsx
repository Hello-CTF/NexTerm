import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";

import { transcriptApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { Select } from "../../ui/Select";
import { IconAlert, IconPlay } from "../../ui/icons";
import { KeyframeBuilder } from "./transcriptKeyframes";
import { loadCachedKeyframes, saveCachedKeyframes, transcriptFingerprint } from "./transcriptKeyframeCache";
import {
  formatReplayClock,
  toReplayChunk,
  TranscriptReplay,
  type ReplaySink,
} from "./transcriptReplay";

const TICK_MS = 50;
// Small pages keep each JSON parse short; a 4 MiB page is a 5+ MB payload that blocks the UI thread for a noticeable time.
const READ_PAGE_BYTES = 1024 * 1024;
const SPEEDS = [1, 2, 5, 10];
// A seek that lands beyond the indexed range replays from the start, so dragging waits for a pause before starting one.
const UNINDEXED_SEEK_DEBOUNCE_MS = 250;
// Short seeks finish before this and never flash the busy overlay.
const SEEK_BUSY_DELAY_MS = 150;
// The recorded window can be larger than the viewer; the screen shrinks to fit but never below this readable floor.
const MIN_FIT_SCALE = 0.3;

interface FitState {
  scale: number;
  width: number;
  height: number;
}

const NO_FIT: FitState = { scale: 1, width: 0, height: 0 };

// Phone-width viewports (<= 560px) use a smaller font so a recorded 80-column screen needs less horizontal scrolling.
const NARROW_VIEWPORT_PX = 560;

const REPLAY_THEME = {
  background: "#101217",
  foreground: "#c6cbd6",
  cursor: "#101217",
  selectionBackground: "#2c3e5d",
};

function terminalSink(terminal: Terminal): ReplaySink {
  return {
    reset: () => terminal.reset(),
    write: (data, callback) => terminal.write(data, callback),
    resize: (cols, rows) => terminal.resize(cols, rows),
    restore: (frame, callback) => {
      terminal.resize(frame.cols, frame.rows);
      terminal.write(frame.state, callback);
    },
  };
}

export function TranscriptReplayView({
  transcriptId,
  contentOmitted = false,
}: {
  transcriptId: string;
  contentOmitted?: boolean;
}) {
  const [loadPhase, setLoadPhase] = useState<"loading" | "ready">("loading");
  const [hasChunks, setHasChunks] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState(1);
  const [position, setPosition] = useState(0);
  const [duration, setDuration] = useState(0);
  const [indexPercent, setIndexPercent] = useState<number | null>(null);
  const [seekBusy, setSeekBusy] = useState(false);

  const containerRef = useRef<HTMLDivElement | null>(null);
  const viewportRef = useRef<HTMLDivElement | null>(null);
  const [fit, setFit] = useState<FitState>(NO_FIT);
  const terminalRef = useRef<Terminal | null>(null);
  const engineRef = useRef<TranscriptReplay | null>(null);
  const positionRef = useRef(0);
  const speedRef = useRef(1);
  const seekTimerRef = useRef<number | null>(null);
  const busyTimerRef = useRef<number | null>(null);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const terminal = new Terminal({
      disableStdin: true,
      scrollback: 5000,
      fontSize: window.innerWidth <= NARROW_VIEWPORT_PX ? 11 : 12,
      theme: REPLAY_THEME,
    });
    terminal.open(container);
    terminalRef.current = terminal;
    return () => {
      terminal.dispose();
      terminalRef.current = null;
    };
  }, []);

  // The terminal keeps the recorded cols x rows, so a recording from a larger window overflows a small viewer. Scale
  // the whole screen down to the viewport instead of scrolling; a smaller recording stays at its natural size.
  const refit = useCallback(() => {
    const viewport = viewportRef.current;
    const container = containerRef.current;
    if (!viewport || !container) return;
    const width = container.offsetWidth;
    const height = container.offsetHeight;
    if (!width || !height || !viewport.clientWidth || !viewport.clientHeight) {
      setFit((current) => (current === NO_FIT ? current : NO_FIT));
      return;
    }
    const scale = Math.max(
      MIN_FIT_SCALE,
      Math.min(1, viewport.clientWidth / width, viewport.clientHeight / height),
    );
    setFit((current) =>
      current.scale === scale && current.width === width && current.height === height
        ? current
        : { scale, width, height },
    );
  }, []);

  useEffect(() => {
    const viewport = viewportRef.current;
    const container = containerRef.current;
    if (!viewport || !container || typeof ResizeObserver === "undefined") return;
    // The container's own size follows the recorded window size; the viewport follows the layout.
    const observer = new ResizeObserver(() => refit());
    observer.observe(viewport);
    observer.observe(container);
    return () => observer.disconnect();
  }, [refit]);

  const clearBusy = useCallback(() => {
    if (busyTimerRef.current !== null) window.clearTimeout(busyTimerRef.current);
    busyTimerRef.current = null;
    setSeekBusy(false);
  }, []);

  useEffect(() => {
    let cancelled = false;
    setLoadPhase("loading");
    setHasChunks(false);
    setLoadError(null);
    setPlaying(false);
    setPosition(0);
    setDuration(0);
    setIndexPercent(null);
    positionRef.current = 0;
    const engine = new TranscriptReplay([]);
    engineRef.current = engine;
    engine.onSettled = clearBusy;
    const builder = new KeyframeBuilder({
      onFrames: (frames) => {
        engine.addKeyframes(frames);
        const total = engine.chunkCount();
        if (total > 0) setIndexPercent(Math.min(99, Math.floor((builder.processedChunks() / total) * 100)));
      },
    });
    if (contentOmitted) {
      builder.dispose();
      setLoadPhase("ready");
      return () => {
        if (engineRef.current === engine) engineRef.current = null;
        engine.dispose();
      };
    }
    void (async () => {
      try {
        let afterSeq = 0;
        let lastSeq = 0;
        for (;;) {
          const result = await transcriptApi.read(transcriptId, afterSeq, READ_PAGE_BYTES);
          if (cancelled) return;
          const page = result.chunks.map(toReplayChunk);
          if (page.length > 0) {
            const first = engine.chunkCount() === 0;
            engine.append(page);
            builder.feed(page);
            lastSeq = page[page.length - 1].seq;
            setDuration(engine.durationMs());
            if (first) {
              setHasChunks(true);
              const terminal = terminalRef.current;
              if (terminal) positionRef.current = engine.seek(terminalSink(terminal), 0);
              setPosition(positionRef.current);
            }
          }
          if (result.done || result.chunks.length === 0) break;
          afterSeq = result.nextSeq;
          // Let input and rendering run between pages.
          await new Promise<void>((resolve) => setTimeout(resolve, 0));
        }
        setLoadPhase("ready");
        const count = engine.chunkCount();
        if (count === 0) {
          builder.dispose();
          return;
        }
        const fingerprint = transcriptFingerprint({
          chunkCount: count,
          lastSeq,
          firstTs: engine.firstTs(),
          lastTs: engine.firstTs() + engine.durationMs(),
        });
        const cached = await loadCachedKeyframes(transcriptId, fingerprint);
        if (cancelled) return;
        if (cached) {
          builder.dispose();
          engine.addKeyframes(cached);
          setIndexPercent(null);
          return;
        }
        setIndexPercent(Math.floor((builder.processedChunks() / count) * 100));
        builder.finish();
        await builder.whenIdle();
        if (cancelled) return;
        setIndexPercent(null);
        void saveCachedKeyframes(transcriptId, fingerprint, engine.keyframeList());
      } catch (error) {
        if (!cancelled) setLoadError(describeError(error));
      }
    })();
    return () => {
      cancelled = true;
      builder.dispose();
      if (engineRef.current === engine) engineRef.current = null;
      engine.dispose();
      if (seekTimerRef.current !== null) window.clearTimeout(seekTimerRef.current);
      seekTimerRef.current = null;
      clearBusy();
    };
  }, [transcriptId, contentOmitted, clearBusy]);

  useEffect(() => {
    if (!playing) return;
    let previous = performance.now();
    const timer = window.setInterval(() => {
      const engine = engineRef.current;
      const terminal = terminalRef.current;
      if (!engine || !terminal) return;
      const now = performance.now();
      const elapsed = now - previous;
      previous = now;
      const next = engine.playStep(
        terminalSink(terminal),
        positionRef.current + elapsed * speedRef.current,
      );
      positionRef.current = next;
      setPosition(next);
      if (next >= engine.durationMs() && loadPhase === "ready") {
        setPlaying(false);
      }
    }, TICK_MS);
    return () => window.clearInterval(timer);
  }, [playing, loadPhase]);

  const commitSeek = useCallback((value: number) => {
    const engine = engineRef.current;
    const terminal = terminalRef.current;
    if (!engine || !terminal) return;
    if (busyTimerRef.current === null) {
      busyTimerRef.current = window.setTimeout(() => setSeekBusy(true), SEEK_BUSY_DELAY_MS);
    }
    positionRef.current = engine.seek(terminalSink(terminal), value);
    setPosition(positionRef.current);
  }, []);

  // Dragging only moves the clock while the target needs a replay from the start; a seek inside the indexed range
  // restores the nearest keyframe and replays at most a minute, so it runs straight away.
  const seekTo = useCallback(
    (value: number) => {
      const engine = engineRef.current;
      if (!engine) return;
      if (seekTimerRef.current !== null) window.clearTimeout(seekTimerRef.current);
      seekTimerRef.current = null;
      if (value <= engine.indexedThroughMs() || value === 0) {
        commitSeek(value);
        return;
      }
      positionRef.current = engine.clampPosition(value);
      setPosition(positionRef.current);
      seekTimerRef.current = window.setTimeout(() => {
        seekTimerRef.current = null;
        commitSeek(positionRef.current);
      }, UNINDEXED_SEEK_DEBOUNCE_MS);
    },
    [commitSeek],
  );

  const togglePlay = () => {
    const engine = engineRef.current;
    if (!engine || engine.durationMs() <= 0) return;
    if (!playing && positionRef.current >= engine.durationMs()) {
      commitSeek(0);
    }
    setPlaying(!playing);
  };

  const changeSpeed = (value: number) => {
    speedRef.current = value;
    setSpeed(value);
  };

  const interactive = hasChunks || loadPhase === "ready";
  const emptyRecording = loadPhase === "ready" && !hasChunks;
  const status =
    loadPhase === "loading" && hasChunks
      ? "加载中…"
      : indexPercent !== null
        ? `正在建立快速跳转索引 ${indexPercent}%`
        : null;

  return (
    <div className="flex min-h-0 flex-1 flex-col" data-testid="transcript-replay">
      <div className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-neutral-800/60 px-3 py-2">
        <button
          className="nx-btn nx-btn-ghost nx-btn-sm pointer-coarse:min-h-11 pointer-coarse:px-4"
          disabled={!interactive || loadError !== null || duration <= 0}
          onClick={togglePlay}
          aria-label={playing ? "暂停回放" : "开始回放"}
        >
          {playing ? null : <IconPlay size={12} />}
          {playing ? "暂停" : "播放"}
        </button>
        <Select
          className="w-[76px] shrink-0"
          triggerClassName="nx-input nx-select-trigger nx-select-ghost"
          ariaLabel="回放倍速"
          value={String(speed)}
          disabled={!interactive || loadError !== null}
          onChange={(value) => changeSpeed(Number(value))}
          options={SPEEDS.map((value) => ({ value: String(value), label: `${value}x` }))}
        />
        <input
          type="range"
          // The thumb owns horizontal drags; vertical pans still scroll the page. On phones the slider gets its own full-width row.
          className="h-6 min-w-[120px] flex-1 touch-pan-y pointer-coarse:h-11 max-[560px]:order-last max-[560px]:basis-full"
          data-testid="replay-seek"
          aria-label="回放进度"
          min={0}
          max={Math.max(1, duration)}
          step={100}
          value={Math.min(position, Math.max(1, duration))}
          disabled={!interactive || loadError !== null || duration <= 0}
          onChange={(event) => seekTo(Number(event.target.value))}
        />
        <span className="shrink-0 whitespace-nowrap font-mono text-[11px] tabular-nums text-neutral-500 max-[560px]:ml-auto" data-testid="replay-clock">
          {formatReplayClock(position)} / {formatReplayClock(duration)}
          {loadPhase === "loading" && hasChunks ? "+" : ""}
        </span>
        {status && (
          <span className="shrink-0 whitespace-nowrap text-[11px] text-neutral-500" data-testid="replay-status" role="status">
            {status}
          </span>
        )}
      </div>
      <div
        ref={viewportRef}
        className="relative min-h-0 flex-1 overflow-auto overscroll-contain"
        style={{ background: REPLAY_THEME.background }}
      >
        <div
          style={fit.width ? { width: fit.width * fit.scale, height: fit.height * fit.scale } : undefined}
          data-testid="replay-fit"
        >
          <div
            ref={containerRef}
            className="w-max px-2 py-1"
            style={fit.scale < 1 ? { transform: `scale(${fit.scale})`, transformOrigin: "0 0" } : undefined}
          />
        </div>
        {!interactive && !loadError && (
          <div className="absolute inset-0 flex items-center justify-center bg-neutral-900/80 text-[12px] text-neutral-500">
            加载回放数据…
          </div>
        )}
        {seekBusy && !loadError && (
          <div
            className="absolute inset-0 flex items-center justify-center bg-neutral-900/60 text-[12px] text-neutral-400"
            data-testid="replay-seek-busy"
          >
            跳转中…
          </div>
        )}
        {loadError && (
          <div className="absolute inset-0 flex items-center justify-center gap-2 bg-neutral-900/80 text-[12px] text-red-300">
            <IconAlert size={13} />
            未能加载回放数据：{loadError}
          </div>
        )}
        {emptyRecording && !loadError && (
          <div className="absolute inset-0 flex items-center justify-center bg-neutral-900/80 text-[12px] text-neutral-500">
            {contentOmitted ? "仅保留元数据" : "该记录没有任何可回放的内容"}
          </div>
        )}
      </div>
    </div>
  );
}

import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";

import { transcriptApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { IconAlert, IconPlay } from "../../ui/icons";
import {
  formatReplayClock,
  toReplayChunk,
  TranscriptReplay,
  type ReplayChunk,
  type ReplaySink,
} from "./transcriptReplay";

const TICK_MS = 50;
const READ_PAGE_BYTES = 4 * 1024 * 1024;
const SPEEDS = [0.5, 1, 2, 4];

const REPLAY_THEME = {
  background: "#101217",
  foreground: "#c6cbd6",
  cursor: "#101217",
  selectionBackground: "#2c3e5d",
};

function terminalSink(terminal: Terminal): ReplaySink {
  return {
    reset: () => terminal.reset(),
    write: (data) => terminal.write(data),
    resize: (cols, rows) => terminal.resize(cols, rows),
  };
}

async function loadAllChunks(transcriptId: string): Promise<ReplayChunk[]> {
  const chunks: ReplayChunk[] = [];
  let afterSeq = 0;
  for (;;) {
    const result = await transcriptApi.read(transcriptId, afterSeq, READ_PAGE_BYTES);
    for (const chunk of result.chunks) {
      chunks.push(toReplayChunk(chunk));
    }
    if (result.done || result.chunks.length === 0) break;
    afterSeq = result.nextSeq;
  }
  return chunks;
}

export function TranscriptReplayView({ transcriptId }: { transcriptId: string }) {
  const [chunks, setChunks] = useState<ReplayChunk[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState(1);
  const [position, setPosition] = useState(0);
  const [duration, setDuration] = useState(0);

  const containerRef = useRef<HTMLDivElement | null>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const engineRef = useRef<TranscriptReplay | null>(null);
  const positionRef = useRef(0);
  const speedRef = useRef(1);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const terminal = new Terminal({
      disableStdin: true,
      scrollback: 5000,
      fontSize: 12,
      theme: REPLAY_THEME,
    });
    terminal.open(container);
    terminalRef.current = terminal;
    return () => {
      terminal.dispose();
      terminalRef.current = null;
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    setChunks(null);
    setLoadError(null);
    setPlaying(false);
    setPosition(0);
    positionRef.current = 0;
    void (async () => {
      try {
        const loaded = await loadAllChunks(transcriptId);
        if (!cancelled) setChunks(loaded);
      } catch (error) {
        if (!cancelled) setLoadError(describeError(error));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [transcriptId]);

  useEffect(() => {
    if (!chunks) return;
    const engine = new TranscriptReplay(chunks);
    engineRef.current = engine;
    setDuration(engine.durationMs());
    const terminal = terminalRef.current;
    if (terminal) {
      positionRef.current = engine.seek(terminalSink(terminal), 0);
      setPosition(positionRef.current);
    }
  }, [chunks]);

  useEffect(() => {
    if (!playing) return;
    const timer = window.setInterval(() => {
      const engine = engineRef.current;
      const terminal = terminalRef.current;
      if (!engine || !terminal) return;
      const next = engine.playStep(terminalSink(terminal), positionRef.current + TICK_MS * speedRef.current);
      positionRef.current = next;
      setPosition(next);
      if (next >= engine.durationMs()) {
        setPlaying(false);
      }
    }, TICK_MS);
    return () => window.clearInterval(timer);
  }, [playing]);

  const seekTo = useCallback((value: number) => {
    const engine = engineRef.current;
    const terminal = terminalRef.current;
    if (!engine || !terminal) return;
    positionRef.current = engine.seek(terminalSink(terminal), value);
    setPosition(positionRef.current);
  }, []);

  const togglePlay = () => {
    const engine = engineRef.current;
    if (!engine || engine.durationMs() <= 0) return;
    if (!playing && positionRef.current >= engine.durationMs()) {
      seekTo(0);
    }
    setPlaying(!playing);
  };

  const changeSpeed = (value: number) => {
    speedRef.current = value;
    setSpeed(value);
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col" data-testid="transcript-replay">
      <div className="flex shrink-0 items-center gap-2 border-b border-neutral-800/60 px-3 py-2">
        <button
          className="nx-btn nx-btn-ghost nx-btn-sm"
          disabled={!chunks || loadError !== null || duration <= 0}
          onClick={togglePlay}
          aria-label={playing ? "暂停回放" : "播放回放"}
        >
          {playing ? null : <IconPlay size={12} />}
          {playing ? "暂停" : "播放"}
        </button>
        <select
          className="nx-select"
          aria-label="回放倍速"
          value={speed}
          disabled={!chunks || loadError !== null}
          onChange={(event) => changeSpeed(Number(event.target.value))}
        >
          {SPEEDS.map((value) => (
            <option key={value} value={value}>
              {value}x
            </option>
          ))}
        </select>
        <input
          type="range"
          className="flex-1"
          aria-label="回放进度"
          min={0}
          max={Math.max(1, duration)}
          step={100}
          value={Math.min(position, Math.max(1, duration))}
          disabled={!chunks || loadError !== null || duration <= 0}
          onChange={(event) => seekTo(Number(event.target.value))}
        />
        <span className="font-mono text-[11px] text-neutral-500" data-testid="replay-clock">
          {formatReplayClock(position)} / {formatReplayClock(duration)}
        </span>
      </div>
      <div className="relative min-h-0 flex-1 overflow-hidden">
        <div ref={containerRef} className="h-full w-full px-2 py-1" />
        {!chunks && !loadError && (
          <div className="absolute inset-0 flex items-center justify-center bg-neutral-900/80 text-[12px] text-neutral-500">
            加载回放数据…
          </div>
        )}
        {loadError && (
          <div className="absolute inset-0 flex items-center justify-center gap-2 bg-neutral-900/80 text-[12px] text-red-300">
            <IconAlert size={13} />
            加载回放数据失败:{loadError}
          </div>
        )}
        {chunks && chunks.length === 0 && (
          <div className="absolute inset-0 flex items-center justify-center bg-neutral-900/80 text-[12px] text-neutral-500">
            这条记录没有任何可回放的内容
          </div>
        )}
      </div>
    </div>
  );
}

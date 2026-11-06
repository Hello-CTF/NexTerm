import { useEffect, useRef, type RefObject } from "react";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { REPLAY_THEME, type ReplayChunk, type ReplaySink, type TranscriptReplay } from "../../../src/features/terminal/transcriptReplay";

const encoder = new TextEncoder();

// chunksOf turns [ms, text] pairs into replay chunks the way a recording stores them.
export function chunksOf(source: readonly (readonly [number, string])[]): ReplayChunk[] {
  return source.map(([ts, data], index) => ({ seq: index + 1, ts, kind: 0, data: encoder.encode(data) }));
}

export function sinkOf(terminal: Terminal): ReplaySink {
  return { reset: () => terminal.reset(), write: (data, callback) => terminal.write(data, callback), resize: (cols, rows) => terminal.resize(cols, rows) };
}

// useReplayTerminal opens a read-only xterm in container and paints the replay at the given position. The returned
// ref holds the terminal while it is mounted.
export function useReplayTerminal(container: RefObject<HTMLDivElement | null>, replay: TranscriptReplay, position: number): RefObject<Terminal | null> {
  const terminalRef = useRef<Terminal | null>(null);
  const initial = useRef(position);
  useEffect(() => {
    const host = container.current;
    if (!host) return;
    const terminal = new Terminal({
      disableStdin: true,
      cursorBlink: false,
      fontFamily: '"Cascadia Mono", "JetBrains Mono", "Cascadia Code", Consolas, "SFMono-Regular", monospace',
      fontSize: host.clientWidth < 520 ? 11 : 12,
      lineHeight: 1.2,
      scrollback: 200,
      theme: REPLAY_THEME,
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(host);
    terminalRef.current = terminal;
    fit.fit();
    replay.seek(sinkOf(terminal), initial.current);
    const observer = new ResizeObserver(() => fit.fit());
    observer.observe(host);
    return () => {
      observer.disconnect();
      terminal.dispose();
      terminalRef.current = null;
    };
  }, [container, replay]);
  return terminalRef;
}

/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { Terminal } from "@xterm/xterm";
import {
  toReplayChunk,
  TranscriptReplay,
  TRANSCRIPT_KIND_OUTPUT,
  TRANSCRIPT_KIND_RESIZE,
  type ReplaySink,
} from "../../features/terminal/transcriptReplay";

function chunk(seq: number, ts: number, kind: number, text: string) {
  const bytes = new TextEncoder().encode(text);
  return toReplayChunk({
    seq,
    ts,
    kind,
    dataBase64: btoa(String.fromCharCode(...bytes)),
  });
}

describe("TranscriptReplay xterm write ordering", () => {
  it("finishes only the last of 20 seeks with the expected real terminal content", async () => {
    const terminal = new Terminal({ cols: 80, rows: 10, allowProposedApi: true });
    const host = document.createElement("div");
    document.body.appendChild(host);
    terminal.open(host);
    const replay = new TranscriptReplay([
      chunk(0, 1000, TRANSCRIPT_KIND_OUTPUT, "A"),
      chunk(1, 1100, TRANSCRIPT_KIND_OUTPUT, "B"),
      chunk(2, 1200, TRANSCRIPT_KIND_RESIZE, '{"cols":120,"rows":40}'),
      chunk(3, 1300, TRANSCRIPT_KIND_OUTPUT, "C"),
      chunk(4, 1500, TRANSCRIPT_KIND_OUTPUT, "D"),
    ]);
    let resets = 0;
    let finalWrite!: () => void;
    const finalWriteComplete = new Promise<void>((resolve) => {
      finalWrite = resolve;
    });
    const sink: ReplaySink = {
      reset: () => {
        resets += 1;
        terminal.reset();
      },
      write: (data, callback) => {
        const finalChunk = resets === 1 && new TextDecoder().decode(data) === "C";
        terminal.write(data, () => {
          callback?.();
          if (finalChunk) finalWrite();
        });
      },
      resize: (cols, rows) => terminal.resize(cols, rows),
    };

    try {
      replay.playStep(sink, 0);
      for (let i = 0; i < 20; i += 1) replay.seek(sink, i * 25);
      expect(resets).toBe(0);
      await finalWriteComplete;

      expect(resets).toBe(1);
      expect(terminal.buffer.active.getLine(0)?.translateToString(true)).toBe("ABC");
      expect(terminal.cols).toBe(120);
      expect(terminal.rows).toBe(40);
    } finally {
      replay.dispose();
      terminal.dispose();
      host.remove();
    }
  });
});

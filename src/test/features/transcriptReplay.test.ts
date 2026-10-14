import { describe, expect, it } from "vitest";
import {
  formatReplayClock,
  parseResizePayload,
  toReplayChunk,
  TranscriptReplay,
  TRANSCRIPT_KIND_INPUT,
  TRANSCRIPT_KIND_OUTPUT,
  TRANSCRIPT_KIND_RESIZE,
  type ReplaySink,
} from "../../features/terminal/transcriptReplay";

function b64(text: string): string {
  return btoa(String.fromCharCode(...new TextEncoder().encode(text)));
}

class RecordingSink implements ReplaySink {
  resets = 0;
  writes: string[] = [];
  resizes: { cols: number; rows: number }[] = [];

  reset() {
    this.resets += 1;
  }

  write(data: Uint8Array) {
    this.writes.push(new TextDecoder().decode(data));
  }

  resize(cols: number, rows: number) {
    this.resizes.push({ cols, rows });
  }
}

function chunk(seq: number, ts: number, kind: number, text: string) {
  return toReplayChunk({ seq, ts, kind, dataBase64: b64(text) });
}

describe("toReplayChunk", () => {
  it("defaults kind to output", () => {
    const decoded = toReplayChunk({ seq: 1, ts: 10, dataBase64: b64("out") });
    expect(decoded.kind).toBe(TRANSCRIPT_KIND_OUTPUT);
    expect(new TextDecoder().decode(decoded.data)).toBe("out");
  });
});

describe("parseResizePayload", () => {
  it("accepts valid geometry", () => {
    expect(parseResizePayload(new TextEncoder().encode('{"cols":80,"rows":24}'))).toEqual({ cols: 80, rows: 24 });
  });

  it("rejects malformed, non-integer, and non-positive geometry", () => {
    expect(parseResizePayload(new TextEncoder().encode("not-json"))).toBeNull();
    expect(parseResizePayload(new TextEncoder().encode('{"cols":0,"rows":24}'))).toBeNull();
    expect(parseResizePayload(new TextEncoder().encode('{"cols":80.5,"rows":24}'))).toBeNull();
    expect(parseResizePayload(new TextEncoder().encode('{"cols":1001,"rows":24}'))).toBeNull();
    expect(parseResizePayload(new TextEncoder().encode("{}"))).toBeNull();
  });
});

describe("TranscriptReplay", () => {
  const chunks = [
    chunk(0, 1000, TRANSCRIPT_KIND_RESIZE, '{"cols":80,"rows":24}'),
    chunk(1, 1000, TRANSCRIPT_KIND_OUTPUT, "hello "),
    chunk(2, 1500, TRANSCRIPT_KIND_INPUT, "secret\r"),
    chunk(3, 2000, TRANSCRIPT_KIND_OUTPUT, "world"),
    chunk(4, 2500, TRANSCRIPT_KIND_RESIZE, '{"cols":120,"rows":40}'),
  ];

  it("seek(0) applies the initial resize and output", () => {
    const replay = new TranscriptReplay(chunks);
    const sink = new RecordingSink();
    expect(replay.seek(sink, 0)).toBe(0);
    expect(sink.resets).toBe(1);
    expect(sink.resizes).toEqual([{ cols: 80, rows: 24 }]);
    expect(sink.writes).toEqual(["hello "]);
  });

  it("playStep advances by position and ignores input", () => {
    const replay = new TranscriptReplay(chunks);
    const sink = new RecordingSink();
    replay.seek(sink, 0);
    expect(replay.playStep(sink, 600)).toBe(600);
    expect(sink.writes).toEqual(["hello "]);
    expect(replay.playStep(sink, 1000)).toBe(1000);
    expect(sink.writes).toEqual(["hello ", "world"]);
    expect(sink.writes.join("")).not.toContain("secret");
    expect(replay.playStep(sink, 1500)).toBe(1500);
    expect(sink.resizes).toEqual([
      { cols: 80, rows: 24 },
      { cols: 120, rows: 40 },
    ]);
  });

  it("seeking backward resets the terminal and replays from zero", () => {
    const replay = new TranscriptReplay(chunks);
    const sink = new RecordingSink();
    replay.seek(sink, 0);
    replay.playStep(sink, 2000);
    expect(sink.writes).toEqual(["hello ", "world"]);
    const resetsBefore = sink.resets;
    const writesBefore = sink.writes.length;
    expect(replay.seek(sink, 500)).toBe(500);
    expect(sink.resets).toBe(resetsBefore + 1);
    expect(sink.writes.length).toBe(writesBefore + 1);
    expect(sink.writes.at(-1)).toBe("hello ");
  });

  it("clamps position to the duration and stops applying chunks after the end", () => {
    const replay = new TranscriptReplay(chunks);
    const sink = new RecordingSink();
    expect(replay.durationMs()).toBe(1500);
    expect(replay.playStep(sink, 99999)).toBe(1500);
    const writes = sink.writes.length;
    expect(replay.playStep(sink, 99999)).toBe(1500);
    expect(sink.writes.length).toBe(writes);
  });

  it("uses zero duration for an empty timeline and seeks safely", () => {
    const replay = new TranscriptReplay([]);
    const sink = new RecordingSink();
    expect(replay.durationMs()).toBe(0);
    expect(replay.seek(sink, 100)).toBe(0);
    expect(sink.writes).toEqual([]);
  });
});

describe("formatReplayClock", () => {
  it("formats time as mm:ss", () => {
    expect(formatReplayClock(0)).toBe("00:00");
    expect(formatReplayClock(65000)).toBe("01:05");
    expect(formatReplayClock(-5)).toBe("00:00");
  });
});

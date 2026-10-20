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
  events: string[] = [];
  content = "";

  reset() {
    this.resets += 1;
    this.content = "";
    this.events.push("reset");
  }

  protected append(data: Uint8Array): void {
    const text = new TextDecoder().decode(data);
    this.writes.push(text);
    this.content += text;
    this.events.push(`write:${text}`);
  }

  write(data: Uint8Array, callback?: () => void) {
    this.append(data);
    callback?.();
  }

  resize(cols: number, rows: number) {
    this.resizes.push({ cols, rows });
    this.events.push(`resize:${cols}x${rows}`);
  }
}

class DeferredRecordingSink extends RecordingSink {
  private callbacks: (() => void)[] = [];

  override write(data: Uint8Array, callback?: () => void) {
    this.append(data);
    if (callback) this.callbacks.push(callback);
  }

  get pendingWrites(): number {
    return this.callbacks.length;
  }

  completeNext(): void {
    const callback = this.callbacks.shift();
    if (!callback) throw new Error("No pending write");
    callback();
  }

  completeAll(): void {
    while (this.callbacks.length > 0) this.completeNext();
  }
}

function chunk(seq: number, ts: number, kind: number, text: string) {
  return toReplayChunk({ seq, ts, kind, dataBase64: b64(text) });
}

async function seek(replay: TranscriptReplay, sink: ReplaySink, position: number): Promise<number> {
  const result = replay.seek(sink, position);
  await Promise.resolve();
  return result;
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

  it("seek(0) applies the initial resize and output", async () => {
    const replay = new TranscriptReplay(chunks);
    const sink = new RecordingSink();
    expect(await seek(replay, sink, 0)).toBe(0);
    expect(sink.resets).toBe(1);
    expect(sink.resizes).toEqual([{ cols: 80, rows: 24 }]);
    expect(sink.writes).toEqual(["hello "]);
  });

  it("playStep advances by position and ignores input", async () => {
    const replay = new TranscriptReplay(chunks);
    const sink = new RecordingSink();
    await seek(replay, sink, 0);
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

  it("seeking backward resets the terminal and replays from zero", async () => {
    const replay = new TranscriptReplay(chunks);
    const sink = new RecordingSink();
    await seek(replay, sink, 0);
    replay.playStep(sink, 2000);
    expect(sink.writes).toEqual(["hello ", "world"]);
    const resetsBefore = sink.resets;
    const writesBefore = sink.writes.length;
    expect(await seek(replay, sink, 500)).toBe(500);
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

  it("uses zero duration for an empty timeline and seeks safely", async () => {
    const replay = new TranscriptReplay([]);
    const sink = new RecordingSink();
    expect(replay.durationMs()).toBe(0);
    expect(await seek(replay, sink, 100)).toBe(0);
    expect(sink.writes).toEqual([]);
  });

  it("waits for output callbacks before resize events and preserves byte order", async () => {
    const replay = new TranscriptReplay(chunks);
    const sink = new DeferredRecordingSink();
    replay.seek(sink, 1500);
    await Promise.resolve();
    expect(sink.events).toEqual(["reset", "resize:80x24", "write:hello "]);
    sink.completeNext();
    expect(sink.events).toEqual(["reset", "resize:80x24", "write:hello ", "write:world"]);
    sink.completeNext();
    expect(sink.events).toEqual([
      "reset",
      "resize:80x24",
      "write:hello ",
      "write:world",
      "resize:120x40",
    ]);
    expect(sink.content).toBe("hello world");
    expect(sink.pendingWrites).toBe(0);
  });

  it("catches up to the latest play position after a pending write", () => {
    const replay = new TranscriptReplay(chunks);
    const sink = new DeferredRecordingSink();
    replay.playStep(sink, 0);
    replay.playStep(sink, 1500);
    expect(sink.content).toBe("hello ");

    sink.completeAll();

    expect(sink.content).toBe("hello world");
    expect(sink.resizes).toEqual([
      { cols: 80, rows: 24 },
      { cols: 120, rows: 40 },
    ]);
  });

  it("coalesces 20 seeks behind a pending write into one final replay", async () => {
    const replay = new TranscriptReplay([
      chunk(0, 1000, TRANSCRIPT_KIND_OUTPUT, "A"),
      chunk(1, 1100, TRANSCRIPT_KIND_OUTPUT, "B"),
      chunk(2, 1200, TRANSCRIPT_KIND_RESIZE, '{"cols":120,"rows":40}'),
      chunk(3, 1300, TRANSCRIPT_KIND_OUTPUT, "C"),
      chunk(4, 1500, TRANSCRIPT_KIND_OUTPUT, "D"),
    ]);
    const sink = new DeferredRecordingSink();
    replay.playStep(sink, 0);
    expect(sink.pendingWrites).toBe(1);
    for (let i = 0; i < 20; i += 1) replay.seek(sink, i * 25);
    expect(sink.resets).toBe(0);

    sink.completeNext();
    await Promise.resolve();
    expect(sink.resets).toBe(1);
    sink.completeAll();

    expect(sink.resets).toBe(1);
    expect(sink.events).toEqual(["write:A", "reset", "write:AB", "resize:120x40", "write:C"]);
    expect(sink.content).toBe("ABC");
    expect(sink.pendingWrites).toBe(0);
  });
});

describe("formatReplayClock", () => {
  it("formats time as mm:ss", () => {
    expect(formatReplayClock(0)).toBe("00:00");
    expect(formatReplayClock(65000)).toBe("01:05");
    expect(formatReplayClock(-5)).toBe("00:00");
  });
});

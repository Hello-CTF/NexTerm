/** @vitest-environment jsdom */

import { SerializeAddon } from "@xterm/addon-serialize";
import { Terminal } from "@xterm/xterm";
import { describe, expect, it } from "vitest";

import { KeyframeBuilder, type KeyframeSource } from "../../features/terminal/transcriptKeyframes";
import {
  TRANSCRIPT_KIND_INPUT,
  TRANSCRIPT_KIND_OUTPUT,
  TRANSCRIPT_KIND_RESIZE,
  TranscriptReplay,
  type ReplayChunk,
  type ReplayKeyframe,
  type ReplaySink,
} from "../../features/terminal/transcriptReplay";

const encoder = new TextEncoder();

function chunk(seq: number, ts: number, kind: number, text: string): ReplayChunk {
  return { seq, ts, kind, data: encoder.encode(text) };
}

// One output line per second with colors, plus a resize, an alternate-screen visit and a screen clear along the way.
function recording(seconds: number): ReplayChunk[] {
  const chunks: ReplayChunk[] = [chunk(0, 1000, TRANSCRIPT_KIND_RESIZE, '{"cols":80,"rows":24}')];
  let seq = 1;
  for (let second = 1; second <= seconds; second += 1) {
    const ts = 1000 + second * 1000;
    chunks.push(chunk(seq++, ts, TRANSCRIPT_KIND_OUTPUT, `\x1b[3${second % 8}mline ${second}\x1b[0m\r\n`));
    chunks.push(chunk(seq++, ts, TRANSCRIPT_KIND_INPUT, "typed"));
    if (second === 40) chunks.push(chunk(seq++, ts, TRANSCRIPT_KIND_RESIZE, '{"cols":100,"rows":30}'));
    if (second === 70) chunks.push(chunk(seq++, ts, TRANSCRIPT_KIND_OUTPUT, "\x1b[?1049h\x1b[2J\x1b[Halt screen"));
    if (second === 90) chunks.push(chunk(seq++, ts, TRANSCRIPT_KIND_OUTPUT, "\x1b[?1049l"));
    if (second === 110) chunks.push(chunk(seq++, ts, TRANSCRIPT_KIND_OUTPUT, "\x1b[2J\x1b[Hcleared"));
  }
  return chunks;
}

function realSource(): KeyframeSource {
  const terminal = new Terminal({ scrollback: 5000, allowProposedApi: true });
  const serializer = new SerializeAddon();
  terminal.loadAddon(serializer);
  return {
    write: (data, callback) => terminal.write(data, callback),
    resize: (cols, rows) => terminal.resize(cols, rows),
    serialize: () => ({ cols: terminal.cols, rows: terminal.rows, state: serializer.serialize() }),
    dispose: () => terminal.dispose(),
  };
}

async function buildFrames(chunks: ReplayChunk[], intervalMs: number): Promise<ReplayKeyframe[]> {
  const frames: ReplayKeyframe[] = [];
  const builder = new KeyframeBuilder({
    createSource: realSource,
    intervalMs,
    onFrames: (batch) => frames.push(...batch),
    yieldToUi: async () => undefined,
  });
  builder.feed(chunks);
  builder.finish();
  await builder.whenIdle();
  return frames;
}

interface RealSink extends ReplaySink {
  snapshot(): { cols: number; rows: number; state: string };
  dispose(): void;
}

function realSink(): RealSink {
  const terminal = new Terminal({ scrollback: 5000, allowProposedApi: true });
  const serializer = new SerializeAddon();
  terminal.loadAddon(serializer);
  return {
    reset: () => terminal.reset(),
    write: (data, callback) => terminal.write(data, callback),
    resize: (cols, rows) => terminal.resize(cols, rows),
    restore: (frame, callback) => {
      terminal.resize(frame.cols, frame.rows);
      terminal.write(frame.state, callback);
    },
    snapshot: () => ({ cols: terminal.cols, rows: terminal.rows, state: serializer.serialize() }),
    dispose: () => terminal.dispose(),
  };
}

function settled(engine: TranscriptReplay): Promise<void> {
  return new Promise((resolve) => {
    engine.onSettled = () => resolve();
  });
}

describe("keyframes", () => {
  it("builds a frame once the interval of recorded time has passed", async () => {
    const frames = await buildFrames(recording(130), 30_000);
    expect(frames.length).toBeGreaterThanOrEqual(4);
    for (let index = 1; index < frames.length; index += 1) {
      expect(frames[index].positionMs - frames[index - 1].positionMs).toBeGreaterThanOrEqual(30_000);
      expect(frames[index].nextIndex).toBeGreaterThan(frames[index - 1].nextIndex);
    }
  });

  it("builds a frame early once enough output bytes have built up", async () => {
    const chunks = recording(20);
    const frames: ReplayKeyframe[] = [];
    const builder = new KeyframeBuilder({
      createSource: realSource,
      intervalMs: 3_600_000,
      intervalBytes: 100,
      onFrames: (batch) => frames.push(...batch),
      yieldToUi: async () => undefined,
    });
    builder.feed(chunks);
    builder.finish();
    await builder.whenIdle();
    expect(frames.length).toBeGreaterThan(1);
  });

  it("restores the same screen as replaying from the start, wherever the seek lands", async () => {
    const chunks = recording(130);
    const frames = await buildFrames(chunks, 30_000);
    const withFrames = new TranscriptReplay(chunks);
    withFrames.addKeyframes(frames);
    const plain = new TranscriptReplay(chunks);
    const sinkA = realSink();
    const sinkB = realSink();
    try {
      for (const target of [5_000, 31_000, 45_000, 75_000, 95_000, 118_000, 130_000]) {
        const done = settled(withFrames);
        withFrames.seek(sinkA, target);
        await done;
        const plainDone = settled(plain);
        plain.seek({ reset: sinkB.reset, write: sinkB.write, resize: sinkB.resize }, target);
        await plainDone;
        expect(sinkA.snapshot()).toEqual(sinkB.snapshot());
      }
    } finally {
      withFrames.dispose();
      plain.dispose();
      sinkA.dispose();
      sinkB.dispose();
    }
  }, 30_000);

  it("replays only the tail after the nearest keyframe", async () => {
    const chunks = recording(130);
    const frames = await buildFrames(chunks, 30_000);
    const engine = new TranscriptReplay(chunks);
    engine.addKeyframes(frames);
    let written = 0;
    let restored = 0;
    const sink: ReplaySink = {
      reset: () => undefined,
      write: (data, callback) => {
        written += data.byteLength;
        callback?.();
      },
      resize: () => undefined,
      restore: (_frame, callback) => {
        restored += 1;
        callback();
      },
    };
    const done = settled(engine);
    engine.seek(sink, 100_000);
    await done;
    expect(restored).toBe(1);
    const total = chunks.filter((c) => c.kind === TRANSCRIPT_KIND_OUTPUT).reduce((n, c) => n + c.data.byteLength, 0);
    expect(written).toBeLessThan(total / 2);
  });

  it("falls back to replaying from the start before any keyframe covers the target", async () => {
    const chunks = recording(30);
    const engine = new TranscriptReplay(chunks);
    let restored = 0;
    const sink: ReplaySink = {
      reset: () => undefined,
      write: (_data, callback) => callback?.(),
      resize: () => undefined,
      restore: (_frame, callback) => {
        restored += 1;
        callback();
      },
    };
    const done = settled(engine);
    engine.seek(sink, 20_000);
    await done;
    expect(restored).toBe(0);
  });

  it("applies pages appended after the replay caught up", () => {
    const first = [chunk(0, 1000, TRANSCRIPT_KIND_OUTPUT, "a")];
    const engine = new TranscriptReplay(first);
    const seen: string[] = [];
    const sink: ReplaySink = {
      reset: () => undefined,
      write: (data, callback) => {
        seen.push(new TextDecoder().decode(data));
        callback?.();
      },
      resize: () => undefined,
    };
    return new Promise<void>((resolve) => {
      engine.onSettled = () => {
        if (seen.length === 2) resolve();
      };
      engine.seek(sink, 0);
      queueMicrotask(() => {
        setTimeout(() => engine.append([chunk(1, 1000, TRANSCRIPT_KIND_OUTPUT, "b")]), 0);
      });
    }).then(() => expect(seen.join("")).toBe("ab"));
  });
});

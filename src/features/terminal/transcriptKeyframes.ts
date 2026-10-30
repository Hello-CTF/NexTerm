import { SerializeAddon } from "@xterm/addon-serialize";
import { Terminal } from "@xterm/xterm";

import {
  parseResizePayload,
  TRANSCRIPT_KIND_OUTPUT,
  TRANSCRIPT_KIND_RESIZE,
  type ReplayChunk,
  type ReplayKeyframe,
} from "./transcriptReplay";

// A keyframe every minute of recorded time, or sooner once this many bytes of output have built up, so the tail a
// seek replays after a keyframe stays bounded for dense output as well.
export const KEYFRAME_INTERVAL_MS = 60_000;
export const KEYFRAME_INTERVAL_BYTES = 8 * 1024 * 1024;
const BUILD_BATCH_BYTES = 256 * 1024;
export const KEYFRAME_SCROLLBACK = 5000;

// The offscreen terminal the index is built on: the same emulator the replay view uses, so a keyframe paints exactly
// what a replay from the start would.
export interface KeyframeSource {
  write(data: Uint8Array, callback: () => void): void;
  resize(cols: number, rows: number): void;
  serialize(): { cols: number; rows: number; state: string };
  dispose(): void;
}

export function createXtermKeyframeSource(): KeyframeSource {
  const terminal = new Terminal({ scrollback: KEYFRAME_SCROLLBACK, allowProposedApi: true });
  const serializer = new SerializeAddon();
  terminal.loadAddon(serializer);
  return {
    write: (data, callback) => terminal.write(data, callback),
    resize: (cols, rows) => terminal.resize(cols, rows),
    serialize: () => ({ cols: terminal.cols, rows: terminal.rows, state: serializer.serialize() }),
    dispose: () => terminal.dispose(),
  };
}

export interface KeyframeBuilderOptions {
  createSource?: () => KeyframeSource;
  intervalMs?: number;
  intervalBytes?: number;
  onFrames: (frames: ReplayKeyframe[]) => void;
  // Lets input and rendering run between batches.
  yieldToUi?: () => Promise<void>;
}

function defaultYield(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

// Builds keyframes in the background from chunks fed page by page. Chunk indexes are positions in the feed order, which
// is the order the replay engine holds them in.
export class KeyframeBuilder {
  private readonly createSource: () => KeyframeSource;
  private readonly intervalMs: number;
  private readonly intervalBytes: number;
  private readonly onFrames: (frames: ReplayKeyframe[]) => void;
  private readonly yieldToUi: () => Promise<void>;
  private source: KeyframeSource | null = null;
  private queue: ReplayChunk[] = [];
  private fed = 0;
  private applied = 0;
  private startTs = 0;
  private lastFrameTs = 0;
  private lastAppliedTs = 0;
  private bytesSinceFrame = 0;
  private running = false;
  private finished = false;
  private disposed = false;
  private idle: (() => void)[] = [];

  constructor(options: KeyframeBuilderOptions) {
    this.createSource = options.createSource ?? createXtermKeyframeSource;
    this.intervalMs = options.intervalMs ?? KEYFRAME_INTERVAL_MS;
    this.intervalBytes = options.intervalBytes ?? KEYFRAME_INTERVAL_BYTES;
    this.onFrames = options.onFrames;
    this.yieldToUi = options.yieldToUi ?? defaultYield;
  }

  // Chunks processed so far, for the progress display.
  processedChunks(): number {
    return this.applied;
  }

  feed(chunks: ReplayChunk[]): void {
    if (this.disposed || chunks.length === 0) return;
    if (this.fed === 0) {
      this.startTs = chunks[0].ts;
      this.lastFrameTs = chunks[0].ts;
    }
    this.fed += chunks.length;
    for (const chunk of chunks) this.queue.push(chunk);
    void this.run();
  }

  // Resolves once everything fed so far has been processed.
  whenIdle(): Promise<void> {
    if (!this.running && this.queue.length === 0) return Promise.resolve();
    return new Promise((resolve) => this.idle.push(resolve));
  }

  finish(): void {
    this.finished = true;
    if (!this.running && this.queue.length === 0) this.releaseSource();
  }

  dispose(): void {
    this.disposed = true;
    this.queue = [];
    this.releaseSource();
    this.settle();
  }

  private releaseSource(): void {
    this.source?.dispose();
    this.source = null;
  }

  private settle(): void {
    const waiting = this.idle;
    this.idle = [];
    for (const resolve of waiting) resolve();
  }

  private async run(): Promise<void> {
    if (this.running || this.disposed) return;
    this.running = true;
    try {
      this.source ??= this.createSource();
      while (!this.disposed && this.queue.length > 0) {
        await this.applyBatch(this.source);
        await this.yieldToUi();
      }
    } finally {
      this.running = false;
      if (this.finished && this.queue.length === 0) this.releaseSource();
      this.settle();
    }
  }

  private async applyBatch(source: KeyframeSource): Promise<void> {
    const frames: ReplayKeyframe[] = [];
    let batchBytes = 0;
    while (this.queue.length > 0 && batchBytes < BUILD_BATCH_BYTES && !this.disposed) {
      const chunk = this.queue[0];
      if (chunk.kind === TRANSCRIPT_KIND_OUTPUT) {
        if (batchBytes > 0 && batchBytes + chunk.data.byteLength > BUILD_BATCH_BYTES) break;
        this.queue.shift();
        await new Promise<void>((resolve) => source.write(chunk.data, resolve));
        batchBytes += chunk.data.byteLength;
        this.bytesSinceFrame += chunk.data.byteLength;
      } else {
        this.queue.shift();
        if (chunk.kind === TRANSCRIPT_KIND_RESIZE) {
          const size = parseResizePayload(chunk.data);
          if (size) source.resize(size.cols, size.rows);
        }
      }
      this.applied += 1;
      this.lastAppliedTs = chunk.ts;
      if (
        this.lastAppliedTs - this.lastFrameTs >= this.intervalMs ||
        this.bytesSinceFrame >= this.intervalBytes
      ) {
        const screen = source.serialize();
        frames.push({
          positionMs: this.lastAppliedTs - this.startTs,
          nextIndex: this.applied,
          cols: screen.cols,
          rows: screen.rows,
          state: screen.state,
        });
        this.lastFrameTs = this.lastAppliedTs;
        this.bytesSinceFrame = 0;
      }
    }
    if (frames.length > 0 && !this.disposed) this.onFrames(frames);
  }
}

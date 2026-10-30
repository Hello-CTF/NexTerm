import { decodeBase64 } from "./transcriptText";

export const TRANSCRIPT_KIND_OUTPUT = 0;
export const TRANSCRIPT_KIND_INPUT = 1;
export const TRANSCRIPT_KIND_RESIZE = 2;

export interface ReplayChunk {
  seq: number;
  ts: number;
  kind: number;
  data: Uint8Array;
}

export interface ReplayResize {
  cols: number;
  rows: number;
}

export interface ReplaySink {
  reset(): void;
  write(data: Uint8Array, callback?: () => void): void;
  resize(cols: number, rows: number): void;
  // Paints a keyframe's screen state; sinks without it fall back to replaying from the start.
  restore?(frame: ReplayKeyframe, callback: () => void): void;
}

// A serialized terminal screen at one point of the recording. nextIndex is the first chunk the state does not
// include, and positionMs is the timestamp (relative to the first chunk) of the last chunk it includes, so a seek
// target at or after positionMs can start from the frame.
export interface ReplayKeyframe {
  positionMs: number;
  nextIndex: number;
  cols: number;
  rows: number;
  state: string;
}

export function toReplayChunk(chunk: { seq: number; ts: number; kind?: number; dataBase64: string }): ReplayChunk {
  return {
    seq: chunk.seq,
    ts: chunk.ts,
    kind: chunk.kind ?? TRANSCRIPT_KIND_OUTPUT,
    data: decodeBase64(chunk.dataBase64),
  };
}

export function parseResizePayload(data: Uint8Array): ReplayResize | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(new TextDecoder().decode(data));
  } catch {
    return null;
  }
  if (!parsed || typeof parsed !== "object") return null;
  const { cols, rows } = parsed as { cols?: unknown; rows?: unknown };
  if (!Number.isInteger(cols) || !Number.isInteger(rows)) return null;
  if ((cols as number) < 1 || (rows as number) < 1 || (cols as number) > 1000 || (rows as number) > 1000) {
    return null;
  }
  return { cols: cols as number, rows: rows as number };
}

const REPLAY_WRITE_BATCH_BYTES = 256 * 1024;

interface PendingSeek {
  sink: ReplaySink;
  position: number;
}

function findKeyframe(frames: readonly ReplayKeyframe[], position: number): ReplayKeyframe | null {
  let low = 0;
  let high = frames.length - 1;
  let found: ReplayKeyframe | null = null;
  while (low <= high) {
    const mid = (low + high) >> 1;
    if (frames[mid].positionMs <= position) {
      found = frames[mid];
      low = mid + 1;
    } else {
      high = mid - 1;
    }
  }
  return found;
}

export class TranscriptReplay {
  private chunks: ReplayChunk[];
  private startTs: number;
  private endTs: number;
  private keyframes: ReplayKeyframe[] = [];
  private nextIndex = 0;
  private activeSink: ReplaySink | null = null;
  private activePosition = 0;
  // Where the replay last caught up, so pages that arrive later and fall at or before it still get applied.
  private idleSink: ReplaySink | null = null;
  private idlePosition = 0;
  private pendingSeek: PendingSeek | null = null;
  private writePending = false;
  private writeToken = 0;
  private seekScheduled = false;
  private pumping = false;
  private disposed = false;
  // Fires whenever the replay has caught up with the requested position (a finished seek or an idle playback tick).
  onSettled: (() => void) | null = null;

  constructor(chunks: ReplayChunk[]) {
    this.chunks = [...chunks].sort((a, b) => a.seq - b.seq);
    this.startTs = this.chunks.length > 0 ? this.chunks[0].ts : 0;
    this.endTs = this.chunks.length > 0 ? this.chunks[this.chunks.length - 1].ts : this.startTs;
  }

  durationMs(): number {
    return this.endTs - this.startTs;
  }

  chunkCount(): number {
    return this.chunks.length;
  }

  // Adds chunks that arrive after construction (a transcript still loading page by page).
  append(chunks: ReplayChunk[]): void {
    if (chunks.length === 0 || this.disposed) return;
    const wasEmpty = this.chunks.length === 0;
    const lastSeq = this.chunks.length > 0 ? this.chunks[this.chunks.length - 1].seq : -Infinity;
    const ordered = [...chunks].sort((a, b) => a.seq - b.seq);
    let reordered = false;
    if (ordered[0].seq <= lastSeq) {
      // An out-of-order page invalidates every index into the chunk list.
      reordered = true;
      this.chunks = [...this.chunks, ...ordered].sort((a, b) => a.seq - b.seq);
      this.keyframes = [];
    } else {
      for (const chunk of ordered) this.chunks.push(chunk);
    }
    if (wasEmpty || reordered) this.startTs = this.chunks[0].ts;
    this.endTs = Math.max(this.endTs, this.chunks[this.chunks.length - 1].ts);
    const sink = this.idleSink;
    if (sink && !this.pendingSeek && !this.writePending && !this.activeSink) {
      this.activeSink = sink;
      this.activePosition = this.idlePosition;
      this.pump();
    }
  }

  chunkAt(index: number): ReplayChunk | undefined {
    return this.chunks[index];
  }

  firstTs(): number {
    return this.startTs;
  }

  // Keyframes arrive in ascending order while the index builds in the background.
  addKeyframes(frames: ReplayKeyframe[]): void {
    if (this.disposed) return;
    for (const frame of frames) {
      const last = this.keyframes[this.keyframes.length - 1];
      if (!last || frame.positionMs > last.positionMs) this.keyframes.push(frame);
    }
  }

  keyframeList(): ReplayKeyframe[] {
    return [...this.keyframes];
  }

  // Latest position a seek reaches without a replay from the start.
  indexedThroughMs(): number {
    const last = this.keyframes[this.keyframes.length - 1];
    return last ? last.positionMs : 0;
  }

  clampPosition(positionMs: number): number {
    return Math.min(Math.max(0, positionMs), this.durationMs());
  }

  seek(sink: ReplaySink, positionMs: number): number {
    const position = this.clampPosition(positionMs);
    if (this.disposed) return position;
    this.pendingSeek = { sink, position };
    this.scheduleSeek();
    return position;
  }

  playStep(sink: ReplaySink, positionMs: number): number {
    const position = this.clampPosition(positionMs);
    if (this.disposed || this.pendingSeek) return position;
    if (this.writePending) {
      this.activePosition = Math.max(this.activePosition, position);
      return position;
    }
    this.activeSink = sink;
    this.activePosition = position;
    this.pump();
    return position;
  }

  dispose(): void {
    this.disposed = true;
    this.pendingSeek = null;
    this.activeSink = null;
    this.writePending = false;
    this.writeToken += 1;
  }

  private scheduleSeek(): void {
    if (this.seekScheduled || this.disposed) return;
    this.seekScheduled = true;
    queueMicrotask(() => {
      this.seekScheduled = false;
      this.flushSeek();
    });
  }

  private flushSeek(): void {
    if (this.disposed || this.writePending || this.pumping || !this.pendingSeek) return;
    const { sink, position } = this.pendingSeek;
    this.pendingSeek = null;
    this.activeSink = null;
    this.nextIndex = 0;
    sink.reset();
    this.activeSink = sink;
    this.activePosition = position;
    const frame = sink.restore ? findKeyframe(this.keyframes, position) : null;
    if (frame && sink.restore) {
      this.nextIndex = frame.nextIndex;
      const restore = sink.restore.bind(sink);
      this.guardedWrite((done) => restore(frame, done));
      return;
    }
    this.pump();
  }

  private guardedWrite(start: (done: () => void) => void): void {
    const token = ++this.writeToken;
    this.writePending = true;
    try {
      start(() => this.finishWrite(token));
    } catch (error) {
      if (token === this.writeToken) {
        this.writePending = false;
        this.activeSink = null;
      }
      throw error;
    }
  }

  private finishWrite(token: number): void {
    if (this.disposed || token !== this.writeToken) return;
    this.writePending = false;
    if (this.pendingSeek) {
      this.activeSink = null;
      this.scheduleSeek();
      return;
    }
    // The next step may resize the terminal; xterm's resize flushes its write queue and, inside a write callback,
    // parses the chunk that just completed a second time (xterm.js#6246). Continue once the write loop has returned.
    queueMicrotask(() => this.pump());
  }

  private pump(): void {
    if (this.disposed || this.pumping || this.writePending) return;
    if (this.pendingSeek) {
      this.scheduleSeek();
      return;
    }
    const sink = this.activeSink;
    if (!sink) return;
    this.pumping = true;
    try {
      while (
        !this.disposed &&
        !this.pendingSeek &&
        !this.writePending &&
        this.activeSink === sink
      ) {
        const cutoff = this.startTs + this.activePosition;
        const chunk = this.chunks[this.nextIndex];
        if (!chunk || chunk.ts > cutoff) {
          this.idleSink = sink;
          this.idlePosition = this.activePosition;
          this.activeSink = null;
          this.onSettled?.();
          break;
        }
        if (chunk.kind === TRANSCRIPT_KIND_OUTPUT) {
          this.write(sink, this.takeOutputBatch(cutoff));
          continue;
        }
        this.nextIndex += 1;
        if (chunk.kind === TRANSCRIPT_KIND_RESIZE) {
          const size = parseResizePayload(chunk.data);
          if (size) sink.resize(size.cols, size.rows);
        }
      }
    } finally {
      this.pumping = false;
    }
    if (this.pendingSeek && !this.writePending) this.scheduleSeek();
  }

  private takeOutputBatch(cutoff: number): Uint8Array {
    const parts: Uint8Array[] = [];
    let total = 0;
    while (this.nextIndex < this.chunks.length) {
      const chunk = this.chunks[this.nextIndex];
      if (
        chunk.ts > cutoff ||
        chunk.kind !== TRANSCRIPT_KIND_OUTPUT ||
        (total > 0 && total + chunk.data.byteLength > REPLAY_WRITE_BATCH_BYTES)
      ) {
        break;
      }
      parts.push(chunk.data);
      total += chunk.data.byteLength;
      this.nextIndex += 1;
    }
    if (parts.length === 1) return parts[0];
    const data = new Uint8Array(total);
    let offset = 0;
    for (const part of parts) {
      data.set(part, offset);
      offset += part.byteLength;
    }
    return data;
  }

  private write(sink: ReplaySink, data: Uint8Array): void {
    this.guardedWrite((done) => sink.write(data, done));
  }
}

export function formatReplayClock(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
}

import { decodeBase64 } from "./transcriptText";

export const TRANSCRIPT_KIND_OUTPUT = 0;
export const TRANSCRIPT_KIND_INPUT = 1;
export const TRANSCRIPT_KIND_RESIZE = 2;

// Playback speeds the replay view offers; the website demo offers the same.
export const REPLAY_SPEEDS = [1, 2, 5, 10] as const;
export const REPLAY_TICK_MS = 50;
// With idle skipping on, a pause longer than this plays as this long: a terminal left alone for an hour (or recorded
// with gaps while it was in the background) does not replay as an hour of nothing.
export const REPLAY_IDLE_CAP_MS = 2000;
export const REPLAY_THEME = {
  background: "#101217",
  foreground: "#c6cbd6",
  cursor: "#101217",
  selectionBackground: "#2c3e5d",
};

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
// include, and positionMs is the replay position of the last chunk it includes, so a seek target at or after
// positionMs can start from the frame. The engine sets positionMs from nextIndex when the frame is added.
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
  // positions[i] is chunk i's place on the replay timeline: its offset from the first chunk with every pause longer
  // than idleCapMs shortened to idleCapMs.
  private positions: number[] = [];
  private idleCapMs: number;
  private startTs: number;
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

  constructor(chunks: ReplayChunk[], options?: { idleCapMs?: number }) {
    this.chunks = [...chunks].sort((a, b) => a.seq - b.seq);
    this.idleCapMs = options?.idleCapMs ?? Infinity;
    this.startTs = this.chunks.length > 0 ? this.chunks[0].ts : 0;
    this.computePositions(0);
  }

  private computePositions(from: number): void {
    this.positions.length = from;
    for (let i = from; i < this.chunks.length; i++) {
      if (i === 0) {
        this.positions.push(0);
        continue;
      }
      const pause = Math.max(0, this.chunks[i].ts - this.chunks[i - 1].ts);
      this.positions.push(this.positions[i - 1] + Math.min(pause, this.idleCapMs));
    }
  }

  durationMs(): number {
    return this.positions.length > 0 ? this.positions[this.positions.length - 1] : 0;
  }

  // setIdleCap changes how long pauses play and returns where the replay at position now sits on the new timeline;
  // the keyframes move with it.
  setIdleCap(idleCapMs: number, position: number): number {
    const index = this.lastIndexAt(position);
    const offset = index >= 0 ? position - this.positions[index] : 0;
    this.idleCapMs = idleCapMs;
    this.computePositions(0);
    for (const frame of this.keyframes) frame.positionMs = this.positions[frame.nextIndex - 1] ?? 0;
    if (index < 0) return 0;
    const next = this.positions[index + 1] ?? this.positions[index];
    return Math.min(this.positions[index] + offset, next);
  }

  // lastIndexAt returns the last chunk at or before position, or -1.
  private lastIndexAt(position: number): number {
    let low = 0;
    let high = this.positions.length - 1;
    let found = -1;
    while (low <= high) {
      const mid = (low + high) >> 1;
      if (this.positions[mid] <= position) {
        found = mid;
        low = mid + 1;
      } else {
        high = mid - 1;
      }
    }
    return found;
  }

  // positionOfTs returns the position of the first chunk recorded at or after the absolute timestamp ts.
  positionOfTs(ts: number): number {
    const index = this.chunks.findIndex((chunk) => chunk.ts >= ts);
    if (index < 0) return this.durationMs();
    return this.positions[index];
  }

  // indexOfSeq returns the index of the first chunk whose sequence is at least seq.
  indexOfSeq(seq: number): number {
    let low = 0;
    let high = this.chunks.length;
    while (low < high) {
      const mid = (low + high) >> 1;
      if (this.chunks[mid].seq < seq) low = mid + 1;
      else high = mid;
    }
    return low;
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
    const appendedFrom = this.chunks.length;
    if (ordered[0].seq <= lastSeq) {
      // An out-of-order page invalidates every index into the chunk list.
      reordered = true;
      this.chunks = [...this.chunks, ...ordered].sort((a, b) => a.seq - b.seq);
      this.keyframes = [];
    } else {
      for (const chunk of ordered) this.chunks.push(chunk);
    }
    if (wasEmpty || reordered) this.startTs = this.chunks[0].ts;
    this.computePositions(reordered ? 0 : appendedFrom);
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

  // Keyframes arrive in ascending order (from the server, the cache or the background builder); each one is placed
  // on the timeline by the chunks it includes, and only frames past the last one and within the loaded chunks count.
  addKeyframes(frames: ReplayKeyframe[]): void {
    if (this.disposed) return;
    for (const frame of frames) {
      const last = this.keyframes[this.keyframes.length - 1];
      if (frame.nextIndex < 1 || frame.nextIndex > this.chunks.length) continue;
      if (last && frame.nextIndex <= last.nextIndex) continue;
      this.keyframes.push({ ...frame, positionMs: this.positions[frame.nextIndex - 1] });
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
        const chunk = this.chunks[this.nextIndex];
        if (!chunk || this.positions[this.nextIndex] > this.activePosition) {
          this.idleSink = sink;
          this.idlePosition = this.activePosition;
          this.activeSink = null;
          this.onSettled?.();
          break;
        }
        if (chunk.kind === TRANSCRIPT_KIND_OUTPUT) {
          this.write(sink, this.takeOutputBatch(this.activePosition));
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

  private takeOutputBatch(position: number): Uint8Array {
    const parts: Uint8Array[] = [];
    let total = 0;
    while (this.nextIndex < this.chunks.length) {
      const chunk = this.chunks[this.nextIndex];
      if (
        this.positions[this.nextIndex] > position ||
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

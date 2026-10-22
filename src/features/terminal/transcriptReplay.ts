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

export class TranscriptReplay {
  private readonly chunks: ReplayChunk[];
  private readonly startTs: number;
  private readonly endTs: number;
  private nextIndex = 0;
  private activeSink: ReplaySink | null = null;
  private activePosition = 0;
  private pendingSeek: PendingSeek | null = null;
  private writePending = false;
  private writeToken = 0;
  private seekScheduled = false;
  private pumping = false;
  private disposed = false;

  constructor(chunks: ReplayChunk[]) {
    this.chunks = [...chunks].sort((a, b) => a.seq - b.seq);
    this.startTs = this.chunks.length > 0 ? this.chunks[0].ts : 0;
    this.endTs = this.chunks.length > 0 ? this.chunks[this.chunks.length - 1].ts : this.startTs;
  }

  durationMs(): number {
    return this.endTs - this.startTs;
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
    this.pump();
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
          this.activeSink = null;
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
    const token = ++this.writeToken;
    this.writePending = true;
    try {
      sink.write(data, () => {
        if (this.disposed || token !== this.writeToken) return;
        this.writePending = false;
        if (this.pendingSeek) {
          this.activeSink = null;
          this.scheduleSeek();
          return;
        }
        this.pump();
      });
    } catch (error) {
      if (token === this.writeToken) {
        this.writePending = false;
        this.activeSink = null;
      }
      throw error;
    }
  }
}

export function formatReplayClock(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
}

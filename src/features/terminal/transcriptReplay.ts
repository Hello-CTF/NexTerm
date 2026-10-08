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
  write(data: Uint8Array): void;
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

export class TranscriptReplay {
  private readonly chunks: ReplayChunk[];
  private readonly startTs: number;
  private readonly endTs: number;
  private nextIndex = 0;

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
    sink.reset();
    this.nextIndex = 0;
    this.applyThrough(sink, position);
    return position;
  }

  playStep(sink: ReplaySink, positionMs: number): number {
    const position = this.clampPosition(positionMs);
    this.applyThrough(sink, position);
    return position;
  }

  private applyThrough(sink: ReplaySink, positionMs: number) {
    const cutoff = this.startTs + positionMs;
    while (this.nextIndex < this.chunks.length && this.chunks[this.nextIndex].ts <= cutoff) {
      this.apply(sink, this.chunks[this.nextIndex]);
      this.nextIndex += 1;
    }
  }

  private apply(sink: ReplaySink, chunk: ReplayChunk) {
    if (chunk.kind === TRANSCRIPT_KIND_OUTPUT) {
      sink.write(chunk.data);
      return;
    }
    if (chunk.kind === TRANSCRIPT_KIND_RESIZE) {
      const size = parseResizePayload(chunk.data);
      if (size) sink.resize(size.cols, size.rows);
    }
  }
}

export function formatReplayClock(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
}

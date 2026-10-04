const ANSI_PATTERN =
  /[\u001B\u009B][[\]()#;?]*(?:(?:(?:(?:[a-zA-Z\d]*(?:;[a-zA-Z\d]*)*)?\u0007)|(?:(?:\d{1,4}(?:;\d{0,4})*)?[\dA-PR-TZcf-nq-uy=><~])))/g;

export function decodeBase64(data: string): Uint8Array {
  const binary = atob(data);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index++) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes;
}

export function chunkToText(dataBase64: string): string {
  const bytes = decodeBase64(dataBase64);
  return new TextDecoder("utf-8", { fatal: false }).decode(bytes);
}

export function stripAnsi(text: string): string {
  return text.replace(ANSI_PATTERN, "");
}

export interface TranscriptDecoder {
  push(dataBase64: string): string;
  flush(): string;
}

export function createTranscriptDecoder(): TranscriptDecoder {
  let tail: Uint8Array<ArrayBufferLike> = new Uint8Array(0);
  const advance = (bytes: Uint8Array): string => {
    const raw = new Uint8Array(tail.length + bytes.length);
    raw.set(tail, 0);
    raw.set(bytes, tail.length);
    const [visible, rest] = visibleBytes(raw);
    tail = rest;
    return visible;
  };
  return {
    push(dataBase64: string): string {
      return advance(decodeBase64(dataBase64));
    },
    flush(): string {
      const raw = tail;
      tail = new Uint8Array(0);
      const [visible] = visibleBytes(raw);
      return visible;
    },
  };
}

function visibleBytes(raw: Uint8Array): [string, Uint8Array<ArrayBufferLike>] {
  let visible = "";
  let index = 0;
  while (index < raw.length) {
    const current = raw[index];
    if (current === 0x1b) {
      const result = escapeSequence(raw, index);
      if (!result.complete) return [visible, raw.slice(index)];
      index = result.end;
      continue;
    }
    if (current === 0x9b) {
      const result = csiSequence(raw, index + 1);
      if (!result.complete) return [visible, raw.slice(index)];
      index = result.end;
      continue;
    }
    if (current === 0x90 || current === 0x98 || current === 0x9e || current === 0x9f) {
      const result = controlString(raw, index + 1);
      if (!result.complete) return [visible, raw.slice(index)];
      index = result.end;
      continue;
    }
    const decoded = decodeRune(raw, index);
    if (decoded.size === 0) {
      return [visible, raw.slice(index)];
    }
    if (decoded.rune === 0xfffd && decoded.size === 1) {
      visible += String.fromCharCode(current);
      index++;
      continue;
    }
    if (decoded.rune >= 0x80 && decoded.rune <= 0x9f) {
      if (decoded.rune === 0x9b) {
        const result = csiSequence(raw, index + decoded.size);
        if (!result.complete) return [visible, raw.slice(index)];
        index = result.end;
        continue;
      }
      if (decoded.rune === 0x90 || decoded.rune === 0x98 || decoded.rune === 0x9e || decoded.rune === 0x9f) {
        const result = controlString(raw, index + decoded.size);
        if (!result.complete) return [visible, raw.slice(index)];
        index = result.end;
        continue;
      }
      index += decoded.size;
      continue;
    }
    visible += String.fromCodePoint(decoded.rune);
    index += decoded.size;
  }
  return [visible, new Uint8Array(0)];
}

function escapeSequence(raw: Uint8Array, start: number): { end: number; complete: boolean } {
  if (start + 1 >= raw.length) return { end: raw.length, complete: false };
  const kind = raw[start + 1];
  if (kind === 0x5b) return csiSequence(raw, start + 2);
  if (kind === 0x5d) return oscSequence(raw, start + 2);
  if (kind === 0x50 || kind === 0x58 || kind === 0x5e || kind === 0x5f) return controlString(raw, start + 2);
  let index = start + 1;
  while (index < raw.length && raw[index] >= 0x20 && raw[index] <= 0x2f) index++;
  if (index >= raw.length) return { end: raw.length, complete: false };
  if (raw[index] >= 0x30 && raw[index] <= 0x7e) return { end: index + 1, complete: true };
  return { end: index, complete: true };
}

function csiSequence(raw: Uint8Array, start: number): { end: number; complete: boolean } {
  for (let index = start; index < raw.length; index++) {
    const current = raw[index];
    if (current >= 0x40 && current <= 0x7e) return { end: index + 1, complete: true };
    if (current < 0x20 || current > 0x3f) return { end: index, complete: true };
  }
  return { end: raw.length, complete: false };
}

function oscSequence(raw: Uint8Array, start: number): { end: number; complete: boolean } {
  for (let index = start; index < raw.length; index++) {
    if (raw[index] === 0x07) return { end: index + 1, complete: true };
    if (raw[index] === 0x1b && index + 1 < raw.length && raw[index + 1] === 0x5c) {
      return { end: index + 2, complete: true };
    }
  }
  return { end: raw.length, complete: false };
}

function controlString(raw: Uint8Array, start: number): { end: number; complete: boolean } {
  for (let index = start; index < raw.length; index++) {
    if (raw[index] === 0x1b && index + 1 < raw.length && raw[index + 1] === 0x5c) {
      return { end: index + 2, complete: true };
    }
    if (raw[index] === 0x9c) return { end: index + 1, complete: true };
  }
  return { end: raw.length, complete: false };
}

function decodeRune(raw: Uint8Array, index: number): { rune: number; size: number } {
  const b0 = raw[index];
  if (b0 < 0x80) return { rune: b0, size: 1 };
  if (b0 >= 0xc2 && b0 <= 0xdf) {
    if (index + 1 >= raw.length) return { rune: 0, size: 0 };
    const b1 = raw[index + 1];
    if ((b1 & 0xc0) !== 0x80) return { rune: 0xfffd, size: 1 };
    return { rune: ((b0 & 0x1f) << 6) | (b1 & 0x3f), size: 2 };
  }
  if (b0 >= 0xe0 && b0 <= 0xef) {
    if (index + 2 >= raw.length) return { rune: 0, size: 0 };
    const b1 = raw[index + 1];
    const b2 = raw[index + 2];
    if ((b1 & 0xc0) !== 0x80 || (b2 & 0xc0) !== 0x80) return { rune: 0xfffd, size: 1 };
    const rune = ((b0 & 0x0f) << 12) | ((b1 & 0x3f) << 6) | (b2 & 0x3f);
    if (rune < 0x800 || (rune >= 0xd800 && rune <= 0xdfff)) return { rune: 0xfffd, size: 1 };
    return { rune, size: 3 };
  }
  if (b0 >= 0xf0 && b0 <= 0xf4) {
    if (index + 3 >= raw.length) return { rune: 0, size: 0 };
    const b1 = raw[index + 1];
    const b2 = raw[index + 2];
    const b3 = raw[index + 3];
    if ((b1 & 0xc0) !== 0x80 || (b2 & 0xc0) !== 0x80 || (b3 & 0xc0) !== 0x80) {
      return { rune: 0xfffd, size: 1 };
    }
    const rune = ((b0 & 0x07) << 18) | ((b1 & 0x3f) << 12) | ((b2 & 0x3f) << 6) | (b3 & 0x3f);
    if (rune < 0x10000 || rune > 0x10ffff) return { rune: 0xfffd, size: 1 };
    return { rune, size: 4 };
  }
  return { rune: 0xfffd, size: 1 };
}

export function formatTranscriptBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function formatTranscriptDuration(startedAt: number, endedAt: number | null): string {
  const end = endedAt ?? Date.now();
  const seconds = Math.max(0, Math.round((end - startedAt) / 1000));
  if (seconds < 60) return `${seconds} 秒`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时`;
  return `${Math.floor(hours / 24)} 天`;
}

export function formatTranscriptTime(ts: number): string {
  const date = new Date(ts);
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

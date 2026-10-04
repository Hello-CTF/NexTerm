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
  const decoder = new TextDecoder("utf-8", { fatal: false });
  let tail = "";
  const strip = (text: string): string => {
    let visible = "";
    let index = 0;
    while (index < text.length) {
      const code = text.charCodeAt(index);
      if (code === 0x1b) {
        const result = scanEscape(text, index);
        if (!result.complete) {
          tail = text.slice(index);
          return visible;
        }
        index = result.end;
        continue;
      }
      if (code === 0x9b) {
        const result = scanCsi(text, index + 1);
        if (!result.complete) {
          tail = text.slice(index);
          return visible;
        }
        index = result.end;
        continue;
      }
      visible += text[index];
      index++;
    }
    return visible;
  };
  return {
    push(dataBase64: string): string {
      const bytes = decodeBase64(dataBase64);
      const text = tail + decoder.decode(bytes, { stream: true });
      tail = "";
      return strip(text);
    },
    flush(): string {
      const text = tail + decoder.decode();
      tail = "";
      return strip(text);
    },
  };
}

function scanCsi(text: string, start: number): { end: number; complete: boolean } {
  for (let index = start; index < text.length; index++) {
    const code = text.charCodeAt(index);
    if (code >= 0x40 && code <= 0x7e) return { end: index + 1, complete: true };
    if (code < 0x20 || code > 0x3f) return { end: index, complete: true };
  }
  return { end: text.length, complete: false };
}

function scanEscape(text: string, start: number): { end: number; complete: boolean } {
  if (start + 1 >= text.length) return { end: text.length, complete: false };
  const kind = text[start + 1];
  if (kind === "[") {
    return scanCsi(text, start + 2);
  }
  if (kind === "]") {
    for (let index = start + 2; index < text.length; index++) {
      if (text.charCodeAt(index) === 0x07) return { end: index + 1, complete: true };
      if (text.charCodeAt(index) === 0x1b && index + 1 < text.length && text[index + 1] === "\\") {
        return { end: index + 2, complete: true };
      }
    }
    return { end: text.length, complete: false };
  }
  let index = start + 1;
  while (index < text.length) {
    const code = text.charCodeAt(index);
    if (code >= 0x20 && code <= 0x2f) {
      index++;
      continue;
    }
    if (code >= 0x30 && code <= 0x7e) return { end: index + 1, complete: true };
    return { end: index, complete: true };
  }
  return { end: text.length, complete: false };
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

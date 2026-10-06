export type OscStreamEvent =
  | { kind: "notification"; body: string }
  | { kind: "clipboard"; payload: string }
  | { kind: "command"; phase: "A" | "B" | "C" | "D"; exitCode: number | null };

export interface OscStreamFilter {
  push: (bytes: Uint8Array) => Uint8Array;
  flush: () => Uint8Array;
}

const ESC = 0x1b;
const BEL = 0x07;
const ST_FINAL = 0x5c;
const SEMICOLON = 0x3b;
const MAX_SEQUENCE_BYTES = 1 << 20;

const utf8Decoder = new TextDecoder("utf-8", { fatal: false });

function indexOfByte(data: Uint8Array, byte: number, from: number): number {
  for (let i = from; i < data.length; i++) {
    if (data[i] === byte) return i;
  }
  return -1;
}

function asciiOf(data: Uint8Array): string {
  let out = "";
  for (let i = 0; i < data.length; i++) out += String.fromCharCode(data[i]);
  return out;
}

interface SequenceEnd {
  end: number;
  contentEnd: number;
}

function findSequenceEnd(data: Uint8Array, from: number): SequenceEnd | null {
  for (let j = from; j < data.length; j++) {
    const b = data[j];
    if (b === BEL) return { end: j + 1, contentEnd: j };
    if (b === ESC) {
      if (j + 1 >= data.length) return null;
      if (data[j + 1] === ST_FINAL) return { end: j + 2, contentEnd: j };
    }
  }
  return null;
}

function joinParts(parts: Uint8Array[], length: number): Uint8Array {
  if (parts.length === 0) return new Uint8Array(0);
  if (parts.length === 1) return parts[0];
  const out = new Uint8Array(length);
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.length;
  }
  return out;
}

// parseOSC133 parses an OSC 133 payload ("A", "B", "C", "D" or "D;<exit>").
// It returns null for anything else so the sequence stays in the stream.
// A trailing empty parameter ("D;") means no exit code, matching the backend
// tracker.
function parseOSC133(payload: Uint8Array): OscStreamEvent | null {
  if (payload.length < 1) return null;
  const letter = String.fromCharCode(payload[0]);
  if (letter !== "A" && letter !== "B" && letter !== "C" && letter !== "D") return null;
  if (payload.length === 1) return { kind: "command", phase: letter, exitCode: null };
  if (payload[1] !== SEMICOLON) return null;
  if (payload.length === 2) return { kind: "command", phase: letter, exitCode: null };
  let exitCode = 0;
  for (let i = 2; i < payload.length; i++) {
    const digit = payload[i] - 0x30;
    if (digit < 0 || digit > 9) return null;
    exitCode = exitCode * 10 + digit;
    if (exitCode > 0x7fffffff) return null;
  }
  return { kind: "command", phase: letter, exitCode };
}

export function createOscStreamFilter(emit: (event: OscStreamEvent) => void): OscStreamFilter {
  let pending: Uint8Array | null = null;

  const push = (bytes: Uint8Array): Uint8Array => {
    let data = bytes;
    if (pending) {
      const merged = new Uint8Array(pending.length + bytes.length);
      merged.set(pending, 0);
      merged.set(bytes, pending.length);
      data = merged;
      pending = null;
    }
    const parts: Uint8Array[] = [];
    let partsLength = 0;
    const pass = (from: number, to: number) => {
      if (to <= from) return;
      parts.push(data.subarray(from, to));
      partsLength += to - from;
    };

    let segmentStart = 0;
    let stop = data.length;
    let i = 0;
    while (i < data.length) {
      const esc = indexOfByte(data, ESC, i);
      if (esc < 0) break;
      if (esc + 1 >= data.length) {
        pending = data.slice(esc);
        stop = esc;
        break;
      }
      if (data[esc + 1] !== 0x5d) {
        i = esc + 1;
        continue;
      }
      const found = findSequenceEnd(data, esc + 2);
      if (!found) {
        if (data.length - esc > MAX_SEQUENCE_BYTES) break;
        pending = data.slice(esc);
        stop = esc;
        break;
      }
      const content = data.subarray(esc + 2, found.contentEnd);
      const semi = indexOfByte(content, SEMICOLON, 0);
      let code = Number.NaN;
      if (semi > 0) {
        let digits = 0;
        while (digits < semi && content[digits] >= 0x30 && content[digits] <= 0x39) digits++;
        if (digits === semi) code = Number(asciiOf(content.subarray(0, digits)));
      }
      if (code === 9 || code === 52) {
        const payload = utf8Decoder.decode(content.subarray(semi + 1));
        emit(code === 9 ? { kind: "notification", body: payload } : { kind: "clipboard", payload });
        pass(segmentStart, esc);
        segmentStart = found.end;
      } else if (code === 133) {
        const event = parseOSC133(content.subarray(semi + 1));
        if (event) {
          emit(event);
          pass(segmentStart, esc);
          segmentStart = found.end;
        }
      }
      i = found.end;
    }
    pass(segmentStart, stop);
    return joinParts(parts, partsLength);
  };

  const flush = (): Uint8Array => {
    const rest = pending ?? new Uint8Array(0);
    pending = null;
    return rest;
  };

  return { push, flush };
}

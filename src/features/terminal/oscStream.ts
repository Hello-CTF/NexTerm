export type OscStreamEvent =
  | { kind: "notification"; body: string }
  | { kind: "clipboard"; payload: string }
  | { kind: "command"; phase: "A" | "B" | "C" | "D"; exitCode: number | null };

// OscStreamSegment is one ordered piece of a pushed chunk: either terminal
// text (with intercepted sequences stripped) or an event raised by a stripped
// sequence. Segments preserve stream order so callers can apply text and
// events to the terminal in the order the shell produced them.
export type OscStreamSegment =
  | { kind: "text"; data: Uint8Array }
  | { kind: "event"; event: OscStreamEvent };

export interface OscStreamFilter {
  push: (bytes: Uint8Array) => OscStreamSegment[];
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

export function createOscStreamFilter(): OscStreamFilter {
  let pendingChunks: Uint8Array[] = [];
  let pendingBytes = 0;

  const appendPending = (bytes: Uint8Array) => {
    if (bytes.length === 0) return;
    pendingChunks.push(bytes.slice());
    pendingBytes += bytes.length;
  };

  const joinPending = (bytes: Uint8Array): Uint8Array => {
    const joined = new Uint8Array(pendingBytes + bytes.length);
    let offset = 0;
    for (const chunk of pendingChunks) {
      joined.set(chunk, offset);
      offset += chunk.length;
    }
    joined.set(bytes, offset);
    pendingChunks = [];
    pendingBytes = 0;
    return joined;
  };

  const push = (bytes: Uint8Array): OscStreamSegment[] => {
    if (bytes.length === 0 && pendingBytes > 0) return [];
    let data = bytes;
    let firstSequenceEnd: SequenceEnd | null = null;
    if (pendingBytes === 1) {
      data = joinPending(bytes);
    } else if (pendingBytes > 1) {
      const previousBytes = pendingBytes;
      const lastChunk = pendingChunks[pendingChunks.length - 1];
      const lastByte = lastChunk[lastChunk.length - 1];
      if (lastByte === ESC && bytes[0] === ST_FINAL) {
        firstSequenceEnd = { end: previousBytes + 1, contentEnd: previousBytes - 1 };
      } else {
        const found = findSequenceEnd(bytes, 0);
        if (found) {
          firstSequenceEnd = {
            end: previousBytes + found.end,
            contentEnd: previousBytes + found.contentEnd,
          };
        }
      }
      if (!firstSequenceEnd) {
        if (previousBytes + bytes.length > MAX_SEQUENCE_BYTES) {
          return [{ kind: "text", data: joinPending(bytes) }];
        }
        appendPending(bytes);
        return [];
      }
      data = joinPending(bytes);
    }
    const segments: OscStreamSegment[] = [];
    let segmentStart = 0;
    let stop = data.length;
    let i = 0;
    while (i < data.length) {
      const esc = indexOfByte(data, ESC, i);
      if (esc < 0) break;
      if (esc + 1 >= data.length) {
        pendingChunks = [data.slice(esc)];
        pendingBytes = data.length - esc;
        stop = esc;
        break;
      }
      if (data[esc + 1] !== 0x5d) {
        i = esc + 1;
        continue;
      }
      const found = firstSequenceEnd ?? findSequenceEnd(data, esc + 2);
      firstSequenceEnd = null;
      if (!found) {
        if (data.length - esc > MAX_SEQUENCE_BYTES) break;
        pendingChunks = [data.slice(esc)];
        pendingBytes = data.length - esc;
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
      let event: OscStreamEvent | null = null;
      if (code === 9 || code === 52) {
        const payload = utf8Decoder.decode(content.subarray(semi + 1));
        event = code === 9 ? { kind: "notification", body: payload } : { kind: "clipboard", payload };
      } else if (code === 133) {
        event = parseOSC133(content.subarray(semi + 1));
      }
      if (event) {
        if (esc > segmentStart) segments.push({ kind: "text", data: data.subarray(segmentStart, esc) });
        segments.push({ kind: "event", event });
        segmentStart = found.end;
      }
      i = found.end;
    }
    if (stop > segmentStart) segments.push({ kind: "text", data: data.subarray(segmentStart, stop) });
    return segments;
  };

  const flush = (): Uint8Array => {
    if (pendingBytes === 0) return new Uint8Array(0);
    return joinPending(new Uint8Array(0));
  };

  return { push, flush };
}

// createOscSegmentWriter returns a segment consumer that keeps OSC events in
// stream order relative to terminal text: an event dispatches after the text
// immediately preceding it has been parsed and before any later text is
// parsed, so command-block markers, end lines and echo reads see the buffer
// the shell produced. Each text write owns the events that directly follow
// it; an event arriving while no write is in flight dispatches immediately
// (its preceding text is already parsed). State spans chunks.
export function createOscSegmentWriter(
  term: { write: (data: string | Uint8Array, callback?: () => void) => void },
  dispatch: (event: OscStreamEvent) => void,
  onWritten?: () => void,
): (segments: OscStreamSegment[]) => void {
  let inFlight = 0;
  let currentTail: OscStreamEvent[] | null = null;
  return (segments) => {
    for (const segment of segments) {
      if (segment.kind === "event") {
        if (inFlight === 0 || currentTail === null) {
          dispatch(segment.event);
        } else {
          currentTail.push(segment.event);
        }
        continue;
      }
      const tail: OscStreamEvent[] = [];
      currentTail = tail;
      inFlight += 1;
      term.write(segment.data, () => {
        inFlight -= 1;
        for (const event of tail) dispatch(event);
        onWritten?.();
      });
    }
  };
}

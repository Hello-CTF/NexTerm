// The prefix trz / tsz print to ask the terminal for a transfer
// (::TRZSZ:TRANSFER:<S|R|D>:<version>:<id>). Live output is scanned for it
// before trzsz.js is loaded, so a terminal that never runs trz / tsz pays one
// native indexOf per chunk and nothing else.
export const TRZSZ_MAGIC = "::TRZSZ:TRANSFER:";

const MAGIC_BYTES = new TextEncoder().encode(TRZSZ_MAGIC);
const COLON = MAGIC_BYTES[0];

// Bytes kept from the end of a chunk so a request split across two chunks is
// still recognized: the prefix plus the mode, version and id that follow it.
export const TRZSZ_TAIL_BYTES = MAGIC_BYTES.length + 48;

export function indexOfTrzszMagic(bytes: Uint8Array, from = 0): number {
  let index = bytes.indexOf(COLON, from);
  while (index >= 0 && index + MAGIC_BYTES.length <= bytes.length) {
    let matched = 1;
    while (matched < MAGIC_BYTES.length && bytes[index + matched] === MAGIC_BYTES[matched]) matched += 1;
    if (matched === MAGIC_BYTES.length) return index;
    index = bytes.indexOf(COLON, index + 1);
  }
  return -1;
}

// Returns the bytes to hand to trzsz.js when `chunk`, alone or joined to the
// tail of the previous chunk, carries a transfer request; null otherwise.
export function trzszRequestIn(tail: Uint8Array, chunk: Uint8Array): Uint8Array | null {
  if (indexOfTrzszMagic(chunk) >= 0) return chunk.slice();
  if (tail.length === 0) return null;
  const head = chunk.subarray(0, Math.min(chunk.length, MAGIC_BYTES.length - 1));
  const seam = new Uint8Array(tail.length + head.length);
  seam.set(tail);
  seam.set(head, tail.length);
  // Only a prefix that starts in the tail and ends in this chunk is new: one
  // wholly inside the tail was already seen with the previous chunk.
  const start = tail.length - (MAGIC_BYTES.length - 1);
  const index = indexOfTrzszMagic(seam, Math.max(0, start));
  if (index < 0 || index + MAGIC_BYTES.length <= tail.length) return null;
  const joined = new Uint8Array(tail.length + chunk.length);
  joined.set(tail);
  joined.set(chunk, tail.length);
  return joined.subarray(index).slice();
}

export function nextTrzszTail(previous: Uint8Array, chunk: Uint8Array): Uint8Array {
  if (chunk.length >= TRZSZ_TAIL_BYTES) return chunk.slice(chunk.length - TRZSZ_TAIL_BYTES);
  const keep = Math.min(previous.length, TRZSZ_TAIL_BYTES - chunk.length);
  const out = new Uint8Array(keep + chunk.length);
  out.set(previous.subarray(previous.length - keep));
  out.set(chunk, keep);
  return out;
}

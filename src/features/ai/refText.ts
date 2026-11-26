// Helpers shared by every place that turns something on screen into an AI reference chip.

// The backend quotes at most this many characters of a text reference (aicontext.RefScreenRunes).
export const REF_TEXT_RUNES = 4000;
const REF_PREVIEW_RUNES = 24;

// singleLine turns line breaks and control characters into spaces and collapses whitespace so a reference label stays on one line.
export function singleLine(text: string): string {
  return text.replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g, " ").replace(/\s+/g, " ").trim();
}

// clipRefText keeps the head or the tail of text within the backend's quote budget, counted in code points.
export function clipRefText(text: string, keep: "head" | "tail", limit = REF_TEXT_RUNES): { text: string; clipped: boolean } {
  const runes = Array.from(text);
  if (runes.length <= limit) return { text, clipped: false };
  return { text: (keep === "tail" ? runes.slice(-limit) : runes.slice(0, limit)).join(""), clipped: true };
}

// refPreview is a short one-line preview of text for a chip label.
export function refPreview(text: string, limit = REF_PREVIEW_RUNES): string {
  const runes = Array.from(singleLine(text));
  return runes.slice(0, limit).join("") + (runes.length > limit ? "…" : "");
}

// newRefId makes an id for a reference that must not merge with another one queued from the same source.
export function newRefId(prefix: string): string {
  return `${prefix}:${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;
}

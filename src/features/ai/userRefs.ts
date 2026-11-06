const USER_REF_HEADER = "[引用]";

// singleLine keeps a reference label on one line so the prefix stays parseable.
function singleLine(value: string): string {
  return value.replace(/[\u0000-\u001f\u007f]+/g, " ").replace(/\s+/g, " ").trim();
}

// composeUserMessage prefixes the message with a readable list of its @ references; the stored
// user message keeps this prefix, so the chat can show the references after a reload.
export function composeUserMessage(text: string, refList: { label: string; detail: string }[]): string {
  if (refList.length === 0) return text;
  const list = refList.map((r) => `- ${singleLine(r.label)}（${singleLine(r.detail)}）`).join("\n");
  return `${USER_REF_HEADER}\n${list}\n\n${text}`;
}

// splitUserRefs is the inverse of composeUserMessage: text without a well-formed prefix is all body.
export function splitUserRefs(text: string): { refs: string[]; body: string } {
  const lines = text.split("\n");
  if (lines[0] !== USER_REF_HEADER) return { refs: [], body: text };
  let index = 1;
  const refs: string[] = [];
  while (index < lines.length && lines[index].startsWith("- ")) {
    refs.push(lines[index].slice(2));
    index++;
  }
  if (refs.length === 0 || lines[index] !== "") return { refs: [], body: text };
  return { refs, body: lines.slice(index + 1).join("\n") };
}

// currentAtQuery returns the text typed after a trailing "@" that starts a word, or null when the
// input is not in the middle of an @ reference (no "@", or whitespace typed after it).
export function currentAtQuery(input: string): string | null {
  const at = input.lastIndexOf("@");
  if (at < 0) return null;
  if (at > 0 && !/\s/.test(input[at - 1])) return null;
  const query = input.slice(at + 1);
  return /\s/.test(query) ? null : query;
}

// The backend renders at most this many references per message (aicontext.RefMaxCount).
export const MAX_MESSAGE_REFS = 4;

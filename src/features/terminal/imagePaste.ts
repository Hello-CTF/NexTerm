export { bytesToBase64 } from "../../ui/base64";

// pureImageFiles returns the file list only when every item is an image and there is at least one (all-or-nothing).
// A mixed payload (images + non-image files) returns an empty array and the whole event goes back to default handling: no image subset is extracted and nothing is dropped.
export function pureImageFiles(data: DataTransfer | null): File[] {
  if (!data) return [];
  const entries: { type: string; file: File | null }[] = [];
  if (data.items && data.items.length > 0) {
    for (const item of data.items) {
      if (item.kind !== "file") continue;
      entries.push({ type: item.type, file: item.getAsFile() });
    }
  } else {
    for (const file of data.files ?? []) {
      entries.push({ type: file.type, file });
    }
  }
  if (entries.length === 0) return [];
  if (entries.some((entry) => !entry.type.startsWith("image/") || entry.file === null)) return [];
  return entries.map((entry) => entry.file as File);
}

// clipboardHasPlainText reports whether a mixed clipboard has usable plain text:
// when text exists, the paste goes back to the terminal's default handling so plain text and bracketed paste are never lost.
export function clipboardHasPlainText(data: DataTransfer | null): boolean {
  if (!data) return false;
  try {
    return data.getData("text/plain").length > 0;
  } catch {
    return false;
  }
}

// markdownFileLink builds a Markdown link to insert at the terminal cursor (a normal link, not an image embed).
// Backslashes and square brackets are escaped in the text; a destination containing whitespace or parentheses is wrapped in angle brackets (a CommonMark-legal target).
export function markdownFileLink(name: string, url: string): string {
  const text = (name || "file").replace(/([\\\[\]])/g, "\\$1");
  const destination = /[\s()]/.test(url) ? `<${url}>` : url;
  return `[${text}](${destination})`;
}

// pasteFileExtension extracts a safe lowercase extension (without the dot) from the file name or MIME, defaulting to png.
export function pasteFileExtension(file: File): string {
  const fromName = /\.([A-Za-z0-9]{1,8})$/.exec(file.name)?.[1];
  if (fromName) return fromName.toLowerCase();
  const fromType = /^image\/([A-Za-z0-9]{1,8})$/.exec(file.type)?.[1];
  return fromType ? fromType.toLowerCase() : "png";
}

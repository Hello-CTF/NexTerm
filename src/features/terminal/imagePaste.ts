export { bytesToBase64 } from "../../ui/base64";

// MAX_PASTE_FILE_BYTES is the per-file size limit for files pasted or dropped into the terminal.
export const MAX_PASTE_FILE_BYTES = 20 * 1024 * 1024;

// pasteUploadFiles returns the real File objects of a paste/drop event that should be uploaded to the host, or [] when
// the event must go back to default text handling.
// - No File objects: a plain text paste, untouched.
// - Only images alongside plain text: the text wins. Spreadsheets and web pages put a rendered image next to the text
//   they copy, and dropping that text would break an ordinary paste.
// - Any non-image File (for example copied in a file manager, which also exposes its name or path as text): the files
//   win and the accompanying text is ignored.
export function pasteUploadFiles(data: DataTransfer | null): File[] {
  if (!data) return [];
  const files: File[] = [];
  if (data.items && data.items.length > 0) {
    for (const item of data.items) {
      if (item.kind !== "file") continue;
      const file = item.getAsFile();
      if (file) files.push(file);
    }
  } else {
    for (const file of data.files ?? []) files.push(file);
  }
  if (files.length === 0) return [];
  if (files.every((file) => file.type.startsWith("image/")) && clipboardHasPlainText(data)) return [];
  return files;
}

// splitOversizedFiles separates files within the size limit from those above it.
export function splitOversizedFiles(files: File[]): { accepted: File[]; skipped: File[] } {
  const accepted: File[] = [];
  const skipped: File[] = [];
  for (const file of files) (file.size > MAX_PASTE_FILE_BYTES ? skipped : accepted).push(file);
  return { accepted, skipped };
}

const MAX_REMOTE_NAME_CHARS = 100;

// sanitizePasteFileName makes a client-supplied file name safe as one remote path segment: no separators or control
// characters, no leading dots, bounded length with the extension kept, never empty.
export function sanitizePasteFileName(name: string): string {
  let clean = name
    .replace(/[\\/\u0000-\u001f\u007f]/g, "_")
    .replace(/^[.\s]+/, "")
    .replace(/\s+$/, "");
  if (clean.length > MAX_REMOTE_NAME_CHARS) {
    const ext = /\.[A-Za-z0-9]{1,8}$/.exec(clean)?.[0] ?? "";
    clean = clean.slice(0, MAX_REMOTE_NAME_CHARS - ext.length) + ext;
  }
  return clean || "file";
}

// uniquePasteFileNames sanitises every name and appends -2, -3, ... before the extension to duplicates, so files from
// one paste never overwrite each other.
export function uniquePasteFileNames(names: string[]): string[] {
  const used = new Set<string>();
  return names.map((raw) => {
    const clean = sanitizePasteFileName(raw);
    const dot = clean.lastIndexOf(".");
    const stem = dot > 0 ? clean.slice(0, dot) : clean;
    const ext = dot > 0 ? clean.slice(dot) : "";
    let candidate = clean;
    for (let n = 2; used.has(candidate.toLowerCase()); n += 1) candidate = `${stem}-${n}${ext}`;
    used.add(candidate.toLowerCase());
    return candidate;
  });
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

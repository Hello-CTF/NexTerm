export { bytesToBase64 } from "../../ui/base64";

// MAX_PASTE_FILE_BYTES is the per-file size limit for files pasted or dropped into the terminal.
export const MAX_PASTE_FILE_BYTES = 20 * 1024 * 1024;

// PasteUploadSelection is what a paste/drop event contributes to the upload: the real File objects plus a visible
// description of every entry that cannot be uploaded (folders, items the browser would not hand over).
export interface PasteUploadSelection {
  files: File[];
  skipped: string[];
}

interface DirectoryProbeItem {
  webkitGetAsEntry?: () => { isDirectory?: boolean; name?: string } | null;
}

// pasteUploadFiles splits a paste/drop event into files to upload to the host and skipped entries. Both lists are
// empty when the event must go back to default text handling.
// - No file items: a plain text paste, untouched.
// - Only image items alongside plain text: the text wins. Spreadsheets and web pages put a rendered image next to the
//   text they copy, and dropping that text would break an ordinary paste.
// - Any other file item (for example copied in a file manager, which also exposes its name or path as text): the files
//   win and the accompanying text is ignored. Folders and items whose File is unavailable are reported as skipped.
export function pasteUploadFiles(data: DataTransfer | null): PasteUploadSelection {
  const none: PasteUploadSelection = { files: [], skipped: [] };
  if (!data) return none;
  const files: File[] = [];
  const skipped: string[] = [];
  let fileItems = 0;
  let onlyImages = true;
  if (data.items && data.items.length > 0) {
    for (const item of data.items) {
      if (item.kind !== "file") continue;
      fileItems += 1;
      if (!item.type.startsWith("image/")) onlyImages = false;
      let entry: { isDirectory?: boolean; name?: string } | null = null;
      try {
        entry = (item as DataTransferItem & DirectoryProbeItem).webkitGetAsEntry?.() ?? null;
      } catch {
        entry = null;
      }
      if (entry?.isDirectory) {
        onlyImages = false;
        skipped.push(`文件夹「${displayFileName(entry.name ?? "")}」`);
        continue;
      }
      const file = item.getAsFile();
      if (file) files.push(file);
      else skipped.push("无法读取的项目");
    }
  } else {
    for (const file of data.files ?? []) {
      fileItems += 1;
      if (!file.type.startsWith("image/")) onlyImages = false;
      files.push(file);
    }
  }
  if (fileItems === 0) return none;
  if (onlyImages && clipboardHasPlainText(data)) return none;
  return { files, skipped };
}

// splitOversizedFiles separates files within the size limit from those above it.
export function splitOversizedFiles(files: File[]): { accepted: File[]; skipped: File[] } {
  const accepted: File[] = [];
  const skipped: File[] = [];
  for (const file of files) (file.size > MAX_PASTE_FILE_BYTES ? skipped : accepted).push(file);
  return { accepted, skipped };
}

// MAX_REMOTE_NAME_BYTES bounds the remote file name in UTF-8 bytes. Chunked uploads stage next to the target as
// ".<name>.nexterm-<id>.part-<n>", which adds about 60 bytes, and common file systems cap a name at 255 bytes.
export const MAX_REMOTE_NAME_BYTES = 150;

// Characters that must never reach a terminal or a Markdown link: C0 and C1 controls, DEL, line/paragraph separators
// and format characters (bidi overrides and isolates, zero-width characters, BOM and the rest of Unicode Cf).
const UNSAFE_TEXT = /[\p{Cc}\p{Cf}\u2028\u2029]/gu;

const UNSAFE_DESTINATION = /[\p{Cc}\p{Cf}\u2028\u2029<>]/gu;

// TERMINAL_CONTROL matches any byte a shell or the bracketed-paste wrapper would interpret as control input.
export const TERMINAL_CONTROL = /[\u0000-\u001f\u007f-\u009f]/;

const utf8 = new TextEncoder();

function utf8Length(text: string): number {
  return utf8.encode(text).length;
}

// truncateUtf8 cuts text to at most maxBytes UTF-8 bytes without splitting a code point.
export function truncateUtf8(text: string, maxBytes: number): string {
  if (utf8Length(text) <= maxBytes) return text;
  let out = "";
  let used = 0;
  for (const codePoint of text) {
    const size = utf8Length(codePoint);
    if (used + size > maxBytes) break;
    out += codePoint;
    used += size;
  }
  return out;
}

function splitExtension(name: string): { stem: string; ext: string } {
  const ext = /\.[A-Za-z0-9]{1,8}$/.exec(name)?.[0] ?? "";
  if (!ext || ext.length === name.length) return { stem: name, ext: "" };
  return { stem: name.slice(0, -ext.length), ext };
}

function fitName(stem: string, suffix: string, ext: string): string {
  const budget = MAX_REMOTE_NAME_BYTES - utf8Length(suffix) - utf8Length(ext);
  return `${truncateUtf8(stem, Math.max(budget, 1))}${suffix}${ext}`;
}

// SAFE_NAME_CHAR is the conservative set a remote file name may keep: letters, digits, space, dot, underscore, plus and
// hyphen. Everything else, including shell metacharacters ($ ` ; | & * ? ' " ! and brackets), control and format
// characters and path separators, becomes "_", so the name stays inert when the link lands at a shell prompt.
const UNSAFE_NAME_CHARS = /[^\p{L}\p{N} ._+-]+/gu;

// sanitizePasteFileName makes a client-supplied file name safe as one remote path segment and as Markdown/terminal
// text: only SAFE_NAME_CHAR characters, repeated "_" collapsed, no leading dots, at most MAX_REMOTE_NAME_BYTES UTF-8
// bytes with the extension kept, never empty.
export function sanitizePasteFileName(name: string): string {
  const clean = name
    .replace(UNSAFE_NAME_CHARS, "_")
    .replace(/_{2,}/g, "_")
    .replace(/^[.\s]+/, "")
    .replace(/\s+$/, "");
  if (!clean) return "file";
  const { stem, ext } = splitExtension(clean);
  return fitName(stem, "", ext);
}

// uniquePasteFileNames sanitises every name and appends -2, -3, ... before the extension to duplicates, so files from
// one paste never overwrite each other. The suffix stays within the byte limit.
export function uniquePasteFileNames(names: string[]): string[] {
  const used = new Set<string>();
  return names.map((raw) => {
    const clean = sanitizePasteFileName(raw);
    const { stem, ext } = splitExtension(clean);
    let candidate = clean;
    for (let n = 2; used.has(candidate.toLowerCase()); n += 1) candidate = fitName(stem, `-${n}`, ext);
    used.add(candidate.toLowerCase());
    return candidate;
  });
}

// displayFileName renders an untrusted name for status messages without control or format characters.
export function displayFileName(name: string): string {
  return name.replace(UNSAFE_TEXT, "") || "未命名";
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
// The text is the sanitised file name, so it matches the stored name and carries no shell metacharacters or control
// characters. In the destination, control and format characters and "<" ">" are percent-encoded, and a destination
// containing whitespace or parentheses is wrapped in angle brackets (a CommonMark-legal target).
export function markdownFileLink(name: string, url: string): string {
  const text = sanitizePasteFileName(name);
  const target = url.replace(UNSAFE_DESTINATION, (c) => encodeURIComponent(c));
  const destination = /[\s()]/.test(target) ? `<${target}>` : target;
  return `[${text}](${destination})`;
}

// pasteFileExtension extracts a safe lowercase extension (without the dot) from the file name or MIME, defaulting to png.
export function pasteFileExtension(file: File): string {
  const fromName = /\.([A-Za-z0-9]{1,8})$/.exec(file.name)?.[1];
  if (fromName) return fromName.toLowerCase();
  const fromType = /^image\/([A-Za-z0-9]{1,8})$/.exec(file.type)?.[1];
  return fromType ? fromType.toLowerCase() : "png";
}

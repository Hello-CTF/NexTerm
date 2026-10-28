// Asset option keys for files pasted or dropped into a terminal (validated the same way by internal/store).
// PASTE_UPLOAD_DIR_KEY is the directory as the file channel (SFTP) sees it; PASTE_TERMINAL_DIR_KEY is the same
// directory as the shell sees it, for chrooted SFTP accounts whose view differs from the shell's.
export const PASTE_UPLOAD_DIR_KEY = "pasteUploadDir";
export const PASTE_TERMINAL_DIR_KEY = "pasteTerminalDir";

// DEFAULT_PASTE_DIR is used when the asset configures no upload directory.
export const DEFAULT_PASTE_DIR = "/tmp";

const UNSAFE_PATH_CHARS = /[\p{Cc}\p{Cf}\u2028\u2029]/u;

export type PasteDirResult = { ok: true; value: string } | { ok: false; error: string };

// normalizePasteDir accepts an absolute POSIX directory and returns it without duplicate or trailing slashes and
// "." segments. Control and format characters and ".." segments are rejected. Empty input means "not configured".
export function normalizePasteDir(raw: string): PasteDirResult {
  if (UNSAFE_PATH_CHARS.test(raw)) return { ok: false, error: "路径不能包含控制字符" };
  const value = raw.replace(/^ +| +$/g, "");
  if (!value) return { ok: true, value: "" };
  if (!value.startsWith("/")) return { ok: false, error: "路径必须是以 / 开头的绝对路径" };
  const segments: string[] = [];
  for (const segment of value.split("/")) {
    if (segment === "" || segment === ".") continue;
    if (segment === "..") return { ok: false, error: "路径不能包含 .. 段" };
    segments.push(segment);
  }
  return { ok: true, value: `/${segments.join("/")}` };
}

export interface PasteDirSettings {
  // uploadDir is where the file channel creates the per-paste directory.
  uploadDir: string;
  // terminalDir is the same directory as the shell sees it; links inserted into the terminal start with it.
  terminalDir: string;
  // custom reports whether the asset configured its own upload directory.
  custom: boolean;
}

// DEFAULT_PASTE_DIRS keeps the original behaviour: upload to /tmp and link /tmp.
export const DEFAULT_PASTE_DIRS: PasteDirSettings = { uploadDir: DEFAULT_PASTE_DIR, terminalDir: DEFAULT_PASTE_DIR, custom: false };

export type PasteDirSettingsResult = { ok: true; settings: PasteDirSettings } | { ok: false; error: string };

// validatePasteDirOptions normalises the two form values. A terminal path requires an upload directory.
export function validatePasteDirOptions(
  uploadRaw: string,
  terminalRaw: string,
): { ok: true; uploadDir: string; terminalDir: string } | { ok: false; error: string } {
  const upload = normalizePasteDir(uploadRaw);
  if (!upload.ok) return { ok: false, error: `粘贴上传目录无效：${upload.error}` };
  const terminal = normalizePasteDir(terminalRaw);
  if (!terminal.ok) return { ok: false, error: `终端中的对应路径无效：${terminal.error}` };
  if (terminal.value && !upload.value) {
    return { ok: false, error: "填写终端中的对应路径时，必须同时填写粘贴上传目录" };
  }
  return { ok: true, uploadDir: upload.value, terminalDir: terminal.value };
}

// pasteDirSettings reads the paste directories from asset options; missing options mean the default /tmp.
export function pasteDirSettings(options: Record<string, unknown> | null | undefined): PasteDirSettingsResult {
  const read = (key: string): string | null => {
    const value = options?.[key];
    if (value === undefined || value === null) return "";
    return typeof value === "string" ? value : null;
  };
  const uploadRaw = read(PASTE_UPLOAD_DIR_KEY);
  const terminalRaw = read(PASTE_TERMINAL_DIR_KEY);
  if (uploadRaw === null || terminalRaw === null) return { ok: false, error: "粘贴上传目录必须是字符串" };
  const result = validatePasteDirOptions(uploadRaw, terminalRaw);
  if (!result.ok) return result;
  if (!result.uploadDir) return { ok: true, settings: DEFAULT_PASTE_DIRS };
  return {
    ok: true,
    settings: { uploadDir: result.uploadDir, terminalDir: result.terminalDir || result.uploadDir, custom: true },
  };
}

// joinPastePath appends a relative name to a normalised absolute directory.
export function joinPastePath(dir: string, name: string): string {
  return dir === "/" ? `/${name}` : `${dir}/${name}`;
}

// terminalPastePath maps a path under the upload directory to the path the shell sees.
export function terminalPastePath(settings: PasteDirSettings, uploadPath: string): string {
  const prefix = settings.uploadDir === "/" ? "" : settings.uploadDir;
  if (uploadPath !== settings.uploadDir && !uploadPath.startsWith(`${prefix}/`)) return uploadPath;
  const rest = uploadPath.slice(prefix.length).replace(/^\//, "");
  return rest ? joinPastePath(settings.terminalDir, rest) : settings.terminalDir;
}

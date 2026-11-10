import { t } from "../../../i18n";
import { formatBytes } from "../../../ui/format";
import { bytesToBase64 } from "../../../ui/base64";

// trzsz.js running in a webview picks and saves files only through the File
// System Access API (window.showOpenFilePicker / showDirectoryPicker, with
// the ids "trzsz_upload" and "trzsz_download"). Those calls are answered here
// instead: every transfer first asks the user in the app, then opens the
// desktop's native dialogs or, in a browser, the browser's own picker (the
// File System Access API where the browser has it, a file input and blob
// downloads where it does not). Calls with any other id reach the browser's
// own implementation unchanged.

const UPLOAD_ID = "trzsz_upload";
const DOWNLOAD_ID = "trzsz_download";

// A file saved without the File System Access API is held in memory until it
// is complete (desktop IPC writes and browser blob downloads), so it is capped.
export const TRZSZ_BUFFERED_SAVE_MAX_BYTES = 128 << 20;
// The desktop's local range read returns at most 4 MiB per call.
const DESKTOP_READ_CHUNK_BYTES = 4 << 20;
// Limits for one download (tsz) written through the app: a host decides what
// it sends, so the number of files, their total size and the depth are bounded.
export const TRZSZ_MAX_FILES = 10000;
export const TRZSZ_MAX_TOTAL_BYTES = 2 * 1024 * 1024 * 1024;
export const TRZSZ_MAX_DEPTH = 32;
// Free-name search for a file that already exists locally.
const MAX_NAME_ATTEMPTS = 100;

export interface TrzszTransferLimits {
  files: number;
  totalBytes: number;
  depth: number;
}

// Bumped when a transfer is cancelled or torn down: a file still being
// received at that moment is incomplete and must not be saved as a result.
let abortEpoch = 0;
export function markTrzszTransferAborted(): void {
  abortEpoch += 1;
}

export interface TrzszRequestContext {
  // The host the request came from, when it can be told apart from other terminals.
  host: string | null;
}

export interface TrzszFileDeps {
  desktop: boolean;
  windows: boolean;
  ask: (message: string, options: { title: string; kind?: "info" | "warning" }) => Promise<boolean>;
  notify: (kind: "info" | "error", message: string) => void;
  context: () => TrzszRequestContext;
  // Desktop: native dialogs and local file commands.
  openFiles: () => Promise<string[]>;
  openFolder: () => Promise<string | null>;
  listFolder: (path: string) => Promise<{
    name: string;
    files: { relativePath: string; localPath: string; size: number; mtimeMs: number }[];
    emptyDirs: string[];
  }>;
  readRange: (path: string, offset: number, maxBytes: number) => Promise<{ size: number; contentBase64: string }>;
  writeFile: (path: string, contentBase64: string) => Promise<{ exists?: boolean }>;
  // Desktop: whether a local path is already taken, checked before content is sent.
  pathExists: (path: string) => Promise<boolean>;
  // Called when the user declines a request (the terminal is inferred from the running transfer).
  declined?: () => void;
  limits?: Partial<TrzszTransferLimits>;
  // Browser: file inputs and blob downloads.
  pickBrowserFiles: (directory: boolean) => Promise<File[] | null>;
  saveBlob: (name: string, blob: Blob) => Promise<boolean>;
}

type PickerOptions = { id?: string; multiple?: boolean; mode?: string; startIn?: string } | undefined;
type Picker = (options?: PickerOptions) => Promise<unknown>;
interface PickerWindow {
  showOpenFilePicker?: Picker;
  showDirectoryPicker?: Picker;
}

let installed: { win: PickerWindow; open?: Picker; directory?: Picker } | null = null;

export function installTrzszPickers(win: PickerWindow, deps: TrzszFileDeps): void {
  if (installed?.win === win) return;
  const open = typeof win.showOpenFilePicker === "function" ? win.showOpenFilePicker.bind(win) : undefined;
  const directory = typeof win.showDirectoryPicker === "function" ? win.showDirectoryPicker.bind(win) : undefined;
  installed = { win, open, directory };
  const unsupported = (name: string) => Promise.reject(new TypeError(`${name} is not supported`));
  win.showOpenFilePicker = (options) =>
    options?.id === UPLOAD_ID
      ? chooseUploadFiles(deps, open, options)
      : open
        ? open(options)
        : unsupported("showOpenFilePicker");
  win.showDirectoryPicker = (options) => {
    if (options?.id === UPLOAD_ID) return chooseUploadFolder(deps, directory, options);
    if (options?.id === DOWNLOAD_ID) return chooseSaveFolder(deps, directory, options);
    return directory ? directory(options) : unsupported("showDirectoryPicker");
  };
}

// Restores the browser's own pickers; for tests.
export function uninstallTrzszPickers(): void {
  if (!installed) return;
  const { win, open, directory } = installed;
  if (open) win.showOpenFilePicker = open;
  else delete win.showOpenFilePicker;
  if (directory) win.showDirectoryPicker = directory;
  else delete win.showDirectoryPicker;
  installed = null;
}

function abort(): never {
  throw new DOMException(t("已取消"), "AbortError");
}

function hostOf(deps: TrzszFileDeps): string {
  return deps.context().host || t("远端主机");
}

// One confirmation is shown at a time and at most one more waits behind it;
// further requests are refused so a host cannot stack dialogs.
let queued = 0;
let turn: Promise<void> = Promise.resolve();

async function confirmRequest(deps: TrzszFileDeps, message: string, title: string): Promise<void> {
  if (queued >= 2) abort();
  queued += 1;
  const previous = turn;
  let release!: () => void;
  turn = new Promise<void>((resolve) => {
    release = resolve;
  });
  let ok = false;
  try {
    await previous;
    ok = await deps.ask(message, { title, kind: "warning" });
  } finally {
    queued -= 1;
    release();
  }
  if (!ok) {
    deps.declined?.();
    abort();
  }
}

interface FileLike {
  name: string;
  size: number;
  lastModified: number;
  slice: (start?: number, end?: number) => { arrayBuffer: () => Promise<ArrayBuffer> };
}

function decodeBase64(text: string): Uint8Array {
  const binary = atob(text);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) out[i] = binary.charCodeAt(i);
  return out;
}

// A desktop file read lazily through the local range-read command, so a large
// upload never has to fit in memory.
function desktopFile(deps: TrzszFileDeps, path: string, name: string, size: number, lastModified: number): FileLike {
  return {
    name,
    size,
    lastModified,
    slice: (start = 0, end = size) => ({
      arrayBuffer: async () => {
        const from = Math.max(0, Math.min(start, size));
        const to = Math.max(from, Math.min(end, size));
        const out = new Uint8Array(to - from);
        let offset = from;
        while (offset < to) {
          const read = await deps.readRange(path, offset, Math.min(DESKTOP_READ_CHUNK_BYTES, to - offset));
          const bytes = decodeBase64(read.contentBase64);
          if (bytes.length === 0) throw new Error(t("读取 {name} 时文件变短了", { name }));
          out.set(bytes.subarray(0, to - offset), offset - from);
          offset += bytes.length;
        }
        return out.buffer;
      },
    }),
  };
}

function baseName(path: string): string {
  const index = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return index < 0 ? path : path.slice(index + 1);
}

function fileHandle(name: string, getFile: () => Promise<FileLike | File>) {
  return { kind: "file" as const, name, getFile };
}

interface UploadDirectory {
  kind: "directory";
  name: string;
  entries: Map<string, UploadDirectory | ReturnType<typeof fileHandle>>;
  values: () => AsyncIterableIterator<UploadDirectory | ReturnType<typeof fileHandle>>;
  getFileHandle: (name: string) => Promise<ReturnType<typeof fileHandle>>;
  getDirectoryHandle: (name: string) => Promise<UploadDirectory>;
}

function uploadDirectory(name: string): UploadDirectory {
  const entries: UploadDirectory["entries"] = new Map();
  const dir: UploadDirectory = {
    kind: "directory",
    name,
    entries,
    values: async function* () {
      for (const entry of entries.values()) yield entry;
    } as UploadDirectory["values"],
    getFileHandle: async (child) => {
      const entry = entries.get(child);
      if (entry?.kind !== "file") throw new DOMException(child, "NotFoundError");
      return entry;
    },
    getDirectoryHandle: async (child) => {
      const entry = entries.get(child);
      if (entry?.kind !== "directory") throw new DOMException(child, "NotFoundError");
      return entry;
    },
  };
  return dir;
}

function addUploadPath(root: UploadDirectory, segments: string[], file: (() => Promise<FileLike | File>) | null): void {
  let dir = root;
  const last = file ? segments.length - 1 : segments.length;
  for (let i = 0; i < last; i += 1) {
    const existing = dir.entries.get(segments[i]);
    if (existing?.kind === "directory") {
      dir = existing;
    } else {
      const next = uploadDirectory(segments[i]);
      dir.entries.set(segments[i], next);
      dir = next;
    }
  }
  if (file) dir.entries.set(segments[last], fileHandle(segments[last], file));
}

async function chooseUploadFiles(deps: TrzszFileDeps, native: Picker | undefined, options: PickerOptions): Promise<unknown> {
  await confirmRequest(
    deps,
    t("{host}请求上传文件（trz）。选择要发送到该主机的本机文件？", { host: hostOf(deps) }),
    t("trzsz 上传"),
  );
  if (deps.desktop) {
    const paths = await deps.openFiles();
    if (paths.length === 0) abort();
    const handles = [];
    for (const path of paths) {
      // A one-byte read reports the file size.
      const { size } = await deps.readRange(path, 0, 1);
      const name = baseName(path);
      handles.push(fileHandle(name, async () => desktopFile(deps, path, name, size, Date.now())));
    }
    return handles;
  }
  if (native) return native(options);
  const files = await deps.pickBrowserFiles(false);
  if (!files || files.length === 0) abort();
  return files.map((file) => fileHandle(file.name, async () => file));
}

async function chooseUploadFolder(deps: TrzszFileDeps, native: Picker | undefined, options: PickerOptions): Promise<unknown> {
  await confirmRequest(
    deps,
    t("{host}请求上传文件夹（trz -d）。选择要发送到该主机的本机文件夹？", { host: hostOf(deps) }),
    t("trzsz 上传"),
  );
  if (deps.desktop) {
    const path = await deps.openFolder();
    if (!path) abort();
    const listing = await deps.listFolder(path);
    const root = uploadDirectory(listing.name);
    for (const file of listing.files) {
      const segments = file.relativePath.split("/");
      const name = segments[segments.length - 1];
      addUploadPath(root, segments, async () => desktopFile(deps, file.localPath, name, file.size, file.mtimeMs));
    }
    for (const dir of listing.emptyDirs) addUploadPath(root, dir.split("/"), null);
    return root;
  }
  if (native) return native(options);
  const files = await deps.pickBrowserFiles(true);
  if (!files || files.length === 0) abort();
  const rootName = (files[0].webkitRelativePath || files[0].name).split("/")[0];
  const root = uploadDirectory(rootName);
  for (const file of files) {
    const segments = (file.webkitRelativePath || file.name).split("/");
    addUploadPath(root, segments.length > 1 ? segments.slice(1) : segments, async () => file);
  }
  return root;
}

const WINDOWS_RESERVED = /[<>:"|?*]/;
const WINDOWS_DEVICE = /^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\..*)?$/i;
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/;

// Names come from the remote host: anything that could leave the chosen
// folder or address something other than a plain file in it is refused.
export function safeEntryName(name: string, windows: boolean): boolean {
  if (!name || name === "." || name === ".." || name.length > 255) return false;
  if (name.includes("/") || name.includes("\\") || CONTROL_CHARS.test(name)) return false;
  if (windows && (WINDOWS_RESERVED.test(name) || /[. ]$/.test(name) || WINDOWS_DEVICE.test(name))) return false;
  return true;
}

function refuseName(name: string): never {
  throw new Error(t("远端发送的文件名不安全，已拒绝：{name}", { name: JSON.stringify(name) }));
}

interface SaveSink {
  // Writes one completed file; returns the name it was saved under.
  save: (segments: string[], data: Blob | Uint8Array) => Promise<string>;
  allowDirectories: boolean;
}

interface TransferBudget {
  limits: TrzszTransferLimits;
  files: number;
  bytes: number;
  exceeded: boolean;
}

function newBudget(deps: TrzszFileDeps): TransferBudget {
  return {
    limits: {
      files: deps.limits?.files ?? TRZSZ_MAX_FILES,
      totalBytes: deps.limits?.totalBytes ?? TRZSZ_MAX_TOTAL_BYTES,
      depth: deps.limits?.depth ?? TRZSZ_MAX_DEPTH,
    },
    files: 0,
    bytes: 0,
    exceeded: false,
  };
}

// Aborts the transfer (the error reaches trzsz.js and the terminal) and tells the user.
function overBudget(deps: TrzszFileDeps, budget: TransferBudget, message: string): never {
  budget.exceeded = true;
  deps.notify("error", message);
  throw new Error(message);
}

function saveDirectory(deps: TrzszFileDeps, sink: SaveSink, segments: string[], budget: TransferBudget): unknown {
  const name = segments[segments.length - 1];
  return {
    kind: "directory",
    name,
    values: async function* () {
      // Existing names are not listed: a name already taken is resolved
      // when the file is written, which never replaces it.
    },
    getFileHandle: async (child: string) => {
      if (!safeEntryName(child, deps.windows)) refuseName(child);
      if (budget.exceeded) throw new Error(t("传输已超出限制"));
      budget.files += 1;
      if (budget.files > budget.limits.files) {
        overBudget(deps, budget, t("文件数超过 {count} 个，已停止接收", { count: budget.limits.files }));
      }
      const path = [...segments, child];
      return {
        kind: "file",
        name: child,
        createWritable: async () => bufferedWriter(deps, sink, path, budget),
      };
    },
    getDirectoryHandle: async (child: string) => {
      if (!safeEntryName(child, deps.windows)) refuseName(child);
      if (!sink.allowDirectories) throw new Error(t("此浏览器无法保存文件夹，请改用桌面端或支持文件系统访问的浏览器"));
      if (budget.exceeded) throw new Error(t("传输已超出限制"));
      if (segments.length > budget.limits.depth) {
        overBudget(deps, budget, t("文件夹层级超过 {count} 层，已停止接收", { count: budget.limits.depth }));
      }
      return saveDirectory(deps, sink, [...segments, child], budget);
    },
  };
}

function bufferedWriter(deps: TrzszFileDeps, sink: SaveSink, segments: string[], budget: TransferBudget) {
  const parts: Uint8Array[] = [];
  let total = 0;
  let closed = false;
  // Set when this file cannot be completed: it is never saved.
  let failed = false;
  const epoch = abortEpoch;
  const wanted = segments[segments.length - 1];
  return {
    write: async (data: Uint8Array | ArrayBuffer | Blob) => {
      if (failed) throw new Error(t("文件 {name} 已停止接收", { name: wanted }));
      const bytes = data instanceof Blob ? new Uint8Array(await data.arrayBuffer()) : new Uint8Array(data instanceof ArrayBuffer ? data : data.slice());
      total += bytes.length;
      budget.bytes += bytes.length;
      if (total > TRZSZ_BUFFERED_SAVE_MAX_BYTES) {
        failed = true;
        parts.length = 0;
        throw new Error(t("文件 {name} 超过 {size}，已停止接收", {
          name: wanted,
          size: formatBytes(TRZSZ_BUFFERED_SAVE_MAX_BYTES),
        }));
      }
      if (budget.bytes > budget.limits.totalBytes) {
        failed = true;
        parts.length = 0;
        overBudget(deps, budget, t("本次传输超过 {size}，已停止接收", { size: formatBytes(budget.limits.totalBytes) }));
      }
      parts.push(bytes);
    },
    // trzsz.js does not wait for close, so a failed save is reported here.
    // It also closes a file whose transfer was cancelled; that one is dropped.
    close: async () => {
      if (closed) return;
      closed = true;
      if (failed) return;
      if (epoch !== abortEpoch) {
        parts.length = 0;
        deps.notify("info", t("传输已取消，未保存不完整的文件 {name}", { name: wanted }));
        return;
      }
      const joined = new Uint8Array(total);
      let offset = 0;
      for (const part of parts) {
        joined.set(part, offset);
        offset += part.length;
      }
      parts.length = 0;
      try {
        const saved = await sink.save(segments, joined);
        if (saved !== wanted) deps.notify("info", t("本机已有 {name}，已另存为 {saved}", { name: wanted, saved }));
      } catch (error) {
        deps.notify("error", t("保存 {name} 失败：{value}", { name: wanted, value: error instanceof Error ? error.message : String(error) }));
      }
    },
  };
}

function desktopSink(deps: TrzszFileDeps, root: string): SaveSink {
  const separator = root.includes("\\") && !root.includes("/") ? "\\" : "/";
  const base = root.endsWith(separator) ? root.slice(0, -1) : root;
  return {
    allowDirectories: true,
    save: async (segments, data) => {
      const bytes = data instanceof Uint8Array ? data : new Uint8Array(await data.arrayBuffer());
      const content = bytesToBase64(bytes);
      const parent = segments.slice(1, -1);
      const wanted = segments[segments.length - 1];
      // Never replace an existing file: take the first free name.0, name.1, ...
      // Free names are probed first so the content is sent once; a write that
      // still reports a collision (a file created meanwhile) moves on.
      for (let attempt = -1; attempt < MAX_NAME_ATTEMPTS; attempt += 1) {
        const name = attempt < 0 ? wanted : `${wanted}.${attempt}`;
        const path = [base, ...parent, name].join(separator);
        if (await deps.pathExists(path)) continue;
        const result = await deps.writeFile(path, content);
        if (!result.exists) return name;
      }
      throw new Error(t("没有可用的文件名"));
    },
  };
}

function browserSink(deps: TrzszFileDeps): SaveSink {
  return {
    allowDirectories: false,
    save: async (segments, data) => {
      const name = segments[segments.length - 1];
      const blob = data instanceof Blob ? data : new Blob([data as Uint8Array<ArrayBuffer>]);
      if (!(await deps.saveBlob(name, blob))) throw new Error(t("已取消保存"));
      return name;
    },
  };
}

async function chooseSaveFolder(deps: TrzszFileDeps, native: Picker | undefined, options: PickerOptions): Promise<unknown> {
  const buffered = deps.desktop || !native;
  const limit = buffered ? t("单个文件最大 {size}。", { size: formatBytes(TRZSZ_BUFFERED_SAVE_MAX_BYTES) }) : "";
  await confirmRequest(
    deps,
    t("{host}请求向本机发送文件（tsz）。选择保存位置？{limit}", { host: hostOf(deps), limit }),
    t("trzsz 下载"),
  );
  if (deps.desktop) {
    const root = await deps.openFolder();
    if (!root) abort();
    return saveDirectory(deps, desktopSink(deps, root), [baseName(root) || root], newBudget(deps));
  }
  if (native) return native(options);
  return saveDirectory(deps, browserSink(deps), [t("下载")], newBudget(deps));
}

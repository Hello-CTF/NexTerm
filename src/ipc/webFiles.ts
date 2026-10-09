
import { WEB, httpUrl } from "./env";
import { authedFetch } from "./serverAuth";

export interface StagedRef {
  id: string;
  path: string;
}

const stagedIds = new Map<string, string>();

const saveHandles = new Map<string, FileHandleLike>();

const CSRF_HEADER = "X-NexTerm-CSRF";

// auth=on 下服务端对 cookie 会话的 POST/DELETE 强制校验 CSRF 头,
// 拒绝形状固定为 403 + { code:"forbidden", message 含 "CSRF" }(与 commands.ts 同一约定)。
function isCsrfRejection(status: number, text: string): boolean {
  if (status !== 403) return false;
  try {
    const body = JSON.parse(text) as { error?: { code?: string; message?: string } } | null;
    return (
      body?.error?.code === "forbidden" &&
      typeof body.error.message === "string" &&
      body.error.message.includes("CSRF")
    );
  } catch {
    return false;
  }
}

// csrfFetch 在 authedFetch 之上携带账号会话 CSRF 头; 精确 CSRF 403 时经 /auth/me
// 刷新并重试同一请求一次。CSRF 拒绝发生在 requireAuth 中间件、未触达 blob handler,
// 重试不会重复暂存/预留/删除; 其他 403 原样返回给调用方, 不重试。
async function csrfFetch(url: string, init: RequestInit = {}): Promise<Response> {
  // 延迟加载 authApi: webFiles 与 authApi 互相引用, 动态加载避免静态环。
  const { authApi, getCsrfToken } = await import("./authApi");
  const send = (csrf: string | null): Promise<Response> =>
    authedFetch(url, {
      ...init,
      headers: {
        ...(init.headers as Record<string, string> | undefined),
        ...(csrf ? { [CSRF_HEADER]: csrf } : {}),
      },
    });
  const res = await send(getCsrfToken());
  if (res.status !== 403) return res;
  const text = await res.clone().text().catch(() => "");
  if (!isCsrfRejection(res.status, text)) return res;
  await authApi.me();
  return send(getCsrfToken());
}

export function isStagedPath(path: string): boolean {
  return stagedIds.has(path);
}

export function browserFilesAvailable(): boolean {
  return WEB;
}

export function pickBrowserFile(accept?: string): Promise<File | null> {
  if (!WEB || typeof document === "undefined") return Promise.resolve(null);
  return new Promise((resolve) => {
    const input = document.createElement("input");
    input.type = "file";
    if (accept) input.accept = accept;
    input.style.position = "fixed";
    input.style.left = "-9999px";
    input.style.width = "1px";
    input.style.height = "1px";
    document.body.appendChild(input);

    let settled = false;
    const finish = (file: File | null) => {
      if (settled) return;
      settled = true;
      window.removeEventListener("focus", onFocus);
      input.remove();
      resolve(file);
    };

    input.addEventListener("change", () => finish(input.files?.[0] ?? null));

    const onFocus = () => {
      window.setTimeout(() => {
        if (!input.files || input.files.length === 0) finish(null);
      }, 800);
    };
    window.addEventListener("focus", onFocus);

    input.click();
  });
}

async function readStagedRef(res: Response, what: string): Promise<StagedRef> {
  if (!res.ok) throw new Error(`${what}失败（HTTP ${res.status}）`);
  const body = (await res.json()) as { data?: Partial<StagedRef> };
  const id = body.data?.id;
  const path = body.data?.path;
  if (!id || !path) throw new Error(`${what}失败：服务端没回 id / path`);
  return { id, path };
}

export async function stageFile(
  file: File,
  opts: { persist?: boolean } = {},
): Promise<StagedRef> {
  const q = new URLSearchParams({ name: file.name || "upload.bin" });
  if (opts.persist) q.set("persist", "1");
  const res = await csrfFetch(httpUrl(`/files/blob?${q.toString()}`), {
    method: "POST",
    body: file,
  });
  const ref = await readStagedRef(res, "暂存");
  if (!opts.persist) stagedIds.set(ref.path, ref.id);
  return ref;
}

async function reserveTarget(name: string): Promise<StagedRef> {
  const q = new URLSearchParams({ name });
  const res = await csrfFetch(httpUrl(`/files/blob/reserve?${q.toString()}`), { method: "POST" });
  const ref = await readStagedRef(res, "预留落点");
  stagedIds.set(ref.path, ref.id);
  return ref;
}

async function askSaveHandle(
  name: string,
): Promise<{ handle: FileHandleLike } | "cancelled" | "unavailable"> {
  const win = window as unknown as {
    showSaveFilePicker?: (o: SavePickerOptionsLike) => Promise<FileHandleLike>;
  };
  if (typeof win.showSaveFilePicker !== "function") return "unavailable";
  try {
    return { handle: await win.showSaveFilePicker({ suggestedName: name }) };
  } catch (e) {
    if (isUserCancel(e)) return "cancelled";
    console.warn("[files] showSaveFilePicker 失败，交付阶段回落到锚点下载", e);
    return "unavailable";
  }
}

export async function requestSaveTarget(name: string): Promise<string | null> {
  const reserved = await reserveTarget(name);
  const picked = await askSaveHandle(name);
  if (picked === "cancelled") {
    await dropStaged(reserved.path);
    return null;
  }
  if (picked !== "unavailable") saveHandles.set(reserved.path, picked.handle);
  return reserved.path;
}

export async function dropStaged(path: string): Promise<void> {
  const id = stagedIds.get(path);
  if (!id) return;
  stagedIds.delete(path);
  saveHandles.delete(path);
  await deleteBlob(id);
}

export async function deliverStaged(
  path: string,
  name?: string,
): Promise<"delivered" | "cancelled" | "not-staged"> {
  const id = stagedIds.get(path);
  if (!id) return "not-staged";
  stagedIds.delete(path);
  const handle = saveHandles.get(path);
  saveHandles.delete(path);
  let cleanup = false;
  try {
    const res = await authedFetch(httpUrl(`/files/blob?id=${encodeURIComponent(id)}`));
    if (!res.ok) throw new Error(`回读暂存内容失败（HTTP ${res.status}）`);
    const blob = await res.blob();
    if (handle) {
      const writable = await handle.createWritable();
      await writable.write(blob);
      await writable.close();
    } else {
      const saved = await saveBrowserBlob(name ?? baseName(path), blob);
      cleanup = true;
      if (!saved) return "cancelled";
    }
    cleanup = true;
    return "delivered";
  } finally {
    if (cleanup) await deleteBlob(id);
  }
}

async function deleteBlob(id: string): Promise<void> {
  await csrfFetch(httpUrl(`/files/blob?id=${encodeURIComponent(id)}`), { method: "DELETE" }).catch(
    () => undefined,
  );
}

interface WritableLike {
  write(data: Blob): Promise<void>;
  close(): Promise<void>;
}
interface FileHandleLike {
  createWritable(): Promise<WritableLike>;
}
interface SavePickerOptionsLike {
  suggestedName?: string;
}

function isUserCancel(e: unknown): boolean {
  return (e as { name?: string } | null)?.name === "AbortError";
}

export async function saveBrowserBlob(name: string, data: Blob): Promise<boolean> {
  const win = window as unknown as {
    showSaveFilePicker?: (o: SavePickerOptionsLike) => Promise<FileHandleLike>;
  };
  if (typeof win.showSaveFilePicker === "function") {
    try {
      const handle = await win.showSaveFilePicker({ suggestedName: name });
      const writable = await handle.createWritable();
      await writable.write(data);
      await writable.close();
      return true;
    } catch (e) {
      if (isUserCancel(e)) return false;
      console.warn("[files] showSaveFilePicker 失败，回落到锚点下载", e);
    }
  }
  anchorDownload(name, data);
  return true;
}

function anchorDownload(name: string, data: Blob): void {
  const url = URL.createObjectURL(data);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.style.display = "none";
  document.body.appendChild(a);
  a.click();
  a.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 30_000);
}

export function baseName(path: string): string {
  const idx = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return idx < 0 ? path : path.slice(idx + 1);
}

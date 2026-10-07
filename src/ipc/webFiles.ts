
import { DEMO, WEB, httpUrl } from "./env";
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
  // 延迟加载 authApi: 部分测试只给 ipc/env mock clientId, 静态链会把 demo/index 拉进来。
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
  if ((!WEB && !DEMO) || typeof document === "undefined") return Promise.resolve(null);
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

export interface ImageLinkHealth {
  publicBaseURLConfigured: boolean;
  maxBytes: number;
  ownerQuotaBytes: number;
  ttlSeconds: number;
}

export interface ImageUploadResult {
  id: string;
  url: string;
  mime: string;
  bytes: number;
  created_at: number;
  expires_at: number;
}

// ImageUploadError 携带 HTTP 状态与后端返回的可读文本, 由 imagePaste 层映射成用户可操作的提示。
export class ImageUploadError extends Error {
  readonly status: number;
  readonly bodyText: string;

  constructor(status: number, bodyText: string) {
    super(`image upload failed (HTTP ${status})`);
    this.name = "ImageUploadError";
    this.status = status;
    this.bodyText = bodyText;
  }
}

// fetchImageService 探测服务端是否提供 M130 图片链接服务(/healthz 的 imageLinks 元数据)。
// 任何失败(网络、非 JSON、字段缺失)都视为"没有兼容的 HTTP 图像服务", 由调用方走本地回退。
export async function fetchImageService(): Promise<ImageLinkHealth | null> {
  try {
    const res = await authedFetch(httpUrl("/healthz"), { method: "GET" });
    if (!res.ok) return null;
    const body = (await res.json()) as { imageLinks?: Partial<ImageLinkHealth> } | null;
    const links = body?.imageLinks;
    if (
      !links ||
      typeof links.maxBytes !== "number" ||
      typeof links.ownerQuotaBytes !== "number" ||
      typeof links.ttlSeconds !== "number"
    ) {
      return null;
    }
    return {
      publicBaseURLConfigured: links.publicBaseURLConfigured === true,
      maxBytes: links.maxBytes,
      ownerQuotaBytes: links.ownerQuotaBytes,
      ttlSeconds: links.ttlSeconds,
    };
  } catch {
    return null;
  }
}

// uploadImage 通过 M130 POST /files/image 上传剪贴板/拖入的图片, 返回限时公开链接。
// 该路由只认账号会话 + CSRF(静态同步令牌不能替代会话): 请求携带 authApi 的会话 CSRF 头;
// 401 时派发 SESSION_EXPIRED_EVENT 交给账号门重新登录(与 authApi.request 同一语义),
// 不做第二套登录流程。失败抛 ImageUploadError(带状态码与后端文本), 不静默回退。
export async function uploadImage(file: File): Promise<ImageUploadResult> {
  // 延迟加载 authApi: 部分测试只给 ipc/env mock clientId, 静态链会把 demo/index 拉进来。
  const { getCsrfToken, SESSION_EXPIRED_EVENT } = await import("./authApi");
  const q = new URLSearchParams({ name: file.name || "pasted-image" });
  const headers: Record<string, string> = {
    "content-type": file.type || "application/octet-stream",
  };
  const csrf = getCsrfToken();
  if (csrf) headers["X-NexTerm-CSRF"] = csrf;
  const res = await fetch(httpUrl(`/files/image?${q.toString()}`), {
    method: "POST",
    headers,
    body: file,
  });
  if (res.status === 401) {
    window.dispatchEvent(new CustomEvent(SESSION_EXPIRED_EVENT));
  }
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new ImageUploadError(res.status, text.slice(0, 300));
  }
  const body = (await res.json()) as Partial<ImageUploadResult> | null;
  if (!body || typeof body.id !== "string" || typeof body.url !== "string") {
    throw new ImageUploadError(res.status, "服务端返回了无效的图片上传响应");
  }
  return {
    id: body.id,
    url: body.url,
    mime: typeof body.mime === "string" ? body.mime : file.type,
    bytes: typeof body.bytes === "number" ? body.bytes : file.size,
    created_at: typeof body.created_at === "number" ? body.created_at : 0,
    expires_at: typeof body.expires_at === "number" ? body.expires_at : 0,
  };
}

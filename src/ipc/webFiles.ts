// 浏览器版的文件出入口：把「浏览器原生文件 API」与「服务端暂存区」拼起来。
//
// # 为什么刻意用原生 API
//
// 懒猫商店有一条硬性要求（《社区激励规则》§8 warning，原文）：
//
// > 如果应用有上传/下载功能，需要接入懒猫网盘的自动拦截文件选择器，未接入无法在懒猫商店上架。
//
// 而平台那份注入脚本（`lzc-file-chooser-inject.js`）**只拦四类浏览器原生入口**：
//
//     showOpenFilePicker()  ·  <input type="file">  ·  showSaveFilePicker()  ·  带 download 的 blob: 链接
//
// 所以这里用这些原生 API 而不是自绘一个选择器 —— 注入会把「网盘文件 / 本地文件」
// 的统一窗口顶上来，用户那边看到的就是官方窗口。**自绘反而是错的**：
// 画一个自己的窗口再"接网盘"，注入拦不到，商店那条要求也就不成立。
//
// 官方还给了另一条路：npm `@lazycatcloud/lzc-file-pickers`。那是个 **Vue 组件**，
// 本前端是 React（为了它嵌一层 Vue 运行时不划算），所以没走；官方文档本身也写着
// 「你愿意改业务前端源码，那可以自己接库，不必走 inject」。
//
// # 与服务端的配合
//
// 浏览器拿到的是**字节**，而传输内核（`fs_upload` / `fs_download` / 打包 / 日志导出）
// 收发的都是**盒子上的路径**。两边的兑换由服务端的暂存区完成
// （见 Rust 侧 `server/blobs.rs` 的模块文档）：
//
//     · 上传：selected file → POST /files/blob → 盒子路径 → fs_upload(...)
//     · 下载：POST /files/blob/reserve → 盒子路径 → fs_download(...) → GET /files/blob → 用户保存
//
// 这样内核一行不用改，浏览器与桌面走的是同一条已经测过的搬运路径。
//
// # 保存落点必须在**用户手势内**就问出来
//
// `showSaveFilePicker()` 要求 transient activation（Chrome 约 5 秒）。而下载路径是
// 「先让内核落盘、再回读交付」，大文件或远端打包动辄几十秒 —— 等字节到手再问落点，
// 激活早就过期，调用只会抛 `SecurityError`，永远走不到那条路。
// 所以 `requestSaveTarget()` 趁手势还在先把**句柄**拿到手，`deliverStaged()` 阶段
// 再往里写字节。句柄拿不到（浏览器没有该 API）时才退到锚点下载，
// 而锚点同样在注入的覆盖范围内，两条路用户都能选「网盘 / 本地」。
//
// # 只在 web 模式下生效
//
// 桌面版有真正的原生对话框（`@tauri-apps/plugin-dialog`），拿到的是磁盘路径，
// 不需要这一层。所有函数在非 web 模式下都返回「不适用」，由调用方回落到桌面分支。

import { WEB, httpUrl } from "./env";

/** 服务端暂存项：id 用于回读 / 删除，path 是它在**盒子**上的路径。 */
export interface StagedRef {
  id: string;
  path: string;
}

/**
 * 盒子路径 → 暂存 id 的登记表。
 *
 * 存在的唯一理由：调用点的既有契约是「`pickSavePath` 返回一个字符串路径，
 * 拿到路径的那个组件负责把内容写进去」。要在**写完之后**才把内容交给浏览器，
 * 就必须能从那个字符串路径反查到 id。为此把 `pick*` 的返回值改成对象的话，
 * 会波及所有调用点（文件树、文件浏览器、终端面板），收益不成比例。
 */
const stagedIds = new Map<string, string>();

/**
 * 用户在**保存窗口里选定的落点句柄**，同样按暂存路径索引。
 *
 * 有它就不必在交付阶段再弹一次窗口（那时已经没有用户激活了）。
 * 浏览器不支持 `showSaveFilePicker()` 时这里没有记录，交付阶段退到锚点下载。
 */
const saveHandles = new Map<string, FileHandleLike>();

/** 这个路径是本次会话登记的暂存落点吗（排查用）。 */
export function isStagedPath(path: string): boolean {
  return stagedIds.has(path);
}

/** 当前是不是浏览器后端模式（这些函数唯一有效的环境）。 */
export function browserFilesAvailable(): boolean {
  return WEB;
}

// ── 选文件 ────────────────────────────────────────────────────────────

/**
 * 用 `<input type="file">` 选一个文件。
 *
 * 用原生 input 而不是 `showOpenFilePicker()`：后者的支持面窄（Chromium 系），
 * 而 input 在所有浏览器都有 —— 且两者都在注入的覆盖范围内。
 *
 * 取消的处理：用户点「取消」时**只有 `change` 不会触发**，Promise 会永远挂着，
 * 界面就卡在「开始上传…」。这里用「窗口重新获得焦点后仍没有文件」当作取消信号，
 * 这是这类原生对话框唯一可用的判别方式。
 */
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
      // 焦点回来得比 change 早是常态，等一拍再判定。
      window.setTimeout(() => {
        if (!input.files || input.files.length === 0) finish(null);
      }, 800);
    };
    window.addEventListener("focus", onFocus);

    input.click();
  });
}

// ── 服务端暂存 ────────────────────────────────────────────────────────

async function readStagedRef(res: Response, what: string): Promise<StagedRef> {
  if (!res.ok) throw new Error(`${what}失败（HTTP ${res.status}）`);
  const body = (await res.json()) as { data?: Partial<StagedRef> };
  const id = body.data?.id;
  const path = body.data?.path;
  if (!id || !path) throw new Error(`${what}失败：服务端没回 id / path`);
  return { id, path };
}

/**
 * 把字节交给服务端暂存，换回一个盒子上的路径。
 *
 * `persist` 为真时落进**不清理**的目录 —— 私钥用。上传的源文件用默认的暂存目录，
 * 用完由 `dropStaged` 删掉（漏删也有巡检兜底）。
 */
export async function stageFile(
  file: File,
  opts: { persist?: boolean } = {},
): Promise<StagedRef> {
  const q = new URLSearchParams({ name: file.name || "upload.bin" });
  if (opts.persist) q.set("persist", "1");
  const res = await fetch(httpUrl(`/files/blob?${q.toString()}`), {
    method: "POST",
    body: file,
  });
  const ref = await readStagedRef(res, "暂存");
  // 只有会被「写完再读」的落点才需要登记；上传源是直接喂给 fs_upload 的。
  if (!opts.persist) stagedIds.set(ref.path, ref.id);
  return ref;
}

/** 预建一个空落点，给「服务端写、浏览器收」的下载用。 */
async function reserveTarget(name: string): Promise<StagedRef> {
  const q = new URLSearchParams({ name });
  const res = await fetch(httpUrl(`/files/blob/reserve?${q.toString()}`), { method: "POST" });
  const ref = await readStagedRef(res, "预留落点");
  stagedIds.set(ref.path, ref.id);
  return ref;
}

/** 在用户手势内问一次保存落点：拿到句柄 → 复用，取消 → 放弃，问不到 → 稍后兜底。 */
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
    // 用户主动取消：把整个流程停掉，不要在他取消之后又冒出一个下载。
    if (isUserCancel(e)) return "cancelled";
    // 其余情况（注入脚本还没就绪、权限被拒等）交给锚点兜底，宁可多弹一次窗口也别丢文件。
    console.warn("[files] showSaveFilePicker 失败，交付阶段回落到锚点下载", e);
    return "unavailable";
  }
}

/**
 * 下载 / 录制的落点：**必须在调用点的用户手势里调用**（见文件头的说明）。
 *
 * 返回盒子上的暂存路径（内核往那儿写），`deliverStaged` 再把它交付给浏览器。
 * 用户在保存窗口点了取消时返回 `null` —— 调用点据此放弃整个操作。
 */
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

/** 丢弃暂存项。上传成功、或流程失败时调用。 */
export async function dropStaged(path: string): Promise<void> {
  const id = stagedIds.get(path);
  if (!id) return;
  stagedIds.delete(path);
  saveHandles.delete(path);
  await deleteBlob(id);
}

/**
 * 把服务端暂存的内容交给浏览器保存。
 *
 * 优先写进 `requestSaveTarget` 在手势期留下的句柄；没有句柄（浏览器不支持
 * `showSaveFilePicker()`）时退回 `saveBrowserBlob`，两条路都在注入的覆盖范围内。
 *
 * 三态返回，因为调用方要据此决定提示语：
 *   · `delivered`  —— 已交给浏览器保存（落点在手势期就确认过了）
 *   · `cancelled`  —— 用户在保存窗口点了取消，内容已丢弃
 *   · `not-staged` —— 这个路径不是暂存项（桌面模式，或流程走岔了）
 */
export async function deliverStaged(
  path: string,
  name?: string,
): Promise<"delivered" | "cancelled" | "not-staged"> {
  const id = stagedIds.get(path);
  if (!id) return "not-staged";
  stagedIds.delete(path);
  // 手势内已经问过落点：直接写进去，不重复弹窗口。
  const handle = saveHandles.get(path);
  saveHandles.delete(path);
  // 只有「确实交出去了」或「用户不要了」才删副本。中途报错时**留着** ——
  // 服务端那份是唯一副本，删掉等于把用户刚下的东西毁了（巡检两小时后会收）。
  let cleanup = false;
  try {
    const res = await fetch(httpUrl(`/files/blob?id=${encodeURIComponent(id)}`));
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

/** 删掉服务端暂存副本。删不掉不致命（巡检会收），不值得打扰用户。 */
async function deleteBlob(id: string): Promise<void> {
  await fetch(httpUrl(`/files/blob?id=${encodeURIComponent(id)}`), { method: "DELETE" }).catch(
    () => undefined,
  );
}

// ── 存文件 ────────────────────────────────────────────────────────────

/** 把浏览器原生保存 API 里我们用到的那几个成员声明出来（`lib.dom` 没带）。 */
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

/**
 * 兜底保存路径：**没有**手势期句柄时才会用到（见 `deliverStaged`）。
 *
 * 返回 `true` = 已交给浏览器保存；`false` = 用户取消。
 *
 * 这里再试一次 `showSaveFilePicker()`：若是别的原因导致手势期没拿到句柄，
 * 此刻可能还来得及；再失败就退成带 `download` 的 `blob:` 锚点 ——
 * 锚点同样在注入的覆盖范围内，所以三条路用户都能选「网盘 / 本地」。
 */
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
      // 用户主动取消：不兜底，否则会在他取消之后又冒出一个下载。
      if (isUserCancel(e)) return false;
      // 其余情况（注入脚本还没就绪、权限被拒等）落到锚点下载，宁可多下一次也别丢文件。
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
  // 立刻 revoke 会让部分浏览器下到一个空文件，给足时间再回收。
  window.setTimeout(() => URL.revokeObjectURL(url), 30_000);
}

/** 路径的 basename（两条分隔符都认，盒子可能是 Linux 也可能是 Windows 远端）。 */
export function baseName(path: string): string {
  const idx = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return idx < 0 ? path : path.slice(idx + 1);
}

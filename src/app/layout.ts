// 布局同步：把「开着哪些工作区 / 面板 / 标签」这件事交给服务端。
//
// # 为什么布局要出浏览器
//
// 改造前工作区只活在 zustand 里，关掉页面就没了。而终端进程其实一直在服务端跑着
// （回滚缓冲、连接都在）—— 缺的只是这份视图。把它落到服务端，浏览器才真正退化成
// 一个显示器：「在外面开了个打日志的长任务，关掉网页，下次打开还能看到它」。
//
// # 三件事
//
// 1. **启动恢复**：`layout_get` → 重建工作区 / 面板 / 标签，终端标签带 `tabId`，
//    上层会以 `resumeTabId` 传给 XtermView（接管，而不是新开 shell）。
// 2. **变化上报**：store 变了 debounce 600ms 后 `layout_put`（带乐观锁 revision）。
// 3. **跨端刷新**：订阅 `layout://changed`，对端改了就把最新布局拉回来。
//
// # 两个必须处理的坑
//
// · **回声**：自己写的那次也会收到 `layout://changed`。用 revision 判据挡掉
//   （收到的不大于本地已知的就忽略），否则两端会来回触发刷新，形成死循环。
// · **冲突**：`conflict: true` 时**不要**拿自己这份重试覆盖 —— 布局没有可合并
//   语义，重试只会把对方的改动顶掉。正确做法是拉最新。
//
// 内核侧见 `src-tauri/src/commands/layout.rs`：它只负责存 / 取 / 版本，
// 不解释 data 的字段（前端改布局结构不用动 Rust）。
import { layoutApi, terminalApi } from "../ipc/commands";
import { listenEvent } from "../ipc/events";
import { describeError } from "../ui/errorText";
import {
  LEFT_WIDTH_RANGE,
  RIGHT_WIDTH_RANGE,
  nextTabId,
  useUi,
  type AppTab,
  type LeftMode,
  type Pane,
  type PaneKind,
  type Workspace,
  type WorkspaceKind,
} from "./store";

/** 事件名与 Rust 侧 `commands::layout::LAYOUT_CHANGED` 必须一致。 */
const LAYOUT_CHANGED = "layout://changed";

/** 变化上报的 debounce：拖拽侧栏宽度时每帧都会写 store，不 debounce 会打爆 IPC。 */
const SAVE_DEBOUNCE_MS = 600;

/** 写失败后的重试间隔与次数上限（服务端短暂不可用时不该丢这次布局变化）。 */
const RETRY_MS = 3000;
const MAX_RETRY = 3;

/** 持久化格式版本。结构不兼容时靠它整体丢弃，好过拿半份脏数据去重建界面。 */
const LAYOUT_VERSION = 1;

export interface PersistedLayout {
  v: number;
  leftOpen: boolean;
  leftMode: LeftMode;
  rightOpen: boolean;
  leftWidth: number;
  rightWidth: number;
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
}

/* ── 序列化 / 反序列化 ─────────────────────────────────────────────── */

const PANE_KINDS = new Set<PaneKind>([
  "terminal",
  "files",
  "mount",
  "forward",
  "docker",
  "db",
  "credentials",
  "credentialsText",
  "settings",
  "audit",
  "background",
]);

function isObj(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}
function optStr(v: unknown): string | undefined {
  return typeof v === "string" && v.length > 0 ? v : undefined;
}
function boolOr(v: unknown, d: boolean): boolean {
  return typeof v === "boolean" ? v : d;
}
/** 像素宽度这类整数用：四舍五入后再夹取。 */
function clampNum(v: unknown, min: number, max: number, d: number): number {
  if (typeof v !== "number" || !Number.isFinite(v)) return d;
  return Math.min(max, Math.max(min, Math.round(v)));
}
/**
 * 比例这类小数用：**不能四舍五入**。
 * `splitRatio` 是 0.15~0.85 的小数，走 clampNum 会被 round 成 0 或 1 再夹回边界，
 * 于是「分屏比例」在恢复后永远变成 0.85 —— 一个只有跑起来才看得见的错值。
 */
function clampFraction(v: unknown, min: number, max: number, d: number): number {
  if (typeof v !== "number" || !Number.isFinite(v)) return d;
  return Math.min(max, Math.max(min, v));
}

/**
 * 把标签规整成可持久化的形状。
 *
 * ⚠️ **没有 `tabId` 的终端标签一律丢弃**。它是「恢复时会新开一个 shell」的根源：
 * 恢复出来的终端若拿不到内核标签 id，XtermView 会走 `terminal_attach` 新建分支，
 * 用户看到的是「我原来的任务不见了，眼前是个空终端」，而远端悄悄多了一个泄漏的 shell。
 * 宁可不恢复这条标签。
 */
function sanitizeTab(raw: unknown): AppTab | null {
  if (!isObj(raw)) return null;
  const id = optStr(raw.id);
  const kind = raw.kind;
  if (!id || typeof kind !== "string" || !PANE_KINDS.has(kind as PaneKind)) return null;
  const tabId = optStr(raw.tabId);
  if (kind === "terminal" && !tabId) return null;
  return {
    id,
    kind: kind as PaneKind,
    title: typeof raw.title === "string" ? raw.title : "标签",
    sessionId: optStr(raw.sessionId),
    tabId,
    containerId: optStr(raw.containerId),
    connId: optStr(raw.connId),
    dbKind: raw.dbKind === "mysql" || raw.dbKind === "redis" ? raw.dbKind : undefined,
    credId: optStr(raw.credId),
    credView: raw.credView === "json" ? "json" : raw.credView === "text" ? "text" : undefined,
    path: optStr(raw.path),
    // pendingCommand 是进程内的瞬时状态（右键「在终端打开」的 cd），刻意不持久化：
    // 恢复时再执行一遍等于凭空往远端发一条命令。
    closable: boolOr(raw.closable, true),
  };
}

function sanitizePane(raw: unknown): Pane | null {
  if (!isObj(raw)) return null;
  const id = optStr(raw.id);
  if (!id) return null;
  const tabs = Array.isArray(raw.tabs)
    ? raw.tabs.map(sanitizeTab).filter((t): t is AppTab => t !== null)
    : [];
  const active = optStr(raw.activeTabId);
  const activeTabId =
    active && tabs.some((t) => t.id === active) ? active : (tabs[tabs.length - 1]?.id ?? null);
  return { id, tabs, activeTabId };
}

function sanitizeWorkspace(raw: unknown): Workspace | null {
  if (!isObj(raw)) return null;
  const id = optStr(raw.id);
  const kind = raw.kind;
  if (!id || (kind !== "session" && kind !== "db" && kind !== "tools")) return null;
  const parsed = Array.isArray(raw.panes)
    ? raw.panes.map(sanitizePane).filter((p): p is Pane => p !== null)
    : [];
  // 分屏里那个只剩空壳的面板（里面的终端因为没 tabId 被丢掉了）没收掉的话，
  // 界面会留下一条空的分屏格。全空时保留一个，工作区本身是可以没有标签的。
  const nonEmpty = parsed.filter((p) => p.tabs.length > 0);
  let panes = nonEmpty.length > 0 ? nonEmpty : parsed.slice(0, 1);
  if (panes.length === 0) panes = [{ id: nextTabId("pane"), tabs: [], activeTabId: null }];
  const activePaneId = panes.some((p) => p.id === raw.activePaneId)
    ? (raw.activePaneId as string)
    : panes[0].id;
  return {
    id,
    kind: kind as WorkspaceKind,
    title: typeof raw.title === "string" ? raw.title : "工作区",
    sessionId: optStr(raw.sessionId),
    assetId: optStr(raw.assetId),
    connId: optStr(raw.connId),
    dbKind: raw.dbKind === "mysql" || raw.dbKind === "redis" ? raw.dbKind : undefined,
    assetKind: optStr(raw.assetKind),
    panes,
    activePaneId,
    // ⚠️ 必须显式列出！`sanitizeWorkspace` 是**显式构造对象**（不是展开 raw），
    // 漏一个字段就会「刷新一次就丢」—— `dead` 字段的注释就是踩过这个坑留下的。
    // 默认 row：旧布局里没有这个字段，读进来就是上下分屏，语义与升级前一致，
    // 所以**不用升 LAYOUT_VERSION**（升版本反而会把所有人的布局重置掉）。
    splitAxis: raw.splitAxis === "col" ? "col" : "row",
    splitRatio: clampFraction(raw.splitRatio, 0.15, 0.85, 0.5),
    closable: boolOr(raw.closable, true),
  };
}

/** 校验并重建一份布局；结构不认识（版本不对 / 字段缺失）就返回 null 走默认行为。 */
export function sanitizeLayout(raw: unknown): PersistedLayout | null {
  if (!isObj(raw)) return null;
  if (raw.v !== LAYOUT_VERSION) return null;
  if (!Array.isArray(raw.workspaces)) return null;
  const workspaces = raw.workspaces
    .map(sanitizeWorkspace)
    .filter((w): w is Workspace => w !== null);
  const activeWorkspaceId = workspaces.some((w) => w.id === raw.activeWorkspaceId)
    ? (raw.activeWorkspaceId as string)
    : (workspaces[0]?.id ?? null);
  return {
    v: LAYOUT_VERSION,
    leftOpen: boolOr(raw.leftOpen, true),
    leftMode:
      raw.leftMode === "files" || raw.leftMode === "credentials" ? raw.leftMode : "assets",
    rightOpen: boolOr(raw.rightOpen, true),
    leftWidth: clampNum(raw.leftWidth, LEFT_WIDTH_RANGE.min, LEFT_WIDTH_RANGE.max, LEFT_WIDTH_RANGE.default),
    rightWidth: clampNum(
      raw.rightWidth,
      RIGHT_WIDTH_RANGE.min,
      RIGHT_WIDTH_RANGE.max,
      RIGHT_WIDTH_RANGE.default,
    ),
    workspaces,
    activeWorkspaceId,
  };
}

/** store 里参与持久化的那部分（只读取，不持有引用）。 */
interface LayoutSource {
  leftOpen: boolean;
  leftMode: LeftMode;
  rightOpen: boolean;
  leftWidth: number;
  rightWidth: number;
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
}

/**
 * 生成可持久化的 JSON 对象。
 *
 * 走一遍 `sanitizeLayout` 而不是直接 `JSON.stringify(state)`：这样"写进去的"
 * 和"读出来的"用同一条规整逻辑，避免出现「存了一份恢复不了的布局」——
 * 那是最难查的一类 bug（每次打开都少点东西，但没有任何报错）。
 */
export function serializeLayout(s: LayoutSource): PersistedLayout | null {
  return sanitizeLayout({
    v: LAYOUT_VERSION,
    leftOpen: s.leftOpen,
    leftMode: s.leftMode,
    rightOpen: s.rightOpen,
    leftWidth: s.leftWidth,
    rightWidth: s.rightWidth,
    workspaces: s.workspaces,
    activeWorkspaceId: s.activeWorkspaceId,
  });
}

/* ── 同步引擎 ─────────────────────────────────────────────────────── */

let started = false;
let resolveBootstrap: () => void = () => {};
/**
 * 首次 `layout_get` 完成（成功或失败）后 resolve。
 *
 * 启动期的自动连接（首启动连「当前设备」/ 演示模式连 web-01）必须等它：
 * 恢复出来的布局里已经有工作区时，不该再凭空多开一个终端。
 */
export const layoutBootstrapped: Promise<void> = new Promise((r) => {
  resolveBootstrap = r;
});

/** 服务端当前 revision。写入成功后同步为返回值。 */
let revision = 0;
/** 最近一次确认已同步到服务端的布局 JSON；相同就跳过写入。 */
let lastSynced = "";
/** 正在把服务端布局套用到 store：此时的 store 变化不该触发回写。 */
let applyingRemote = false;
let saveTimer: number | null = null;
let retryTimer: number | null = null;
let retryCount = 0;
let saveErrorNotified = false;
let fetchInFlight = false;
/**
 * 写路径串行化（见 [`flushLayout`]）。
 *
 * `writing` = 有一次 `layout_put` 在途；`dirty` = 在途期间又有 flush 请求，
 * 等它写完要再补一次。没有这两个闸门时，两次写会**并发**发出、用同一个过期
 * `revision`，第二次必然 conflict，从而触发"拉最新 → 用服务端那份盖掉本地"，
 * 把用户第二处改动吃掉。
 */
let writing = false;
let dirty = false;
/**
 * 写 `layout_put` **在途期间**收到过远端 `layout://changed`。
 *
 * # 为什么需要它（竞态，实机实测）
 *
 * 服务端提交后立刻广播，广播**可能先于 HTTP 响应到达**（实测相差约 2ms 级）：
 * 事件处理器跑完时 `layout_put` 的 response 还没回到 fetch 的 then，本地
 * `revision` 仍是旧值 —— 于是 `onRemoteChange` 的 `rev <= revision` 快速判据
 * **拦不住自己的回声**，会把自己的写当成"对端改动"再拉一遍。
 *
 * 而这个场景里「自修复」恰恰**故意**让 store 与服务端布局不一致（清掉失效
 * `tabId` ⇒ 被 `sanitizeTab` 丢弃），所以那一拉会把本端刚清理掉的东西又抹掉 ——
 * 现象就是「失效遮罩只活约 0.7 秒」。
 *
 * ⚠️ 这个在途守卫**仍然正确且必要**（避免写期间多拉一次、可能把对端新事件错过），
 * 但它**不是**遮罩存活的解法：写彻底完成后 [`flushLayout`] 还会补拉一次，那一拉照样
 * 把服务端那份（不含死标签）套进 store。真正解决遮罩存活的是 [`mergeDeadTabs`] ——
 * 它让 `applyToStore` 把本地 `dead` 标签并回来。
 *
 * # 为什么是"记下"而不是"丢弃"
 *
 * 在途期间到达的事件里可能**混着对端的新写入**（对端在我之后成功提交，它的广播
 * 正好赶在我的响应之前到）。直接丢掉会让本端停在旧 revision 上，直到下一次事件
 * 才追上。所以先记下，等这次写彻底完成后补拉一次（见 [`flushLayout`]）。
 */
let remoteEventDuringWrite = false;

function clearSaveTimer() {
  if (saveTimer !== null) {
    window.clearTimeout(saveTimer);
    saveTimer = null;
  }
}
function clearRetryTimer() {
  if (retryTimer !== null) {
    window.clearTimeout(retryTimer);
    retryTimer = null;
  }
}

/**
 * 把本地处于「连接已失效」态（`dead: true`）的终端标签并回远端布局。
 *
 * # 为什么需要它（实机实测）
 *
 * 服务端重启后内核里那条终端没了，attach 拿到 `not_found` ⇒ 本地把 `tabId` 清掉并切到
 * 失效态（见 `TerminalPane.handleAttachDead`）。但 `sanitizeTab` 对**没有 `tabId` 的
 * 终端标签一律丢弃**（有意为之，防死标签永久累积），于是：
 * 600ms debounce 后那次 `layout_put` 写出的布局**不含这条标签**；随后任何一次
 * `pullLatest → applyToStore` 都会把服务端那份（不含该标签）套进 store ⇒ 标签被移除、
 * 遮罩随之消失（实测遮罩只活 ~608ms ⇒「重新连接这台主机」按钮按不到）。
 *
 * `dead` 是纯本地字段（不进 `sanitizeTab` 白名单、不写服务端），所以服务端那份永远
 * 不含它 —— 这里就是把它补回来：把本地 dead 标签按 workspace id / pane id 并回远端布局。
 * 死标签依旧不进服务端，所以「不累积」不回归。
 *
 * **不修改入参**（`next` / `current` 都不就地改），返回新数组；无候选时**原样返回**
 * `next.workspaces`（不做无谓的浅拷贝，否则每次远端同步都会多一次 re-render）。
 */
export function mergeDeadTabs(next: PersistedLayout, current: Workspace[]): Workspace[] {
  // 收集本地失效标签：workspace id → pane id → 待并标签。
  const byWs = new Map<string, Map<string, AppTab[]>>();
  for (const w of current) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (t.kind !== "terminal" || t.dead !== true) continue;
        let panes = byWs.get(w.id);
        if (!panes) {
          panes = new Map();
          byWs.set(w.id, panes);
        }
        const list = panes.get(p.id);
        if (list) list.push(t);
        else panes.set(p.id, [t]);
      }
    }
  }
  if (byWs.size === 0) return next.workspaces;

  let changed = false;
  const workspaces = next.workspaces.map((w) => {
    const paneMap = byWs.get(w.id);
    if (!paneMap) return w;
    let wsChanged = false;
    const panes = w.panes.map((p) => {
      const cand = paneMap.get(p.id);
      if (!cand) return p;
      const existing = new Set(p.tabs.map((t) => t.id));
      const add = cand.filter((t) => !existing.has(t.id)); // 去重：已有同 id 就跳过
      if (add.length === 0) return p;
      wsChanged = true;
      const tabs = [...p.tabs, ...add];
      // activeTabId 为 null 或已不指向存在的标签 ⇒ 指到最后一个并入的标签，
      // 让用户看到的是遮罩那一屏，而不是一个空面板 /「新建终端」。
      const activeAlive =
        p.activeTabId !== null && tabs.some((t) => t.id === p.activeTabId);
      return {
        ...p,
        tabs,
        activeTabId: activeAlive ? p.activeTabId : add[add.length - 1].id,
      };
    });
    // 目标 pane 在远端那份里整个缺失（分屏里那个面板因清空被 sanitizeWorkspace 丢掉）
    // ⇒ 补出来，保证 dead 标签有落脚的面板。
    const missing: Pane[] = [];
    for (const [paneId, cand] of paneMap) {
      if (w.panes.some((p) => p.id === paneId)) continue;
      wsChanged = true;
      missing.push({ id: paneId, tabs: [...cand], activeTabId: cand[cand.length - 1].id });
    }
    if (!wsChanged) return w;
    changed = true;
    return { ...w, panes: missing.length > 0 ? [...panes, ...missing] : panes };
  });
  return changed ? workspaces : next.workspaces;
}

/** 把服务端布局套用到 store（同时压低回声与回写）。 */
function applyToStore(l: PersistedLayout) {
  applyingRemote = true;
  clearSaveTimer();
  try {
    // 先算出并入本地失效标签后的工作区（必须在 setState 之前取当前 store）。
    const merged = mergeDeadTabs(l, useUi.getState().workspaces);
    useUi.setState({
      leftOpen: l.leftOpen,
      leftMode: l.leftMode,
      rightOpen: l.rightOpen,
      leftWidth: l.leftWidth,
      rightWidth: l.rightWidth,
      workspaces: merged,
      activeWorkspaceId: l.activeWorkspaceId,
    });
  } finally {
    applyingRemote = false;
  }
}

/**
 * 拉取服务端最新布局。
 *
 * `apply = false` 时只取回内容与最新 `revision`，**不**套进 store、也不更新
 * `lastSynced` —— 冲突处理要用它拿到新 revision，然后以本地为准再写一次，
 * 此时把服务端那份套进 store 只会当场抹掉用户刚改的东西。
 */
async function pullLatest(
  reason: "boot" | "remote" | "conflict",
  apply = true,
): Promise<PersistedLayout | null> {
  if (fetchInFlight) return null;
  fetchInFlight = true;
  try {
    const dto = await layoutApi.get();
    // revision 一定要对齐：即使下面因为 data 为 null 不应用布局，
    // 下一次写入也必须基于最新的 revision，否则一上来就撞冲突。
    revision = dto.revision;
    const parsed = sanitizeLayout(dto.data);
    if (!parsed) {
      // data=null 表示服务端从没存过布局（首次使用），或版本不认识。
      // 此时**不动**本地视图，让"首启动自动连当前设备"这类默认行为照常发生。
      return null;
    }
    if (!apply) return parsed;
    // ⚠️ 必须在 applyToStore **之前**算差集：套用之后本端 store 已经变成对端那份，
    // 就再也看不出"这一端刚才还有哪些终端标签被这次同步移除了"。
    // 只在 remote 时做：boot 时本端 store 是空的，diff 必然假阳性。
    const removed = reason === "remote" ? collectRemovedTerminals(parsed) : [];
    lastSynced = JSON.stringify(parsed);
    applyToStore(parsed);
    if (reason === "conflict") {
      useUi.getState().pushToast("info", "布局已在其他设备上更新，已刷新为最新版本");
    }
    if (removed.length > 0) void notifyBackgroundedTerminals(removed);
    return parsed;
  } catch (e) {
    console.warn("[NexTerm] 拉取服务端布局失败", e);
    return null;
  } finally {
    fetchInFlight = false;
  }
}

/**
 * 写入一次布局（带乐观锁）。
 *
 * `allowTakeover = false` 表示"这已经是冲突后的第二次尝试"，再冲突就放弃重写。
 */
async function putLayout(json: string, allowTakeover = true): Promise<void> {
  try {
    const res = await layoutApi.put(json, revision);
    if (res.conflict) {
      // 对端在我们之后改过。
      //
      // # 取舍（**有意为之**，别再改回"直接 pullLatest 然后 return"）
      //
      // 布局没有可合并语义 ⇒ 不能真"合并"。旧写法是拉最新并 applyToStore，结果是把
      // 用户这一轮改动**当场抹掉**、界面还跳回去 —— 用户视角就是"我刚改的被吃了"。
      // 现在改成：只拉最新拿 revision（apply=false，不碰 store），再以本地为准写一次。
      //
      // 代价：两端**同时**改同一处时表现为**本地赢**（对方那次被覆盖）。这是可接受的
      // 取舍 —— 覆盖掉对方一次后台改动，远好过静默丢弃用户眼前的操作。
      const server = await pullLatest("conflict", false);
      // 拉回来的内容与本地相同 = 对端写的就是同一份，等价于成功。
      if (server !== null && JSON.stringify(server) === json) {
        lastSynced = json;
        retryCount = 0;
        saveErrorNotified = false;
        return;
      }
      if (allowTakeover) {
        // 用刚拉到的新 revision 再写一次本地（只让一次，避免两端反复抢）。
        await putLayout(json, false);
        return;
      }
      // 连续两次冲突：放弃以本地为准，退回"拉最新并应用"。此时本地这次改动确实保不住
      // （对端也在高频改同一份），如实让 pullLatest 弹提示，总好过无限重试。
      await pullLatest("conflict", true);
      return;
    }
    revision = res.revision;
    lastSynced = json;
    retryCount = 0;
    saveErrorNotified = false;
  } catch (e) {
    // 静默失败 = 用户以为布局存下来了、其实没有（下次打开少东西却查不出原因）。
    // 但布局同步是后台行为，不能每次失败都弹 toast 刷屏 —— 同一轮失败只提示一次。
    if (!saveErrorNotified) {
      saveErrorNotified = true;
      useUi.getState().pushToast("error", `布局同步失败：${describeError(e)}`);
    }
    // 重新排队：不重试的话这次布局变化就永远丢了（要等下次变化才会再写）。
    if (retryCount < MAX_RETRY) {
      retryCount += 1;
      clearRetryTimer();
      retryTimer = window.setTimeout(() => {
        retryTimer = null;
        void flushLayout();
      }, RETRY_MS);
    }
  }
}

/**
 * 把当前 store 的布局写回服务端（无实质变化则跳过）。
 *
 * 写路径**串行化**：同一时刻只允许一次 `putLayout` 在途；在途期间新的 flush 请求
 * 只置 `dirty`、不并发发出，等当前写完成后若仍与已写内容不同，用**新 revision**
 * 再补一次。不串行化的话两次写会用同一个过期 `revision`，第二次必然 conflict，
 * 于是触发"拉最新盖掉本地"—— 用户第二处改动丢失、界面还跳回去。
 *
 * 导出仅为纯逻辑测试可直接驱动它（正常由 debounce / 重试定时器调用）。
 */
export async function flushLayout(): Promise<void> {
  if (writing) {
    dirty = true;
    return;
  }
  const layout = serializeLayout(useUi.getState());
  if (!layout) return;
  const json = JSON.stringify(layout);
  if (json === lastSynced) return;
  writing = true;
  try {
    await putLayout(json);
  } finally {
    writing = false;
  }
  // 写的过程中本地又变了（或冲突回写后仍需再写）→ 补一次，把最新状态送达服务端。
  if (dirty) {
    dirty = false;
    await flushLayout();
  }
  // 在途期间收到过远端事件（广播可能先于 HTTP 响应到达，实测约 2ms 级）→ 补拉一次。
  //
  // 放在**写彻底完成之后**（含上面的 dirty 补写）：此刻本端已与服务端对齐，这次 pull
  // 不会再把自己还没写完的改动盖掉。安全性来自既有的冲突路径 —— 若对端那次真落地了，
  // 我下一次写会撞 conflict，走 [`putLayout`] 里"以本地为准重写一次"的处理；补拉只是
  // 为了不让我停在旧 revision 上。
  //
  // 补拉本身是对的；保住本地失效标签靠的是 [`mergeDeadTabs`] —— 服务端那份不含
  // dead 标签，靠 applyToStore 把它并回来，遮罩才不会被这次补拉抹掉。
  if (remoteEventDuringWrite) {
    await pullLatest("remote");
    remoteEventDuringWrite = false;
  }
}

/**
 * 找出 `next` 这份布局相对当前 store **会被移除的本端终端标签**（tabId + 标题）。
 *
 * 用于任务3：设备 A 关掉一个终端标签会把布局同步到 B，B 的标签会被移除；
 * 若内核里那个终端其实还在后台跑，必须提示用户，而不是静默失去画面。
 */
export function collectRemovedTerminals(next: PersistedLayout): { tabId: string; title: string }[] {
  const before = new Map<string, string>();
  for (const w of useUi.getState().workspaces) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (t.kind === "terminal" && t.tabId) before.set(t.tabId, t.title);
      }
    }
  }
  const after = new Set<string>();
  for (const w of next.workspaces) {
    for (const p of w.panes) {
      for (const t of p.tabs) {
        if (t.kind === "terminal" && t.tabId) after.add(t.tabId);
      }
    }
  }
  const out: { tabId: string; title: string }[] = [];
  for (const [tabId, title] of before) {
    if (!after.has(tabId)) out.push({ tabId, title });
  }
  return out;
}

/**
 * 对被远端布局移除、但**进程仍在内核里存活**的终端标签逐条提示。
 *
 * # 判据为什么是 `hit && !hit.exited`
 *
 * 核心语义是「**这个标签的进程还活着，而我的视图被拿走了**」。
 *
 * ⚠️ **不要再把 `hit.subscribers === 0` 加回来**：发起这次查询的客户端**自身就是
 * 一个订阅者** —— `applyToStore` 之后 B 自己的 React 卸载 / `XtermView` cleanup /
 * `terminal_detach` 都还在途中，实测在移除后 +8ms..+620ms 区间 `subscribers` 一直是
 * 1。所以 `subscribers === 0` 对「自己的标签」这个场景**结构上永远不可达**，加上它
 * 这条提示就永远不会弹（这正是修复前的现象：B 端 toast 恒为空）。
 *
 * 用 `exited` 能正确区分两种"关"：
 * - A 选「后台继续运行」⇒ 进程活着 ⇒ `terminal_list` 里仍有该标签、`exited=false`
 *   ⇒ **应当提示**（正是要修的）。
 * - A 选「结束进程」⇒ Rust 侧在从会话表摘掉**之前**先 `mark_exited()` 并发
 *   `terminal://control{exited:true}` ⇒ `listLive` 里**查不到**该标签（`hit` 为
 *   undefined）⇒ **不提示**（那是正常的"关"，提示反而是噪音）。
 *
 * 其余防线保留：查不到不提示、查询失败静默不提示。
 */
export async function notifyBackgroundedTerminals(
  removed: { tabId: string; title: string }[],
): Promise<void> {
  let live: Awaited<ReturnType<typeof terminalApi.listLive>>;
  try {
    live = await terminalApi.listLive();
  } catch {
    // 查询失败就不提示（宁可不提示，也不要凭猜测弹一条不准确的）。
    return;
  }
  for (const r of removed) {
    const hit = live.find((t) => t.tabId === r.tabId);
    if (!hit || hit.exited) continue;
    // 按**设备数**分档：`viewers` 按 clientId 去重、**含自己**，所以「只剩自己」
    // 就是 `viewers == 1`，「还有别的设备在看」是 `viewers > 1`。
    //
    // ⚠️ 判据必须用 `viewers`，不能用 `subscribers`：后者是通道数，同一台设备
    // 多开一个页面就会 +1，会把"只有我自己"误判成"还有别的设备在看"。
    // 也不能写成 `viewers === 0` —— 发起这次查询的客户端**自身就是**一个观看设备，
    // 那个条件结构上不可达（前几轮踩过 `subscribers === 0` 的同款坑）。
    const text =
      hit.viewers > 1
        ? `终端「${r.title}」已被其他设备关闭标签，进程仍在运行，且仍有其他设备正在观看，可在「后台会话」里接管`
        : `终端「${r.title}」已被其他设备关闭标签，进程仍在后台运行，可在「后台会话」里接管`;
    useUi.getState().pushToast("info", text);
  }
}

/**
 * 仅供纯逻辑测试重置模块级状态（生产代码不调用）。
 *
 * 这些状态（revision / lastSynced / 在途闸门…）是模块单例，测试要在同一进程里
 * 跑多个场景就得能回到初始值。
 */
export function resetLayoutSyncForTest(): void {
  revision = 0;
  lastSynced = "";
  applyingRemote = false;
  retryCount = 0;
  saveErrorNotified = false;
  fetchInFlight = false;
  writing = false;
  dirty = false;
  remoteEventDuringWrite = false;
  clearSaveTimer();
  clearRetryTimer();
}

function onStoreChange() {
  if (applyingRemote) return;
  clearSaveTimer();
  saveTimer = window.setTimeout(() => {
    saveTimer = null;
    void flushLayout();
  }, SAVE_DEBOUNCE_MS);
}

/**
 * 收到远端 `layout://changed`。
 *
 * 导出仅为纯逻辑测试可直接投递远端事件（生产代码由 [`startLayoutSync`] 订阅）。
 */
export function onRemoteChange(payload: { revision?: number } | null) {
  const rev = payload?.revision;
  // 自己写的那次也会收到这个事件。收到的不大于本地已知的 revision 就直接忽略 ——
  // 不做这一步，本端会把自己刚写的布局再拉一遍，两端来回触发形成死循环。
  //
  // ⚠️ 这条快速判据挡不住"广播抢在 HTTP 响应之前"（实测约 2ms 级）：那种情况下
  // 本地 revision 还没更新，`rev <= revision` 为假，自己的回声会被当成对端改动。
  if (typeof rev === "number" && rev <= revision) return;
  // 自己的 `layout_put` 还在途：此时 pull 会把自己的回声拉进来，而本端 store 与
  // 服务端那份可能**故意**不一致（例如刚清掉失效 tabId）—— 一拉就把清理动作抹掉。
  // 先记下、不拉，等这次写彻底完成后由 flushLayout 补拉一次（也保住对端的新事件）。
  if (writing) {
    remoteEventDuringWrite = true;
    return;
  }
  void pullLatest("remote");
}

/**
 * 启动布局同步（幂等；StrictMode 下 effect 会调两次）。
 *
 * 顺序很讲究：**先把服务端布局恢复进 store，再订阅 store 变化**。
 * 反过来的话，恢复过程本身会排一次回写，把刚拉下来的布局又写回去
 * （revision 自增、惊动其他设备，还可能撞上对端的并发写入）。
 */
export function startLayoutSync(): void {
  if (started) return;
  started = true;
  void (async () => {
    try {
      await pullLatest("boot");
    } finally {
      resolveBootstrap();
    }
    useUi.subscribe(onStoreChange);
    // 统一走 listenEvent：桌面（Tauri listen）/ 服务端（/ws/events）/ 演示（本地总线）
    // 三条路径共用，不必在这里判环境。
    await listenEvent<{ revision?: number }>(LAYOUT_CHANGED, onRemoteChange);
  })();
}

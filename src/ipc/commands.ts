// 与 Rust 的唯一接口层：类型化命令包装（§6）。
// 类型来自 ts-rs 生成（cargo test 导出），见 types.ts。
import { invoke } from "@tauri-apps/api/core";
import { DEMO, WEB } from "../demo";
import { clientId, httpUrl } from "./env";
import { describeError } from "../ui/errorText";

// ───────── 通用 ─────────

export interface AppError {
  code: string;
  message: string;
  detail?: Record<string, unknown>;
}

export function toAppError(e: unknown): AppError {
  if (e && typeof e === "object" && "code" in e) {
    return e as AppError;
  }
  return { code: "internal", message: describeError(e) };
}

/**
 * 唯一的 IPC 出海口。三种运行环境各走一条：
 *
 * | 环境 | 出口 |
 * |---|---|
 * | 桌面（Tauri） | `invoke()` —— 现有发布线，行为一个字不变 |
 * | 服务端（浏览器） | `POST /rpc` —— 见 nexterm-server |
 * | 演示 | `src/demo/mock.ts` 的内存实现 |
 *
 * 三者的**参数形状完全一致**（Tauri 的 `invoke(cmd, args)` 约定），
 * 所以上面那 700 行 `sessionApi` / `fsApi` / … 一行都不用改 —— 包括
 * 通道参数：服务端模式下 `WebChannel.toJSON()` 会把它压成一个 id 字符串，
 * `JSON.stringify` 自然就把它送出去了。
 *
 * mock 用动态 import：让假数据 + 虚拟 shell 独立成一个 chunk，
 * Tauri 生产包不会加载它。
 */
async function call<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  try {
    if (DEMO) {
      const { mockInvoke } = await import("../demo/mock");
      return (await mockInvoke(cmd, args)) as T;
    }
    if (WEB) {
      return await callWeb<T>(cmd, args);
    }
    return await invoke<T>(cmd, args);
  } catch (e) {
    throw toAppError(e);
  }
}

/**
 * 服务端出口。
 *
 * 错误也走 HTTP 200 + `{ok:false,error}` 信封（服务端如此约定）——
 * 用非 2xx 会让 `fetch` 的失败路径和业务错误路径分裂成两套，
 * 而 `code`（`vault_locked` / `host_key_pending` …）恰恰在前端是有分支意义的。
 */
async function callWeb<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  const res = await fetch(httpUrl("/rpc"), {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ cmd, args: args ?? null }),
  });
  const text = await res.text();
  let body: { ok?: boolean; data?: unknown; error?: unknown } | null = null;
  try {
    body = JSON.parse(text) as { ok?: boolean; data?: unknown; error?: unknown };
  } catch {
    // 不是 JSON：多半是被静态托管兜底成了 index.html（路由没对上）。
    throw {
      code: "internal",
      message: `服务端返回了非 JSON 响应（HTTP ${res.status}）：${text.slice(0, 120)}`,
    } satisfies AppError;
  }
  if (!body || body.ok !== true) {
    throw (body?.error ?? { code: "internal", message: `HTTP ${res.status}` }) as AppError;
  }
  return body.data as T;
}

// ───────── 内核自述 ─────────
//
// 首帧渲染前要用到这两条，所以单独成一组。
// 注意：**服务端模式下 `app_platform` 回的是「后端所在的系统」**，
// 不是浏览器所在的系统 —— 这正是我们要的（标题栏留白这类判定问的是
// 「有没有原生红绿灯」，而红绿灯只存在于桌面窗口里，见 App.tsx）。

export const systemApi = {
  platform: () => call<string>("app_platform"),
};

// ───────── session ─────────

export interface SessionInfo {
  id: string;
  assetId: string | null;
  name: string;
  kind: string;
  status: "connecting" | "connected" | "reconnecting" | "disconnected" | "failed";
  tabs: string[];
  createdAt: number;
}

export const sessionApi = {
  connect: (assetId: string, acceptHostKey = false) =>
    call<SessionInfo>("session_connect", { args: { assetId, acceptHostKey } }),
  connectLocal: () => call<SessionInfo>("session_connect_local"),
  disconnect: (sessionId: string) =>
    call<void>("session_disconnect", { sessionId }),
  /**
   * 重连一个已断开的会话（§7）。
   *
   * **非阻塞**：返回 `true` 只代表"已开始重连"，真正的退避重试在后台跑（最坏
   * 三分钟），结果通过 `session://status` 事件推回来（重连中 → 已连接 / 连接失败）。
   * 返回 `false` 不是错误 —— 表示这个会话天然不可重连（本机会话没有重连语义，
   * 或没绑定资产）。
   */
  reconnect: (sessionId: string) => call<boolean>("session_reconnect", { sessionId }),
  list: () => call<SessionInfo[]>("session_list"),
  probe: (host: string, port: number, timeoutMs?: number) =>
    call<{ open: boolean; error?: string }>("session_probe", {
      args: { host, port, timeoutMs },
    }),
  openLineTab: (sessionId: string, cols: number, rows: number, channel: unknown) =>
    call<string>("session_open_line_tab", {
      sessionId,
      cols,
      rows,
      channel,
      clientId: clientId(),
    }),
  lineExec: (tabId: string, line: string) =>
    call<void>("session_line_exec", { tabId, line }),
  cwd: (sessionId: string) =>
    call<string | null>("session_cwd", { sessionId }),
};

// ───────── terminal ─────────

/**
 * 接管一个已有内核标签的结果（`terminal_attach_tab` 的返回）。
 *
 * 字段与 Rust 侧 `session::AttachedTabInfo` 一一对应（camelCase）。
 */
export interface AttachedTabInfo {
  tabId: string;
  sessionId: string;
  /** PTY 的**当前**尺寸 —— 接管方应照它渲染，而不是按自己窗口尺寸去改 PTY。 */
  cols: number;
  rows: number;
  /** 当前持输入控制权的人；`null` = 无人持权（谁都不能敲）。 */
  controller: string | null;
  /**
   * 有几个前端在看。
   *
   * ⚠️ **含自己**：刚 attach 完立刻读会是 1。这是**通道数**（同一台设备开两个页面
   * 就是 2），不是设备数 —— 界面要显示「几个设备在看」请用 {@link viewers}。
   */
  subscribers: number;
  /** **观看设备数**（按 clientId 去重）：同一台设备多开页面不重复计数。 */
  viewers: number;
  /** 这个标签的进程已经结束了（只能看到最后一屏，敲不了）。 */
  exited: boolean;
}

/** 内核里存活着的终端标签（后台会话面板的数据源）。 */
export interface LiveTabInfo {
  tabId: string;
  sessionId: string;
  sessionName: string;
  sessionKind: string;
  cols: number;
  rows: number;
  controller: string | null;
  /** 通道数（含自己）：同一台设备多开一个页面就会 +1。 */
  subscribers: number;
  /** 观看设备数（按 clientId 去重）：界面上「N 个设备正在观看」用它。 */
  viewers: number;
  exited: boolean;
  lastOutputMsAgo: number;
}

export const terminalApi = {
  /**
   * 新建终端标签 —— **会在远端开一个新 shell**。
   *
   * 想接回已有标签请用 {@link attachTab}：这个命令每调一次就多一个 shell。
   */
  attach: (sessionId: string, cols: number, rows: number, channel: unknown) =>
    call<string>("terminal_attach", { sessionId, cols, rows, channel, clientId: clientId() }),
  /**
   * 接管一个**已存在**的内核标签（不新开 shell）。
   *
   * 这是「关掉网页再回来，终端还在、中间那段日志也在」的关键路径：服务端把那条
   * 连接上的滚动内容回放给这个新页面。
   *
   * ⚠️ **刻意不收 `cols`/`rows`**：一个 PTY 只有一组尺寸，接管方若顺手按自己窗口
   * 改尺寸，正在另一台设备上操作的人画面会被突然重排。尺寸只由**持控制权**的那端
   * 决定（`resize` 会校验控制权），返回的 `cols`/`rows` 才是当前真实尺寸。
   *
   * `replayBytes` 缺省由服务端定（4 MiB）。
   */
  attachTab: (tabId: string, channel: unknown, replayBytes?: number) =>
    call<AttachedTabInfo>("terminal_attach_tab", {
      tabId,
      replayBytes,
      channel,
      clientId: clientId(),
    }),
  write: (tabId: string, data: Uint8Array) =>
    call<void>("terminal_write", {
      args: { tabId, data: Array.from(data), clientId: clientId() },
    }),
  resize: (tabId: string, cols: number, rows: number) =>
    call<void>("terminal_resize", { tabId, cols, rows, clientId: clientId() }),
  /**
   * 摘掉自己的订阅。
   *
   * ⚠️ `channelId` 必须传（服务端）：不传就是「清空全部」，会把同一个终端上
   * 其他设备的推送一起掐掉。
   */
  detach: (tabId: string, channelId?: string) =>
    call<void>("terminal_detach", { tabId, channelId }),
  /** 接管输入控制权（单点模式）。返回被顶掉的那个人。 */
  claim: (tabId: string) =>
    call<string | null>("terminal_claim", { tabId, clientId: clientId() }),
  /** 主动交出输入控制权。 */
  release: (tabId: string) =>
    call<boolean>("terminal_release", { tabId, clientId: clientId() }),
  /** 内核里存活着的终端标签（含在后台跑的）。 */
  listLive: () => call<LiveTabInfo[]>("terminal_list"),
  screenText: (tabId: string) => call<string>("terminal_screen_text", { tabId }),
  snapshot: (tabId: string) =>
    call<import("./types").ScreenSnapshotDto>("terminal_snapshot", { tabId }),
  tail: (tabId: string, n: number) =>
    call<string[]>("terminal_tail", { tabId, n }),
  setVisible: (tabId: string, visible: boolean) =>
    call<void>("terminal_set_visible", { tabId, visible }),
  dump: (tabId: string, maxBytes?: number) =>
    call<string>("terminal_dump", { tabId, maxBytes }),
  switchEncoding: (tabId: string, encoding: string) =>
    call<void>("terminal_switch_encoding", { tabId, encoding }),
  recordStart: (tabId: string, path: string) =>
    call<void>("terminal_record_start", { tabId, path }),
  recordStop: (tabId: string) => call<number>("terminal_record_stop", { tabId }),
  /**
   * 把当前回滚输出写到本地文件，返回写入字节数。
   *
   * 走内核而不是前端：前端只装了 dialog 插件（能选路径），没装 fs 插件 ——
   * 浏览器里没有任何办法把一段文字落到用户磁盘上。
   */
  exportLog: (tabId: string, path: string, maxBytes?: number) =>
    call<number>("terminal_export_log", { tabId, path, maxBytes }),
  /**
   * 关闭标签。
   *
   * `mode`：
   * - `"detach"` —— 只从视图里拿走，**进程继续在服务端跑**（跑长任务时选这个，
   *   之后可以在「后台会话」里重新接管）
   * - 不传 —— 真的结束：停泵、杀进程
   *
   * 缺省是「真结束」而不是 «detach»：不能因为"关标签"这个动作看着轻，就把
   * 用户可能正等着结果的任务默默留成后台僵尸。后台运行必须由用户显式选择。
   */
  closeTab: (tabId: string, mode?: "kill" | "detach") =>
    // 带上 clientId：服务端要据此**只摘掉本端那一条订阅**，而不是无差别清空全部。
    // 不传时内核按"整个进程一个视图"的桌面语义退回清空全部（见 Rust terminal_close_tab），
    // 所以两边落地有先后也不会坏。
    call<void>("terminal_close_tab", { tabId, mode, clientId: clientId() }),
};

// ───────── layout（工作区布局：服务端权威运行态）─────────

export interface LayoutDto {
  /** 乐观锁版本号。写入时必须带上"我这份是基于哪个版本"。 */
  revision: number;
  updatedAt: number;
  /** 布局正文（前端的视图模型，内核不解释）；从没保存过时是 null。 */
  data: unknown | null;
}

export interface LayoutSaveResult {
  saved: boolean;
  /** 写入后的 revision（`saved=false` 时是**对端**的当前版本）。 */
  revision: number;
  /** 对端在你之后改过。应拉最新再决定，**不要**直接重试覆盖。 */
  conflict: boolean;
}

export const layoutApi = {
  get: () => call<LayoutDto>("layout_get"),
  put: (data: string, revision: number) =>
    call<LayoutSaveResult>("layout_put", { args: { data, revision } }),
};

// ───────── asset / group / snippet ─────────

export interface Asset {
  id: string;
  groupId: string | null;
  kind: "ssh" | "winrm" | "local" | "docker" | "mysql" | "redis";
  name: string;
  host: string | null;
  port: number | null;
  username: string | null;
  authKind: string | null;
  keyPath: string | null;
  credId: string | null;
  options: Record<string, unknown>;
  tags: string;
  note: string;
  sort: number;
  createdAt: number;
  updatedAt: number;
  deletedAt: number | null;
  /**
   * 应用自带的内置资产（「当前设备」）：不可删除，类型锁定，列表里带「本机」徽标。
   *
   * 判据来自内核（`asset.builtin` 列），不在前端按名字或 ID 猜 ——
   * 名字用户可以改，ID 常量写两份迟早会不一致。
   */
  builtin: boolean;
}

export interface AssetGroup {
  id: string;
  parentId: string | null;
  name: string;
  sort: number;
  createdAt: number;
  updatedAt: number;
}

export const assetApi = {
  list: () => call<Asset[]>("asset_list"),
  get: (id: string) => call<Asset>("asset_get", { id }),
  create: (args: Partial<Asset> & { kind: string; name: string }) =>
    call<Asset>("asset_create", { args }),
  update: (args: { id: string } & Record<string, unknown>) =>
    call<Asset>("asset_update", { args }),
  /** 粘贴的私钥落成本地文件（应用数据目录 keys/），返回路径供绑定 keyPath。 */
  saveKeyFile: (content: string) =>
    call<{ path: string }>("asset_save_key_file", { content }),
  /** 读取用户选中的私钥文件内容（存入凭据库用，上限 64KB）。 */
  readKeyFile: (path: string) => call<string>("asset_read_key_file", { path }),
  delete: (id: string) => call<void>("asset_delete", { id }),
  search: (q: string) => call<Asset[]>("asset_search", { q }),
  groupList: () => call<AssetGroup[]>("group_list"),
  groupCreate: (name: string, parentId?: string) =>
    call<AssetGroup>("group_create", { name, parentId }),
  groupUpdate: (id: string, name?: string, parentId?: string | null) =>
    call<AssetGroup>("group_update", { id, name, parentId }),
  groupDelete: (id: string) => call<void>("group_delete", { id }),
  snippetList: () =>
    call<
      { id: string; name: string; body: string; groupId: string | null; sort: number }[]
    >("snippet_list"),
  snippetCreate: (name: string, body: string) =>
    call<{ id: string }>("snippet_create", { name, body }),
  snippetUpdate: (id: string, name: string, body: string) =>
    call<void>("snippet_update", { id, name, body }),
  snippetDelete: (id: string) => call<void>("snippet_delete", { id }),
  auditQuery: (args: Record<string, unknown> = {}) =>
    call<import("./types").AuditEntryDto[]>("audit_query", { args }),
  knownHostList: () => call<import("./types").KnownHostDto[]>("known_host_list"),
  knownHostAccept: (host: string, port: number, keyType: string, fingerprint: string) =>
    call<void>("known_host_accept", { host, port, keyType, fingerprint }),
  knownHostRemove: (id: string) => call<void>("known_host_remove", { id }),
  appInfo: () =>
    call<{ name: string; version: string; vault: import("./types").VaultStatusDto }>(
      "app_info",
    ),
};

// ───────── batch（批量执行命令）─────────

/** 一台主机上执行一条命令的结果（`batch_exec` 的返回项）。 */
export interface BatchExecRow {
  assetId: string;
  name: string;
  host: string;
  ok: boolean;
  exitCode: number | null;
  stdout: string;
  stderr: string;
  error: string | null;
  durationMs: number;
}

/**
 * 批量执行：把同一条命令并行投放到多台资产上。
 *
 * 前端只负责选资产 + 起参数，并发调度与超时在**内核**里做 ——
 * 前端并发跑 N 个 IPC 既难统一超时，也会把每台的错误分片成互相独立的 toast。
 */
export const batchApi = {
  exec: (args: { assetIds: string[]; command: string; timeoutMs?: number; concurrency?: number }) =>
    call<BatchExecRow[]>("batch_exec", { args }),
};

// ───────── fs ─────────

/**
 * Windows 本地会话返回的路径是 `C:\Users\x\y`，而前端（左栏文件树、宽幅浏览器、
 * 面包屑）一律按 `/` 做路径算术。混用两种分隔符的后果是：`Users\WinCore` 被当成一段、
 * `parentOf` 找不到分隔符就跳回根。这里统一成 `/` ——
 * Windows 的文件 API 本来就接受正斜杠，回传也不会有问题。
 */
function normEntryPath<T extends { path: string }>(e: T): T {
  return { ...e, path: e.path.replace(/\\/g, "/") };
}

export const fsApi = {
  list: async (sessionId: string, path: string) => {
    const list = await call<import("./types").FileEntryDto[]>("fs_list", {
      sessionId,
      path,
    });
    return list.map(normEntryPath);
  },
  read: (sessionId: string, path: string, maxBytes?: number) =>
    call<{ path: string; size: number; contentBase64: string }>("fs_read", {
      sessionId,
      path,
      maxBytes,
    }),
  write: (
    sessionId: string,
    path: string,
    contentBase64: string,
    backup = true,
  ) => call<void>("fs_write", { args: { sessionId, path, contentBase64, backup } }),
  mkdir: (sessionId: string, path: string) =>
    call<void>("fs_mkdir", { sessionId, path }),
  rename: (sessionId: string, from: string, to: string) =>
    call<void>("fs_rename", { sessionId, from, to }),
  delete: (sessionId: string, path: string, isDir: boolean) =>
    call<void>("fs_delete", { sessionId, path, isDir }),
  chmod: (sessionId: string, path: string, mode: number) =>
    call<void>("fs_chmod", { sessionId, path, mode }),
  checksum: (sessionId: string, path: string, algo?: string) =>
    call<string>("fs_checksum", { sessionId, path, algo }),
  upload: (sessionId: string, localPath: string, remotePath: string, resume?: boolean) =>
    call<number>("fs_upload", { sessionId, localPath, remotePath, resume }),
  download: (sessionId: string, remotePath: string, localPath: string) =>
    call<number>("fs_download", { sessionId, remotePath, localPath }),
  /** 打包下载目录：远端 tar.gz → 本地文件，返回字节数。 */
  packDownload: (sessionId: string, remotePath: string, localPath: string) =>
    call<number>("fs_pack_download", { sessionId, remotePath, localPath }),
  /** 解压远端压缩包，返回真实落地目录。 */
  extract: (sessionId: string, path: string) =>
    call<string>("fs_extract", { sessionId, path }),
};

// ───────── mount ─────────

export const mountApi = {
  /** 本机是否支持磁盘挂载；返回原因（字符串）表示暂不可用。 */
  capability: () => call<string | null>("mount_capability"),
  list: (forceRefresh?: boolean) =>
    call<import("./types").MountEntryDto[]>("mount_list", { forceRefresh }),
  create: (args: {
    sessionId: string;
    remotePath: string;
    localPoint: string;
    username?: string;
    password?: string;
  }) => call<import("./types").MountEntryDto>("mount_create", { args }),
  remove: (localPoint: string, sessionId?: string) =>
    call<void>("mount_remove", { localPoint, sessionId }),
};

// ───────── docker ─────────

export interface ContainerSummary {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  ports: string;
  composeProject: string | null;
}

export interface ImageSummary {
  id: string;
  repository: string;
  tag: string;
  size: string;
  createdSince: string;
}

export const dockerApi = {
  overview: (sessionId: string) =>
    call<{ containers: ContainerSummary[]; hostStats: Record<string, number> }>(
      "docker_overview",
      { sessionId },
    ),
  ps: (sessionId: string) => call<ContainerSummary[]>("docker_ps", { sessionId }),
  logsAttach: (
    sessionId: string,
    containerId: string,
    tail: number,
    channel: unknown,
  ) =>
    call<string>("docker_logs_attach", {
      sessionId,
      containerId,
      tail,
      channel,
      clientId: clientId(),
    }),
  execAttach: (
    sessionId: string,
    containerId: string,
    cols: number,
    rows: number,
    channel: unknown,
    cmd?: string,
  ) =>
    // 入参收成一个 `args` 对象：Rust 侧为了不超过 clippy 的参数上限把签名收成了
    // `DockerExecArgs`（`channel` 必须留在签名上，宏要特殊处理它）。
    call<string>("docker_exec_attach", {
      args: { sessionId, containerId, cmd, cols, rows, clientId: clientId() },
      channel,
    }),
  action: (sessionId: string, containerId: string, action: string, newName?: string) =>
    call<void>("docker_action", { args: { sessionId, containerId, action, newName } }),
  images: (sessionId: string) => call<ImageSummary[]>("docker_images", { sessionId }),
  imagePull: (sessionId: string, image: string) =>
    call<string>("docker_image_pull", { sessionId, image }),
  imageRemove: (sessionId: string, image: string, force?: boolean) =>
    call<void>("docker_image_remove", { sessionId, image, force }),
  inspect: (sessionId: string, containerId: string) =>
    call<Record<string, unknown>>("docker_inspect", { sessionId, containerId }),
  stats: (sessionId: string) => call<string>("docker_stats", { sessionId }),
  containerListDir: (sessionId: string, containerId: string, path: string) =>
    call<string[]>("docker_container_list_dir", { sessionId, containerId, path }),
};

// ───────── db ─────────

export interface QueryResult {
  columns: string[];
  rows: unknown[][];
  rowsAffected: number;
  durationMs: number;
  truncated: boolean;
  error: string | null;
}

/** 表字段（对应 Rust 侧 information_schema.columns 的投影）。 */
export interface TableColumn {
  name: string;
  type: string;
  nullable: boolean;
  key: string;
  default: string | null;
  extra: string;
}

/** 表索引（对应 information_schema.statistics）。 */
export interface TableIndex {
  name: string;
  unique: boolean;
  column: string;
  seq: number;
}

/** db_columns 的返回：字段 + 索引。 */
export interface TableDescribe {
  columns: TableColumn[];
  indexes: TableIndex[];
}

export const dbApi = {
  connect: (assetId?: string, inline?: Record<string, unknown>) =>
    call<{ connId: string }>("db_connect", { args: { assetId, inline } }),
  disconnect: (connId: string) => call<void>("db_disconnect", { connId }),
  schemas: (connId: string) => call<string[]>("db_schemas", { connId }),
  tables: (connId: string, schema?: string) =>
    call<string[]>("db_tables", { connId, schema }),
  columns: (connId: string, table: string, schema?: string) =>
    call<TableDescribe>("db_columns", { connId, table, schema }),
  query: (connId: string, sql: string, timeoutMs?: number) =>
    call<QueryResult>("db_query", { connId, sql, timeoutMs }),
  redisScan: (connId: string, cursor?: number, pattern?: string, count?: number) =>
    call<[number, string[]]>("redis_scan", { connId, cursor, pattern, count }),
  redisInspect: (connId: string, key: string) =>
    call<import("./types").RedisKeyViewDto>("redis_inspect", { connId, key }),
  redisCommand: (connId: string, args: string[]) =>
    call<string>("redis_command", { connId, args }),
  redisSetTtl: (connId: string, key: string, seconds: number) =>
    call<void>("redis_set_ttl", { connId, key, seconds }),
};

// ───────── ai ─────────

export interface ProviderConfig {
  baseUrl: string;
  apiKey: string;
  model: string;
  temperature: number;
  contextWindow: number;
  proxy: string | null;
  stream: boolean;
}

/** 常规 AI 的权限档位（终端接管是独立模块，不在这里）。 */
export type AiPermissionMode = "read_only" | "read_write" | "silent";

export interface AiPermissionConfig {
  mode: AiPermissionMode;
  /**
   * 自定义危险规则，**子串匹配**（大小写不敏感）。
   *
   * 命中即视为危险命令：任何档位都要问用户 —— 包括「完全静默」。
   */
  dangerRules: string[];
}

export const aiApi = {
  chat: (
    args: {
      conversationId?: string;
      scope: Record<string, unknown>;
      message: string;
      selection?: string;
      /** 图片附件（裸 base64 或 data URI）。 */
      images?: string[];
      /** 计划模式：只做只读调研并把方案交出来，等批准后再动手。 */
      planMode?: boolean;
      channel: unknown;
    },
  ) => {
    // 内核侧把请求体收成了单个 `args` 参数（那边平铺参数超了 clippy 上限 7），
    // channel 单独走顶层 —— Tauri 的 Channel 不能塞进普通结构体里。
    const { channel, ...body } = args;
    // 返回值必须带 `conversationId`：首轮是内核新建的会话，前端要把它记住，
    // 否则下一句追问会被当成新会话（现象：同一个会话里每追问一次就多一个历史项）。
    return call<{ jobId: string; conversationId: string }>("ai_chat", { args: body, channel });
  },
  /** 权限档位 + 自定义危险规则。 */
  getPermission: () => call<AiPermissionConfig>("ai_get_permission"),
  setPermission: (config: AiPermissionConfig) =>
    call<void>("ai_set_permission", { config }),
  cancel: (jobId: string) => call<void>("ai_cancel", { jobId }),
  confirm: (jobId: string, decision: "allow" | "allow_session" | "deny") =>
    call<void>("ai_confirm", { jobId, decision }),
  models: () => call<string[]>("ai_models"),
  testProvider: () =>
    call<{ modelsOk: boolean; modelsError?: string; chatOk: boolean; chatError?: string }>(
      "ai_test_provider",
    ),
  setProvider: (config: ProviderConfig) =>
    call<void>("ai_set_provider", { config }),
  getProvider: () => call<ProviderConfig>("ai_get_provider"),
  presets: () => call<string[]>("ai_presets"),
  takeoverEnter: (tabId: string, allowWrite: boolean) =>
    call<void>("ai_takeover_enter", { args: { tabId, allowWrite } }),
  takeoverExit: (tabId: string, reason?: string) =>
    call<void>("ai_takeover_exit", { tabId, reason }),
  takeoverRun: (args: {
    tabId: string;
    instruction: string;
    allowWrite: boolean;
    channel: unknown;
  }) => call<string>("ai_takeover_run", args),
  conversationList: () =>
    call<import("./types").ConversationDto[]>("ai_conversation_list"),
  conversationDelete: (id: string) => call<void>("ai_conversation_delete", { id }),
  conversationCreate: (title?: string) =>
    call<import("./types").ConversationDto>("ai_conversation_create", { title }),
  messages: (conversationId: string) =>
    call<import("./types").MessageDto[]>("ai_messages", { conversationId }),
};

// ───────── ai models（多模型档案，P0-3）─────────

// 类型由 ts-rs 从内核 `ai::profiles` 生成（跑 `cargo test` 导出到 types.ts）——
// 别在这里手抄一份，字段一改就两边漂移。
import type { ModelProfile, ModelProfilesView } from "./types";
export type { ModelProfile, ModelProfilesView };

export const modelApi = {
  overview: () =>
    call<ModelProfilesView | null>("ai_model_profiles").then(
      (v) => v ?? { profiles: [], activeId: null },
    ),
  save: (profile: ModelProfile) => call<ModelProfile>("ai_model_save", { profile }),
  remove: (id: string) => call<void>("ai_model_delete", { id }),
  activate: (id: string) => call<void>("ai_model_activate", { id }),
  /**
   * 按**表单当前值**去拉模型列表，不要求先保存 ——
   * 用户想先验证 baseUrl + key 能不能连，这个顺序必须支持。
   */
  refresh: (profile: ModelProfile) =>
    call<string[] | null>("ai_model_refresh", { profile }).then((v) => v ?? []),
  presets: () => call<string[] | null>("ai_presets").then((v) => v ?? []),
  preset: (name: string) => call<ModelProfile>("ai_model_preset", { preset: name }),
};

// ───────── vault ─────────

export interface VaultStatus {
  initialized: boolean;
  mode: "not_init" | "dpapi" | "master";
  unlocked: boolean;
  autoLockMinutes: number;
}

/** 「被谁使用」：凭据页的核心信息（asset.cred_id 反查）。 */
export interface CredentialUsedBy {
  id: string;
  name: string;
  kind: string;
}

/**
 * 私钥的来源。
 *
 * `inline` = 正文收进凭据库；`file` = 只记本地文件路径（引用，不复制内容）。
 * 口令永远跟私钥存在同一条凭据里，不是独立凭据。
 */
export type CredentialSource = "inline" | "file";

/** 凭据页用的列表项：Meta + 引用关系 + 私钥来源（后端返回 DTO 而不是 Row）。 */
export interface Credential {
  id: string;
  name: string;
  kind: string;
  createdAt: number;
  updatedAt: number;
  usedBy: CredentialUsedBy[];
  /** 仅私钥类有值；**锁定态为 null**（来源藏在密文里，解不开就不显示） */
  source: CredentialSource | null;
  /** 引用型私钥的本地路径 */
  refPath: string | null;
  hasPassphrase: boolean;
}

/** 凭据明文（「显示」用）。私钥类会把来源、路径、口令一并给出来。 */
export interface RevealedCredential {
  kind: string;
  /** 非私钥：值本身；内容型私钥：正文；引用型私钥：空串（库里本来就没有正文） */
  value: string;
  source: CredentialSource | null;
  refPath: string | null;
  passphrase: string | null;
}

export const vaultApi = {
  status: () => call<VaultStatus>("vault_status"),
  initMaster: (password: string) => call<void>("vault_init_master", { password }),
  initDpapi: () => call<void>("vault_init_dpapi"),
  unlock: (password: string) => call<void>("vault_unlock", { password }),
  lock: () => call<void>("vault_lock"),
  changePassword: (oldPassword: string, newPassword: string) =>
    call<void>("vault_change_password", { oldPassword, newPassword }),
  /**
   * 存凭据。
   *
   * `extra.source` 仅私钥有意义：`file` 时 `secret` 是**路径**（只记引用），
   * 否则是私钥正文。`extra.passphrase` 也是私钥专用，随私钥一起加密保存。
   */
  setCredential: (
    name: string,
    kind: string,
    secret: string,
    extra: { id?: string; source?: CredentialSource; passphrase?: string } = {},
  ) =>
    call<{ id: string }>("vault_set_credential", {
      args: {
        id: extra.id,
        name,
        kind,
        secret,
        source: extra.source,
        passphrase: extra.passphrase,
      },
    }),
  listCredentials: () => call<Credential[]>("vault_list_credentials"),
  deleteCredential: (id: string) => call<void>("vault_delete_credential", { id }),
  revealCredential: (id: string) => call<RevealedCredential>("vault_reveal_credential", { id }),
  /**
   * 改名 / 改值 / 改口令。
   *
   * ⚠️ `passphrase` 的语义是**提供即覆盖**（传空串 = 清除口令）；要"保持原样"就别带这个字段。
   * 只改口令时不必重新提供私钥 —— 后端会解出原载荷、只换口令再加密。
   */
  updateCredential: (
    id: string,
    patch: {
      name?: string;
      secret?: string;
      source?: CredentialSource;
      passphrase?: string;
    },
  ) => call<void>("credential_update", { args: { id, ...patch } }),
};

// ───────── port forward ─────────

export const forwardApi = {
  /**
   * 转发能力自述：本平台允不允许转发、以及监听地址是哪个。
   *
   * 界面拿它决定要不要显示「本平台暂不可用」的提示条，以及地址该写成
   * `127.0.0.1` 还是 `0.0.0.0` —— 这两个值只有内核知道（编译形态 + 平台标识），
   * 前端不猜。进程内不会变，所以取一次就够。
   */
  env: () => call<import("./types").ForwardEnvDto>("forward_env"),
  create: (sessionId: string, listenPort: number, targetHost: string, targetPort: number) =>
    call<import("./types").ForwardSpecDto>("forward_create", {
      sessionId,
      listenPort,
      targetHost,
      targetPort,
    }),
  /**
   * SOCKS5 动态转发：在监听地址上起一个 SOCKS5 代理，目标由客户端当场指定。
   *
   * 只有 SSH 会话能建。⚠️ 这个代理本身**无认证**：桌面形态绑回环所以只给本机用；
   * 服务端形态绑 `0.0.0.0`，能连上它的任何人都能借这条 SSH 会话逛内网。
   */
  createSocks: (sessionId: string, listenPort: number) =>
    call<import("./types").ForwardSpecDto>("forward_create_socks", {
      sessionId,
      listenPort,
    }),
  list: () => call<import("./types").ForwardSpecDto[]>("forward_list"),
  remove: (id: string) => call<void>("forward_remove", { id }),
};

// ───────── sync（跨实例资产同步）─────────

/**
 * 桌面 ↔ 服务端（微服上那个 / 自建服务器上那个）之间搬运资产。
 *
 * 三类命令，边界很清楚：
 * - `digest` / `export` / `import` 是**两端共用**的本地操作（服务端靠它们接收推送）；
 * - `linkGet` / `linkSet` / `remoteDigest` / `push` / `pull` 只在桌面版有意义
 *   （服务端上会返回 `unsupported`）；
 * - `token` / `rotateToken` 只在服务端有值（桌面版回 `null`）。
 *
 * 连接**只有一种凭据**：服务端自己生成的那串同步令牌，抄到桌面版即可。
 * 服务端装在哪里由 `linkSet` 的 `tokenKind`（`box` | `server`）标注，
 * 它只影响界面提示，不影响协议。
 *
 * `push` / `pull` 的方向是**显式**的：用户勾哪些、按哪个按钮，就往哪个方向走。
 * 没有自动合并 —— 冲突（两边都改过）由界面呈现，由人决定。
 */
export const syncApi = {
  /** 本机摘要（不含任何密文），用于对照界面。 */
  digest: () => call<import("./types").SyncDigest>("sync_digest"),
  origin: () => call<string>("sync_origin"),

  /** 导出选中资产为同步包（本地操作，不走网络）。 */
  exportAssets: (assetIds: string[], withCreds: boolean) =>
    call<unknown>("sync_export", { args: { assetIds, withCreds } }),

  /** 应用一个同步包（本地操作）。 */
  importBundle: (bundle: unknown, force: boolean) =>
    call<import("./types").ImportReport>("sync_import", { args: { bundle, force } }),

  // ── 仅桌面 ──

  linkGet: () => call<import("./types").SyncLink>("sync_link_get"),
  /**
   * 保存连接配置。`token` 缺省 = 不改动已存的令牌（界面留空时不覆盖）。
   */
  linkSet: (patch: { url: string; tokenKind?: string; token?: string; insecure?: boolean }) =>
    call<import("./types").SyncLink>("sync_link_set", { args: patch }),

  /** 拉对端摘要 —— 同时就是连通性探测，结果会写回连接配置。 */
  remoteDigest: () => call<import("./types").SyncDigest>("sync_remote_digest"),

  /** 推送选中资产到对端。 */
  push: (assetIds: string[], withCreds: boolean, force: boolean) =>
    call<import("./types").ImportReport>("sync_push", { args: { assetIds, withCreds, force } }),
  /** 从对端拉取选中资产到本地。 */
  pull: (assetIds: string[], withCreds: boolean, force: boolean) =>
    call<import("./types").ImportReport>("sync_pull", { args: { assetIds, withCreds, force } }),

  // ── 仅服务端 ──

  /** 本机同步令牌（桌面版回 null）。 */
  token: () => call<string | null>("sync_token"),
  /** 换一个同步令牌，旧令牌立即失效。 */
  rotateToken: () => call<string | null>("sync_token_rotate"),
};

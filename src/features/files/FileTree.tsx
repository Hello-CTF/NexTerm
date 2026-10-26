import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type DragEvent as ReactDragEvent, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent as ReactMouseEvent } from "react";
import { defaultRangeExtractor, useVirtualizer, type Range } from "@tanstack/react-virtual";
import { useQueries, useQuery, useQueryClient, type QueryClient, type UseQueryResult } from "@tanstack/react-query";
import type { FileEntryDto } from "../../ipc/types";
import { fsApi, sessionApi, terminalApi } from "../../ipc/commands";
import {
  ask,
  discardStaged,
  finishSave,
  pickLocalFile,
  pickSavePath,
  promptText,
} from "../../ui/dialogs";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { describeError } from "../../ui/errorText";
import { copyText } from "../../ui/clipboard";
import "../../ui/skeleton.css";
import {
  findWritableTerminal,
  openFileTab,
  openFileTabInSplit,
  openLogTab,
  openTerminalTab,
  reconnectDeadHost,
  useUi,
} from "../../app/store";
import { useCoarsePointer } from "../../app/shell";
import { fileVisual, formatSize, isEditableFile, isExtractableArchive } from "./fileTypes";
import {
  HOME,
  baseName,
  crumbsOf,
  directoryCommandFor,
  type DirectoryCommandFailure,
  fsErrorResource,
  isRoot,
  joinPath,
  norm,
  normalizeTypedPath,
  parentOf,
} from "./pathUtils";
import { useFileOps } from "./useFileOps";
import { packDownloadSupported, validateEntryName } from "./fileOps";
import { checkUploadOverwrite } from "./uploadConfirm";
import { useCwdFollow } from "./cwdFollow";
import { dropFolderUpload, pickFolderUpload } from "./folderUpload";
import { noteHtml5Drop, useNativeFileDrop } from "./nativeDrop";
import { enqueueDownload, enqueueUploads, subscribeTransferSettled } from "./transferStore";
import { createTransferTaskId } from "./transferTaskId";
import {
  IconArchive,
  IconArrowUp,
  IconChevronDown,
  IconChevronRight,
  IconClose,
  IconDownload,
  IconDrive,
  IconEdit,
  IconEye,
  IconFilePlus,
  IconFoldAll,
  IconFolderPlus,
  IconHome,
  IconLocate,
  IconLock,
  IconRefresh,
  IconShieldCheck,
  IconTerminal,
  IconTrash,
  IconUpload,
  IconXCircle,
} from "../../ui/icons";

// localeCompare with locale options builds a collator per comparison, which costs hundreds of milliseconds on
// directories with thousands of entries; one shared collator gives the same order.
const entryCollator = new Intl.Collator("en", { numeric: true });
// Listings keep their reference until refetched, so each one is sorted once instead of on every render.
const sortedListings = new WeakMap<FileEntryDto[], FileEntryDto[]>();

function sortEntries(list: FileEntryDto[]): FileEntryDto[] {
  const cached = sortedListings.get(list);
  if (cached) return cached;
  const sorted = [...list].sort((a, b) => {
    const ad = a.kind === "dir" ? 0 : 1;
    const bd = b.kind === "dir" ? 0 : 1;
    if (ad !== bd) return ad - bd;
    return entryCollator.compare(a.name, b.name);
  });
  sortedListings.set(list, sorted);
  return sorted;
}

// A stable combined result keeps the directory map and rows memoized across unrelated re-renders.
function combineListings(list: UseQueryResult<FileEntryDto[]>[]) {
  return list.map((result) => ({ data: result.data, error: result.error, isLoading: result.isLoading }));
}

const LISTING_STALE_MS = 30_000;
// Listings outlive the tree's unmount (another workspace in front) long enough to be shown again without a remote read.
const LISTING_GC_MS = 30 * 60_000;

// The view a tree had when its workspace was left, by session: showing the workspace again restores it instead of starting at home.
// Views live beside the query cache whose listings they show again.
interface TreeView {
  root: string | null;
  expanded: string[];
  selected: string | null;
}
const treeViewsByClient = new WeakMap<QueryClient, Map<string, TreeView>>();

function treeViews(client: QueryClient): Map<string, TreeView> {
  let views = treeViewsByClient.get(client);
  if (!views) {
    views = new Map();
    treeViewsByClient.set(client, views);
  }
  return views;
}

function saveTreeView(client: QueryClient, sessionId: string, view: TreeView) {
  const views = treeViews(client);
  const live = new Set(useUi.getState().sessions.map((session) => session.id));
  for (const id of views.keys()) if (!live.has(id)) views.delete(id);
  views.set(sessionId, view);
}
const TREE_ROW_HEIGHT = 28;
// Rows just outside the viewport that stay mounted; the window is the visible rows plus a few on each side.
const TREE_OVERSCAN = 4;
const ERROR_ROW_HEIGHT = 56;
const TREE_VIRTUALIZE_THRESHOLD = 60;

type TreeRowActions = {
  toggle: (path: string) => void;
  select: (path: string) => void;
  enterDir: (path: string) => void;
  openFile: (entry: FileEntryDto) => void;
  openRowMenuAt: (entry: FileEntryDto, listKey: string, x: number, y: number, rootEntry?: boolean) => void;
  openRowMenu: (e: ReactMouseEvent<HTMLDivElement>, entry: FileEntryDto, listKey: string, rootEntry?: boolean) => void;
  download: (path: string) => Promise<void>;
  remove: (path: string) => Promise<void>;
  retryDir: (dir: string) => void;
  reconnect: () => void;
  navigate: (event: ReactKeyboardEvent<HTMLElement>, path: string) => void;
  noteFocus: (path: string) => void;
};

type TreeRowProps = {
  entry?: FileEntryDto;
  depth: number;
  error?: { message: string; gone: boolean };
  dir: string;
  rootEntry?: boolean;
  open: boolean;
  isSel: boolean;
  coarse: boolean;
  reconnecting: boolean;
  setSize: number;
  posInSet: number;
  actions: TreeRowActions;
};

// Memoized so a selection or expand change only re-renders the rows whose own props changed.
const TreeRow = memo(function TreeRow({
  entry,
  depth,
  error,
  dir,
  rootEntry,
  open,
  isSel,
  coarse,
  reconnecting,
  setSize,
  posInSet,
  actions,
}: TreeRowProps) {
  if (error) {
    return (
      <div
        className="mx-1 my-0.5 flex items-start gap-1.5 rounded border border-red-500/30 bg-red-950/30 px-2 py-1.5"
        style={{ marginLeft: 2 + depth * 12 }}
      >
        <IconXCircle size={12} className="mt-0.5 shrink-0 text-red-300" />
        <div className="min-w-0 flex-1 text-[11px] leading-relaxed text-red-200">
          <div className="break-words">
            {error.gone
              ? "会话已在服务端删除，重试不会恢复；重新连接这台主机后在此继续浏览"
              : error.message}
          </div>
          <div className="flex gap-2">
            {!error.gone && (
              <button className="nx-link mt-0.5 text-[11px]" onClick={() => actions.retryDir(dir)}>
                重试
              </button>
            )}
            {error.gone && (
              <button
                className="nx-link mt-0.5 text-[11px]"
                disabled={reconnecting}
                onClick={() => actions.reconnect()}
              >
                {reconnecting ? "正在重新连接…" : "重新连接这台主机"}
              </button>
            )}
          </div>
        </div>
      </div>
    );
  }
  if (!entry) return null;
  const isDir = entry.kind === "dir";
  const { Icon, tone } = fileVisual(entry.name, entry.kind);
  return (
    <div
      role="treeitem"
      aria-level={depth + 1}
      aria-expanded={isDir ? open : undefined}
      aria-selected={isSel}
      aria-setsize={setSize}
      aria-posinset={posInSet}
      data-row-path={entry.path}
      tabIndex={0}
      data-drop-path={isDir ? entry.path : undefined}
      className={`nx-row nx-row-oneline nx-row-reserve-actions nx-ftree-row ${isSel ? "is-selected" : ""}`}
      style={{ paddingLeft: 2 + depth * 12 }}
      onClick={() => {
        actions.select(entry.path);
        if (isDir) actions.toggle(entry.path);
      }}
      onDoubleClick={() => (isDir ? actions.enterDir(entry.path) : actions.openFile(entry))}
      onKeyDown={(e) => {
        if (e.target !== e.currentTarget) return;
        if (e.key === "Enter") {
          e.preventDefault();
          if (isDir) actions.enterDir(entry.path);
          else actions.openFile(entry);
          return;
        }
        if (e.key === " ") {
          e.preventDefault();
          actions.select(entry.path);
          if (isDir) actions.toggle(entry.path);
          return;
        }
        if ((e.key === "F10" && e.shiftKey) || e.key === "ContextMenu") {
          e.preventDefault();
          const r = e.currentTarget.getBoundingClientRect();
          actions.openRowMenuAt(entry, dir, r.left, r.bottom, rootEntry);
          return;
        }
        if (e.key === "ArrowRight" && isDir && !open) {
          e.preventDefault();
          actions.toggle(entry.path);
          return;
        }
        if (e.key === "ArrowLeft" && isDir && open) {
          e.preventDefault();
          actions.toggle(entry.path);
          return;
        }
        actions.navigate(e, entry.path);
      }}
      onFocus={() => actions.noteFocus(entry.path)}
      onContextMenu={(e) => actions.openRowMenu(e, entry, dir, rootEntry)}
      title={entry.kind === "symlink" ? `${entry.path} → ${entry.symlinkTarget}` : entry.path}
    >
      {isDir ? (
        <button
          className="nx-tree-caret"
          onClick={(e) => {
            e.stopPropagation();
            actions.toggle(entry.path);
          }}
          title={open ? "收起" : "展开"}
          aria-label={`${open ? "收起" : "展开"} ${entry.name}`}
          aria-expanded={open}
        >
          {open ? <IconChevronDown size={11} /> : <IconChevronRight size={11} />}
        </button>
      ) : (
        <span className="w-[14px] shrink-0" />
      )}
      {rootEntry ? (
        <IconDrive size={13} className={`shrink-0 ${tone}`} />
      ) : (
        <Icon size={13} className={`shrink-0 ${tone}`} />
      )}
      <span className="nx-row-name min-w-0 flex-auto truncate text-[12.5px]">{entry.name}</span>
      {coarse ? (
        <button
          className="nx-icon-btn nx-icon-btn-sm nx-row-more"
          title={`更多操作 ${entry.name}`}
          aria-label={`更多操作 ${entry.name}`}
          onClick={(e) => {
            e.stopPropagation();
            const r = e.currentTarget.getBoundingClientRect();
            actions.openRowMenuAt(entry, dir, r.left, r.bottom, rootEntry);
          }}
          onDoubleClick={(e) => e.stopPropagation()}
        >
          ⋯
        </button>
      ) : (
        <span className="nx-row-actions">
          {!isDir && (
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              title="下载"
              aria-label={`下载 ${entry.name}`}
              onClick={(e) => {
                e.stopPropagation();
                void actions.download(entry.path);
              }}
            >
              <IconDownload size={11} />
            </button>
          )}
          {!rootEntry && (
            <button
              className="nx-icon-btn nx-icon-btn-sm is-danger"
              title="删除"
              aria-label={`删除 ${entry.name}`}
              onClick={(e) => {
                e.stopPropagation();
                void actions.remove(entry.path);
              }}
            >
              <IconTrash size={11} />
            </button>
          )}
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title={`更多操作 ${entry.name}`}
            aria-label={`更多操作 ${entry.name}`}
            onClick={(e) => {
              e.stopPropagation();
              const r = e.currentTarget.getBoundingClientRect();
              actions.openRowMenuAt(entry, dir, r.left, r.bottom, rootEntry);
            }}
          >
            ⋯
          </button>
        </span>
      )}
    </div>
  );
});

export function FileTree({ sessionId }: { sessionId: string }) {
  return <FileTreeInner sessionId={sessionId} />;
}

function FileTreeInner({ sessionId }: { sessionId: string }) {
  const qc = useQueryClient();
  const pushToast = useUi((s) => s.pushToast);
  const leftOpen = useUi((s) => s.leftOpen);
  const leftWidth = useUi((s) => s.leftWidth);
  const sessionKind = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.kind);
  const packHidden = sessionKind !== undefined && !packDownloadSupported(sessionKind);
  const [initialView] = useState(() => treeViews(qc).get(sessionId));
  const [rootChoice, setRoot] = useState<string | null>(initialView?.root ?? null);
  const [expanded, setExpanded] = useState<string[]>(initialView?.expanded ?? []);
  const [selected, setSelected] = useState<string | null>(initialView?.selected ?? null);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const crumbRef = useRef<HTMLDivElement>(null);
  const fileOps = useFileOps(sessionId);
  const coarse = useCoarsePointer();
  const rootsQuery = useQuery({
    queryKey: ["fsRoots", sessionId],
    queryFn: () => fsApi.roots(sessionId),
    staleTime: LISTING_STALE_MS,
    gcTime: LISTING_GC_MS,
    refetchOnMount: initialView === undefined,
    enabled: typeof fsApi.roots === "function",
    refetchOnWindowFocus: false,
    retry: false,
  });
  const rootsData = rootsQuery.data;
  const pathStyle = rootsData?.pathStyle ?? "unknown";
  const windowsComputer = rootsData?.supported === true && pathStyle === "windows";
  const environmentSettled =
    typeof fsApi.roots !== "function" || rootsQuery.isSuccess || rootsQuery.isError;
  const rootsSessionGone = fsErrorResource(rootsQuery.error) === "session";
  const root = rootChoice ?? (windowsComputer ? null : sessionKind === "winrm" ? "/" : HOME);
  const computer = root === null && windowsComputer;
  const homeTarget = rootsData?.homePath ?? HOME;
  const homeSupported = sessionKind !== "winrm" || rootsData?.homePath != null;
  const follow = useCwdFollow(sessionId, root ?? "\u0000", `tree:${sessionId}`, (next) => {
    setExpanded([]);
    setSelected(null);
    setRoot(next);
  });

  useEffect(() => {
    const el = crumbRef.current;
    if (el) el.scrollLeft = el.scrollWidth;
  }, [root, editing]);

  useEffect(() => {
    if (sessionKind !== "winrm" || root !== HOME || windowsComputer) return;
    setExpanded([]);
    setSelected(null);
    setRoot("/");
  }, [sessionKind, root, windowsComputer]);

  useEffect(
    () =>
      subscribeTransferSettled((task) => {
        if (task.sessionId !== sessionId) return;
        void qc.invalidateQueries({ queryKey: ["fs", sessionId] });
      }),
    [qc, sessionId],
  );

  const beginEdit = () => {
    setDraft(root ?? "");
    setEditing(true);
  };

  const commitEdit = () => {
    const next = normalizeTypedPath(draft, pathStyle);
    if (!next) {
      if (draft.trim()) {
        pushToast("error", "Windows 驱动器相对路径无法直接跳转，需要绝对路径（如 D:/Projects）");
      }
      setEditing(false);
      return;
    }
    setEditing(false);
    if (next === root) return;
    setExpanded([]);
    setSelected(null);
    setRoot(next);
  };

  const dirs = useMemo(() => {
    if (!environmentSettled || rootsSessionGone) return [];
    if (computer) return expanded;
    if (!root) return [];
    return [root, ...expanded.filter((d) => d !== root)];
  }, [computer, environmentSettled, expanded, root, rootsSessionGone]);

  useEffect(() => {
    saveTreeView(qc, sessionId, { root: rootChoice, expanded, selected });
  }, [qc, sessionId, rootChoice, expanded, selected]);

  // Directories the restored view shows keep their cached listing when the tree mounts again; a directory the user navigates
  // to, or collapses and expands again, reads as usual. Declared before useQueries so the set is current when its observers subscribe.
  const restoredDirs = useRef<Set<string> | null>(initialView ? null : new Set());
  useEffect(() => {
    if (!environmentSettled) return;
    if (restoredDirs.current === null) {
      restoredDirs.current = new Set(dirs);
      return;
    }
    for (const dir of restoredDirs.current) if (!dirs.includes(dir)) restoredDirs.current.delete(dir);
  }, [dirs, environmentSettled]);

  const results = useQueries({
    queries: dirs.map((dir) => ({
      queryKey: ["fs", sessionId, dir],
      queryFn: () => fsApi.list(sessionId, dir),
      // Remounting the panel reuses a fresh listing; invalidateQueries ignores staleTime, so refresh and file operations still refetch.
      staleTime: LISTING_STALE_MS,
      gcTime: LISTING_GC_MS,
      refetchOnMount: () => restoredDirs.current !== null && !restoredDirs.current.has(dir),
      refetchOnWindowFocus: false,
      retry: false,
    })),
    combine: combineListings,
  });

  const dirMap = useMemo(() => {
    const map = new Map<string, FileEntryDto[]>();
    dirs.forEach((dir, i) => {
      const data = results[i]?.data;
      if (data) map.set(dir, sortEntries(data));
    });
    return map;
  }, [dirs, results]);

  const dirErrors = useMemo(() => {
    const map = new Map<string, { message: string; gone: boolean }>();
    dirs.forEach((dir, i) => {
      const err = results[i]?.error;
      if (err) {
        const resource = fsErrorResource(err);
        map.set(dir, {
          message: describeError(err),
          gone: resource === "session",
        });
      }
    });
    return map;
  }, [dirs, results]);

  const sessionGone =
    rootsSessionGone || results.some((result) => fsErrorResource(result.error) === "session");
  const goneHint = "会话已在服务端删除，请重新连接这台主机";
  const [reconnecting, setReconnecting] = useState(false);

  const reconnectHost = useCallback(async () => {
    const ws = useUi.getState().workspaces.find((w) => w.sessionId === sessionId);
    if (!ws) {
      pushToast("error", "找不到这个工作区的主机信息，请到左侧资产树里手动连接");
      return;
    }
    setReconnecting(true);
    try {
      const s = await reconnectDeadHost(ws);
      if (!s) {
        pushToast("info", "已取消重连");
        return;
      }
      const list = useUi.getState().sessions;
      useUi.getState().setSessions([
        ...list.filter((x) => x.id !== sessionId && x.id !== s.id),
        s,
      ]);
      useUi.getState().updateWorkspaceSession(ws.id, s.id);
      pushToast("info", "已重新连接，正在重新加载文件列表");
    } catch (e) {
      pushToast("error", `重新连接失败：${describeError(e)}`);
    } finally {
      setReconnecting(false);
    }
  }, [sessionId, pushToast]);

  const autoReconnectRef = useRef<string | null>(null);
  useEffect(() => {
    if (!sessionGone) {
      autoReconnectRef.current = null;
      return;
    }
    if (autoReconnectRef.current === sessionId) return;
    autoReconnectRef.current = sessionId;
    void reconnectHost();
  }, [sessionGone, sessionId, reconnectHost]);

  const rows = useMemo(() => {
    const out: {
      entry?: FileEntryDto;
      depth: number;
      error?: { message: string; gone: boolean };
      dir: string;
      rootEntry?: boolean;
      setSize: number;
      posInSet: number;
    }[] = [];
    const open = new Set(expanded);
    const walk = (dir: string, depth: number) => {
      const list = dirMap.get(dir) ?? [];
      list.forEach((entry, i) => {
        out.push({ entry, depth, dir, setSize: list.length, posInSet: i + 1 });
        if (entry.kind === "dir" && open.has(entry.path)) {
          walk(entry.path, depth + 1);
        }
      });
      const err = dirErrors.get(dir);
      if (err) out.push({ depth, error: err, dir, setSize: 1, posInSet: 1 });
    };
    if (computer) {
      const realRoots = rootsData?.roots ?? [];
      realRoots.forEach((realRoot, i) => {
        const entry: FileEntryDto = {
          name: realRoot.label || realRoot.path,
          path: realRoot.path,
          kind: "dir",
          size: 0,
          mode: "",
          owner: null,
          group: null,
          mtime: 0,
          symlinkTarget: null,
        };
        out.push({ entry, depth: 0, dir: realRoot.path, rootEntry: true, setSize: realRoots.length, posInSet: i + 1 });
        if (open.has(realRoot.path)) walk(realRoot.path, 1);
      });
    } else if (root) {
      walk(root, 0);
    }
    return out;
  }, [computer, dirMap, root, expanded, dirErrors, rootsData]);

  const retryDir = (dir: string) => {
    void qc.invalidateQueries({ queryKey: ["fs", sessionId, dir] });
  };

  const findEntry = (path: string): FileEntryDto | undefined => {
    for (const list of dirMap.values()) {
      const hit = list.find((e) => e.path === path);
      if (hit) return hit;
    }
    return undefined;
  };

  const selectedEntry = selected ? findEntry(selected) : undefined;

  const navigateRoot = (next: string | null) => {
    setSelected(null);
    if (next === null) {
      setExpanded([]);
      setRoot(null);
      return;
    }
    const normalized = norm(next);
    const prefix = normalized.endsWith("/") ? normalized : `${normalized}/`;
    setExpanded((prev) =>
      prev.filter((path) => {
        const candidate = norm(path);
        return candidate === normalized || candidate.startsWith(prefix);
      }),
    );
    setRoot(next);
  };

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["fsRoots", sessionId] });
    void qc.invalidateQueries({ queryKey: ["fs", sessionId] });
  };

  const toggle = (path: string) =>
    setExpanded((prev) =>
      prev.includes(path) ? prev.filter((p) => p !== path) : [...prev, path],
    );

  const openFile = (entry: FileEntryDto) => {
    if (!isEditableFile(entry.name)) {
      pushToast("info", `「${entry.name}」不是文本文件，不能在线编辑；可以下载到本机后打开`);
      return;
    }
    openFileTab(sessionId, entry.path);
  };

  const targetDir = (): string => {
    if (selectedEntry) {
      if (selectedEntry.kind === "dir") return selectedEntry.path;
      return parentOf(selectedEntry.path) ?? root ?? "";
    }
    return root ?? "";
  };

  const treeDropRef = useRef<HTMLDivElement>(null);
  useNativeFileDrop(sessionId, {
    surface: treeDropRef,
    fallbackDir: targetDir,
    knownSiblings: (dir) => dirMap.get(dir),
    onSettled: () => refresh(),
  });

  const newFileIn = async (dir: string) => {
    const name = await promptText("新建文件名", "");
    if (name === null) return;
    const problem = validateEntryName(name);
    if (problem) {
      pushToast("error", `无法创建：${problem}`);
      return;
    }
    const path = joinPath(dir, name);
    try {
      await fsApi.write(sessionId, path, "", false);
      refresh();
      pushToast("success", `已创建 ${path}`);
    } catch (e) {
      pushToast("error", `创建失败：${describeError(e)}`);
    }
  };

  const newDirIn = async (dir: string) => {
    const name = await promptText("新建目录名", "");
    if (name === null) return;
    const problem = validateEntryName(name);
    if (problem) {
      pushToast("error", `无法创建：${problem}`);
      return;
    }
    const path = joinPath(dir, name);
    try {
      await fsApi.mkdir(sessionId, path);
      refresh();
      if (dir !== root && !expanded.includes(dir)) toggle(dir);
      pushToast("success", `已创建 ${path}`);
    } catch (e) {
      pushToast("error", `创建失败：${describeError(e)}`);
    }
  };

  const uploadTo = async (dir: string) => {
    const file = await pickLocalFile();
    if (!file) return;
    const remote = joinPath(dir, baseName(file));
    let handed = false;
    try {
      const check = await checkUploadOverwrite(sessionId, dir, remote);
      if (check === "cancelled") {
        pushToast("info", `已取消上传，${remote} 保持原样`);
        return;
      }
      pushToast("info", "开始上传…");
      handed = true;
      enqueueUploads(sessionId, [{ localPath: file, remotePath: remote }], dir);
    } catch (e) {
      pushToast("error", `上传失败：${describeError(e)}`);
    } finally {
      if (!handed) await discardStaged(file);
    }
  };

  const uploadFolderTo = async (dir: string) => {
    await pickFolderUpload({
      sessionId,
      dir,
      knownSiblings: dirMap.get(dir),
      onSettled: refresh,
    });
  };

  const download = async (path: string) => {
    const target = await pickSavePath(baseName(path));
    if (!target) return;
    pushToast("info", "开始下载…");
    enqueueDownload(sessionId, { remotePath: path, localPath: target });
  };

  const dropUpload = (event: ReactDragEvent<HTMLDivElement>) => {
    if (!event.dataTransfer.types.includes("Files")) return;
    event.preventDefault();
    noteHtml5Drop();
    const row = (event.target as HTMLElement).closest<HTMLElement>("[data-drop-path]");
    const dir = row?.dataset.dropPath ?? targetDir();
    if (!dir) {
      pushToast("info", "请先选择实际目录再上传");
      return;
    }
    void dropFolderUpload({
      sessionId,
      dir,
      dataTransfer: event.dataTransfer,
      knownSiblings: dirMap.get(dir),
      onSettled: refresh,
    });
  };

  const remove = async (path: string) => {
    const entry = findEntry(path);
    const isDir = entry?.kind === "dir";
    const tip = isDir ? "\n\n目录会被递归删除，不可恢复。" : "";
    if (!(await ask(`删除 ${path}？${tip}`, { kind: "warning" }))) return;
    try {
      await fsApi.delete(sessionId, path, isDir);
      setSelected(null);
      setExpanded((prev) => prev.filter((p) => p !== path));
      refresh();
      pushToast("success", "已删除");
    } catch (e) {
      pushToast("error", `删除失败：${describeError(e)}`);
    }
  };

  const enterDir = (path: string) => {
    if (path === root) return;
    setExpanded([]);
    setSelected(null);
    setRoot(path);
  };

  const packDownload = async (dir: string) => {
    const target = await pickSavePath(`${baseName(dir)}.tar.gz`);
    if (!target) return;
    pushToast("info", "正在远端打包…");
    try {
      const bytes = await fsApi.packDownload(sessionId, dir, target, createTransferTaskId());
      const where = await finishSave(target, `${baseName(dir)}.tar.gz`);
      pushToast(
        where ? "success" : "info",
        where ? `已打包下载到 ${where}（${bytes} 字节）` : "已取消保存",
      );
    } catch (e) {
      pushToast("error", `打包下载失败：${describeError(e)}`);
    }
  };

  const extractHere = async (archive: string) => {
    pushToast("info", "正在解压…");
    try {
      const target = await fsApi.extract(sessionId, archive);
      const parent = parentOf(target) ?? root ?? HOME;
      if (parent !== root) {
        setExpanded((prev) => (prev.includes(parent) ? prev : [...prev, parent]));
      }
      void qc.invalidateQueries({ queryKey: ["fs", sessionId, parent] });
      void qc.invalidateQueries({ queryKey: ["fs", sessionId, target] });
      pushToast("success", `已解压到 ${target}`);
    } catch (e) {
      pushToast("error", `解压失败：${describeError(e)}`);
    }
  };

  const notifyDirectoryFailure = (dir: string, failure: DirectoryCommandFailure | null) => {
    const reason =
      failure === "control"
        ? "目录包含控制字符"
        : failure === "unsupported"
          ? "当前终端 Shell 暂不支持自动切换目录"
          : "无法确认当前终端 Shell";
    void copyText(dir)
      .then(() => {
        pushToast(
          failure === "control" ? "error" : "info",
          `${reason}，未发送命令；路径已复制，请手动切换`,
        );
      })
      .catch((e) => {
        pushToast(
          "error",
          `${reason}，未发送命令；复制路径失败：${describeError(e)}。路径：${dir}`,
        );
      });
  };

  const openTerminalAt = (dir: string) => {
    const session = useUi.getState().sessions.find((s) => s.id === sessionId);
    if (!session) {
      pushToast("error", "当前会话已断开，无法打开终端");
      return;
    }
    if (sessionKind !== "winrm") {
      void openTerminalTab(session, undefined, undefined, { directory: dir });
      return;
    }
    const { command, failure } = directoryCommandFor(dir, "powershell");
    if (!command) {
      notifyDirectoryFailure(dir, failure);
      return;
    }
    const previous = new Set(
      useUi
        .getState()
        .workspaces.flatMap((workspace) => workspace.panes)
        .flatMap((pane) => pane.tabs)
        .filter((tab) => tab.kind === "terminal" && tab.sessionId === sessionId)
        .map((tab) => tab.id),
    );
    void openTerminalTab(session)
      .then(() => {
        const created = useUi
          .getState()
          .workspaces.flatMap((workspace) => workspace.panes)
          .flatMap((pane) => pane.tabs)
          .find(
            (tab) =>
              tab.kind === "terminal" && tab.sessionId === sessionId && !previous.has(tab.id),
          );
        if (!created) {
          pushToast("error", "新终端创建失败，无法切换目录");
          return;
        }
        let finished = false;
        let unsubscribe = () => {};
        const execute = () => {
          if (finished) return;
          const tab = useUi
            .getState()
            .workspaces.flatMap((workspace) => workspace.panes)
            .flatMap((pane) => pane.tabs)
            .find((candidate) => candidate.id === created.id);
          if (!tab?.tabId) return;
          finished = true;
          window.clearTimeout(timeout);
          unsubscribe();
          void sessionApi
            .lineExec(tab.tabId, command)
            .catch((e) => pushToast("error", `切换目录失败：${describeError(e)}`));
        };
        const timeout = window.setTimeout(() => {
          if (finished) return;
          finished = true;
          unsubscribe();
          pushToast("error", "新终端尚未就绪，无法切换目录");
        }, 10000);
        unsubscribe = useUi.subscribe(execute);
        execute();
      })
      .catch((e) => pushToast("error", `打开终端失败：${describeError(e)}`));
  };

  const cdTerminalTo = (dir: string) => {
    const target = findWritableTerminal(sessionId);
    if (!target) {
      pushToast("info", "当前工作区没有可用终端，已新建一个");
      openTerminalAt(dir);
      return;
    }
    const { command, failure } = directoryCommandFor(
      dir,
      sessionKind === "winrm" ? "powershell" : target.shell,
    );
    if (!command) {
      notifyDirectoryFailure(dir, failure);
      return;
    }
    if (sessionKind === "winrm") {
      void sessionApi
        .lineExec(target.tabId, command)
        .catch((e) => pushToast("error", `切换目录失败：${describeError(e)}`));
      return;
    }
    void terminalApi
      .write(target.tabId, new TextEncoder().encode(`${command}\n`))
      .catch((e) => pushToast("error", `切换目录失败：${describeError(e)}`));
  };

  const remapAfterRename = (from: string, to: string) => {
    const fromN = norm(from);
    const toN = norm(to);
    const styled = (target: string, styleOf: string) =>
      styleOf.includes("\\") ? target.replace(/\//g, "\\") : target;
    const remap = (p: string) => {
      const pn = norm(p);
      if (pn === fromN) return styled(toN, p);
      if (pn.startsWith(`${fromN}/`)) return styled(`${toN}${pn.slice(fromN.length)}`, p);
      return p;
    };
    setSelected((cur) => (cur ? remap(cur) : cur));
    setExpanded((prev) => prev.map(remap));
    if (root && (norm(root) === fromN || norm(root).startsWith(`${fromN}/`))) {
      setRoot(remap(root));
    }
  };

  const siblingsOf = (dir: string): FileEntryDto[] => dirMap.get(dir) ?? [];

  const openRowMenuAt = (
    entry: FileEntryDto,
    listKey: string,
    x: number,
    y: number,
    rootEntry = false,
  ) => {
    setSelected(entry.path);
    if (rootEntry) {
      setMenu({
        x,
        y,
        title: entry.path,
        items: [
          { kind: "group", label: "磁盘" },
          { kind: "item", label: "打开此磁盘", onSelect: () => enterDir(entry.path) },
        ],
      });
      return;
    }
    const isDir = entry.kind === "dir";
    const dir = isDir ? entry.path : (parentOf(entry.path) ?? root ?? HOME);
    const items: MenuItem[] = [
      { kind: "group", label: "终端" },
      {
        kind: "item",
        label: "在当前终端打开",
        icon: <IconTerminal size={13} />,
        hint: "已有终端",
        onSelect: () => cdTerminalTo(dir),
      },
      {
        kind: "item",
        label: "在新终端打开",
        icon: <IconTerminal size={13} />,
        hint: "新标签",
        onSelect: () => openTerminalAt(dir),
      },
      { kind: "separator" },
      { kind: "group", label: isDir ? "目录" : "文件" },
    ];
    if (isDir) {
      if (!packHidden) {
        items.push({
          kind: "item",
          label: "打包下载当前目录",
          icon: <IconArchive size={13} />,
          hint: "tar.gz",
          onSelect: () => void packDownload(entry.path),
        });
      }
    } else {
      items.push({
        kind: "item",
        label: "在下方编辑",
        icon: <IconEdit size={13} />,
        disabled: !isEditableFile(entry.name),
        onSelect: () => openFileTabInSplit(sessionId, entry.path),
      });
      items.push({
        kind: "item",
        label: "查看日志",
        icon: <IconEye size={13} />,
        hint: "只读分块",
        disabled: !isEditableFile(entry.name),
        onSelect: () => openLogTab(sessionId, entry.path),
      });
      if (isExtractableArchive(entry.name)) {
        items.push({
          kind: "item",
          label: "解压",
          icon: <IconArchive size={13} />,
          hint: "到同名目录",
          onSelect: () => void extractHere(entry.path),
        });
      }
      items.push({
        kind: "item",
        label: "下载当前文件",
        icon: <IconDownload size={13} />,
        onSelect: () => void download(entry.path),
      });
    }
    items.push(
      {
        kind: "item",
        label: "重命名",
        icon: <IconEdit size={13} />,
        disabled: fileOps.busy !== null,
        onSelect: () =>
          void fileOps.renameEntry(entry, siblingsOf(listKey), listKey, remapAfterRename),
      },
      {
        kind: "item",
        label: "权限…",
        icon: <IconLock size={13} />,
        hint: "chmod",
        disabled: fileOps.busy !== null,
        onSelect: () => void fileOps.chmodEntry(entry, listKey),
      },
    );
    if (!isDir) {
      items.push({
        kind: "item",
        label: "校验值…",
        icon: <IconShieldCheck size={13} />,
        hint: "md5/sha256",
        disabled: fileOps.busy !== null,
        onSelect: () => void fileOps.checksumEntry(entry),
      });
    }
    items.push(
      { kind: "separator" },
      { kind: "group", label: "危险操作" },
      {
        kind: "item",
        label: "删除",
        icon: <IconTrash size={13} />,
        danger: true,
        onSelect: () => void remove(entry.path),
      },
    );
    setMenu({ x, y, title: entry.path, items });
  };

  const openRowMenu = (
    e: ReactMouseEvent<HTMLDivElement>,
    entry: FileEntryDto,
    listKey: string,
    rootEntry = false,
  ) => {
    e.preventDefault();
    e.stopPropagation();
    openRowMenuAt(entry, listKey, e.clientX, e.clientY, rootEntry);
  };

  const openBlankMenu = (e: ReactMouseEvent<HTMLDivElement>) => {
    e.preventDefault();
    if (root === null) {
      setMenu({
        x: e.clientX,
        y: e.clientY,
        title: windowsComputer ? "此电脑" : "文件",
        items: [
          { kind: "group", label: windowsComputer ? "此电脑" : "文件" },
          {
            kind: "item",
            label: "刷新",
            icon: <IconRefresh size={13} />,
            onSelect: refresh,
          },
        ],
      });
      return;
    }
    const gone = sessionGone
      ? { hint: "会话已删除" as const, disabled: true }
      : { hint: undefined, disabled: false };
    const items: MenuItem[] = [
      { kind: "group", label: "当前目录" },
      {
        kind: "item",
        label: "上传到当前目录",
        icon: <IconUpload size={13} />,
        hint: gone.hint,
        disabled: gone.disabled,
        onSelect: () => void uploadTo(root),
      },
      {
        kind: "item",
        label: "上传文件夹到当前目录",
        icon: <IconFolderPlus size={13} />,
        hint: gone.hint,
        disabled: gone.disabled,
        onSelect: () => void uploadFolderTo(root),
      },
      {
        kind: "item",
        label: "新建目录",
        icon: <IconFolderPlus size={13} />,
        hint: gone.hint,
        disabled: gone.disabled,
        onSelect: () => void newDirIn(root),
      },
      {
        kind: "item",
        label: "新建空白文件",
        icon: <IconFilePlus size={13} />,
        hint: gone.hint,
        disabled: gone.disabled,
        onSelect: () => void newFileIn(root),
      },
      {
        kind: "item",
        label: "刷新",
        icon: <IconRefresh size={13} />,
        hint: gone.hint,
        disabled: gone.disabled,
        onSelect: refresh,
      },
    ];
    setMenu({ x: e.clientX, y: e.clientY, title: root, items });
  };

  const expandedSet = useMemo(() => new Set(expanded), [expanded]);
  const entryCount = useMemo(() => rows.reduce((n, r) => n + (r.entry ? 1 : 0), 0), [rows]);

  // Small trees render every row; only large directories (a home folder can hold thousands) pay for windowing.
  const virtualized = rows.length > TREE_VIRTUALIZE_THRESHOLD;
  const listRef = useRef<HTMLDivElement>(null);
  const [listOffset, setListOffset] = useState(0);
  const rowsRef = useRef(rows);
  rowsRef.current = rows;
  const focusedPathRef = useRef<string | null>(null);
  const pendingFocusRef = useRef<{ path: string; at: number } | null>(null);

  // Keep the focused row mounted while it is scrolled out so keyboard focus is not dropped.
  const rangeExtractor = useCallback((range: Range) => {
    const indexes = defaultRangeExtractor(range);
    const path = focusedPathRef.current;
    if (!path) return indexes;
    const at = rowsRef.current.findIndex((r) => r.entry?.path === path);
    if (at < 0 || indexes.includes(at)) return indexes;
    return [...indexes, at].sort((a, b) => a - b);
  }, []);

  // Entry rows are one line of identical height, so it is measured once from a real row instead of observing every
  // mounted row; touch layouts make rows taller than the desktop default.
  const [entryRowHeight, setEntryRowHeight] = useState(TREE_ROW_HEIGHT);
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => treeDropRef.current,
    estimateSize: (index) => (rowsRef.current[index]?.error ? ERROR_ROW_HEIGHT : entryRowHeight),
    overscan: TREE_OVERSCAN,
    enabled: virtualized,
    scrollMargin: listOffset,
    initialRect: { width: 260, height: 600 },
    getItemKey: (index) => {
      const r = rowsRef.current[index];
      return r ? (r.entry ? r.entry.path : `err:${r.dir}`) : index;
    },
    rangeExtractor,
  });

  useLayoutEffect(() => {
    const el = listRef.current;
    if (!el || !virtualized) return;
    const row = el.querySelector<HTMLElement>('[role="treeitem"]');
    if (!row) return;
    const measure = () => {
      const next = Math.round(row.getBoundingClientRect().height);
      if (next > 0) setEntryRowHeight((cur) => (Math.abs(cur - next) < 1 ? cur : next));
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(row);
    return () => observer.disconnect();
  }, [virtualized, rows.length]);

  useEffect(() => {
    if (virtualized) virtualizer.measure();
  }, [virtualized, entryRowHeight, virtualizer]);

  // The root-listing error banner sits above the rows inside the same scroller.
  useLayoutEffect(() => {
    const el = listRef.current;
    if (!el || !virtualized) return;
    const next = el.offsetTop;
    setListOffset((cur) => (cur === next ? cur : next));
  }, [virtualized, rows.length]);

  const focusPendingRow = () => {
    const pending = pendingFocusRef.current;
    if (!pending) return;
    if (Date.now() - pending.at > 1500) {
      pendingFocusRef.current = null;
      return;
    }
    const target = [...(treeDropRef.current?.querySelectorAll<HTMLElement>("[data-row-path]") ?? [])].find(
      (el) => el.dataset.rowPath === pending.path,
    );
    if (!target) return;
    pendingFocusRef.current = null;
    target.focus({ preventScroll: true });
  };
  useLayoutEffect(focusPendingRow);

  const navigateRows = (event: ReactKeyboardEvent<HTMLElement>, path: string) => {
    const key = event.key;
    if (key !== "ArrowDown" && key !== "ArrowUp" && key !== "Home" && key !== "End") return;
    const list = rowsRef.current;
    const from = list.findIndex((r) => r.entry?.path === path);
    if (from < 0) return;
    let to = -1;
    if (key === "ArrowDown") {
      for (let i = from + 1; i < list.length; i++) if (list[i].entry) { to = i; break; }
    } else if (key === "ArrowUp") {
      for (let i = from - 1; i >= 0; i--) if (list[i].entry) { to = i; break; }
    } else if (key === "Home") {
      to = list.findIndex((r) => r.entry);
    } else {
      for (let i = list.length - 1; i >= 0; i--) if (list[i].entry) { to = i; break; }
    }
    if (to < 0) return;
    event.preventDefault();
    if (to === from) return;
    const targetPath = list[to].entry!.path;
    pendingFocusRef.current = { path: targetPath, at: Date.now() };
    if (virtualized) virtualizer.scrollToIndex(to);
    focusPendingRow();
  };

  const actionsRef = useRef<TreeRowActions | null>(null);
  actionsRef.current = {
    toggle,
    select: setSelected,
    enterDir,
    openFile,
    openRowMenuAt,
    openRowMenu,
    download,
    remove,
    retryDir,
    reconnect: () => void reconnectHost(),
    navigate: navigateRows,
    noteFocus: (path) => {
      focusedPathRef.current = path;
    },
  };
  // Stable proxies keep memoized rows from re-rendering when only parent closures change.
  const rowActions = useMemo<TreeRowActions>(() => {
    const call = <K extends keyof TreeRowActions>(name: K) =>
      ((...args: unknown[]) =>
        (actionsRef.current![name] as (...a: unknown[]) => unknown)(...args)) as TreeRowActions[K];
    return {
      toggle: call("toggle"),
      select: call("select"),
      enterDir: call("enterDir"),
      openFile: call("openFile"),
      openRowMenuAt: call("openRowMenuAt"),
      openRowMenu: call("openRowMenu"),
      download: call("download"),
      remove: call("remove"),
      retryDir: call("retryDir"),
      reconnect: call("reconnect"),
      navigate: call("navigate"),
      noteFocus: call("noteFocus"),
    };
  }, []);

  const renderRow = (index: number) => {
    const r = rows[index];
    if (!r) return null;
    return (
      <TreeRow
        entry={r.entry}
        depth={r.depth}
        error={r.error}
        dir={r.dir}
        rootEntry={r.rootEntry}
        open={r.entry ? expandedSet.has(r.entry.path) : false}
        isSel={r.entry ? selected === r.entry.path : false}
        coarse={coarse}
        reconnecting={reconnecting}
        setSize={r.setSize}
        posInSet={r.posInSet}
        actions={rowActions}
      />
    );
  };

  if (!leftOpen) return null;

  const crumbs = root ? crumbsOf(root) : [];
  const parent = root ? parentOf(root) : null;
  const upToComputer = root !== null && parent === null && windowsComputer && isRoot(root);
  const up = parent ?? (upToComputer ? "此电脑" : null);
  const loading = !environmentSettled || dirs.some((_, i) => results[i]?.isLoading);

  return (
    <aside
      className="nx-files-pane flex h-full shrink-0 flex-col border-r border-neutral-800/60 bg-neutral-950"
      style={{ width: leftWidth }}
    >
      <div className="nx-files-bar flex h-[34px] shrink-0 items-center gap-0.5 overflow-x-auto px-2">
        <span className="mr-1 shrink-0 text-xs font-semibold tracking-wide text-neutral-200">
          文件
        </span>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={sessionGone ? goneHint : computer ? "请选择实际目录" : "新建文件"}
          aria-label="新建文件"
          disabled={sessionGone || computer}
          onClick={() => void newFileIn(targetDir())}
        >
          <IconFilePlus size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={sessionGone ? goneHint : computer ? "请选择实际目录" : "新建目录"}
          aria-label="新建目录"
          disabled={sessionGone || computer}
          onClick={() => void newDirIn(targetDir())}
        >
          <IconFolderPlus size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={sessionGone ? goneHint : "刷新"}
          aria-label="刷新文件列表"
          disabled={sessionGone}
          onClick={refresh}
        >
          <IconRefresh size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="折叠全部"
          aria-label="折叠全部目录"
          onClick={() => {
            setExpanded([]);
            setSelected(null);
          }}
        >
          <IconFoldAll size={14} />
        </button>
        {follow.supported && (
          <button
            className={`nx-icon-btn nx-icon-btn-sm${follow.enabled ? " is-active" : ""}`}
            title={
              follow.enabled
                ? "跟随终端目录：开。终端切换目录时，文件树自动跟随"
                : "跟随终端目录：关。点击开启自动跟随"
            }
            aria-label="跟随终端目录"
            aria-pressed={follow.enabled}
            onClick={follow.toggle}
          >
            <IconLocate size={14} />
          </button>
        )}
        <div className="nx-spacer" />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={sessionGone ? goneHint : computer ? "请选择实际目录" : "上传到当前目录"}
          aria-label="上传到当前目录"
          disabled={sessionGone || computer}
          onClick={() => void uploadTo(targetDir())}
        >
          <IconUpload size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={sessionGone ? goneHint : computer ? "请选择实际目录" : "上传文件夹到当前目录"}
          aria-label="上传文件夹到当前目录"
          disabled={sessionGone || computer}
          onClick={() => void uploadFolderTo(targetDir())}
        >
          <IconFolderPlus size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="下载选中项"
          aria-label="下载选中项"
          disabled={!selectedEntry || selectedEntry.kind === "dir"}
          onClick={() => selectedEntry && void download(selectedEntry.path)}
        >
          <IconDownload size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm is-danger"
          title="删除选中项"
          aria-label="删除选中项"
          disabled={!selectedEntry}
          onClick={() => selectedEntry && void remove(selectedEntry.path)}
        >
          <IconTrash size={14} />
        </button>
      </div>

      <div className="nx-pathbar">
        <button
          className="nx-tree-caret"
          title={up ? `上级：${up}` : computer ? "已经在此电脑" : "已经在根目录"}
          aria-label={up ? `上级目录 ${up}` : computer ? "已经在此电脑" : "已经在根目录"}
          disabled={!up}
          onClick={() => {
            if (parent) navigateRoot(parent);
            else if (upToComputer) navigateRoot(null);
          }}
        >
          <IconArrowUp size={12} />
        </button>
        {editing ? (
          <input
            className="nx-input nx-input-sm min-w-0 flex-1 font-mono"
            autoFocus
            spellCheck={false}
            value={draft}
            placeholder="输入路径后回车（支持 ~ 与 C:/）"
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !isImeKeyEvent(e)) {
                e.preventDefault();
                void commitEdit();
              } else if (e.key === "Escape") {
                e.preventDefault();
                setEditing(false);
              }
            }}
            onBlur={() => setEditing(false)}
          />
        ) : (
          <div
            ref={crumbRef}
            className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto"
            onClick={(e) => {
              if (e.target === e.currentTarget) beginEdit();
            }}
            title="点空白处可直接输入路径"
          >
            {computer ? (
              <span className="nx-path-crumb is-current" aria-current="location">
                此电脑
              </span>
            ) : (
              <>
                {windowsComputer && (
                  <span className="flex shrink-0 items-center gap-0.5">
                    <button className="nx-path-crumb" onClick={() => navigateRoot(null)}>
                      此电脑
                    </button>
                    <span className="text-neutral-600">/</span>
                  </span>
                )}
                {crumbs.map((c, i) => (
                  <span key={c.path} className="flex shrink-0 items-center gap-0.5">
                    {i > 0 && crumbs[i - 1].label !== "/" && (
                      <span className="text-neutral-600">/</span>
                    )}
                    <button
                      className={`nx-path-crumb ${i === crumbs.length - 1 ? "is-current" : ""}`}
                      aria-current={i === crumbs.length - 1 ? "location" : undefined}
                      onClick={() => navigateRoot(c.path)}
                      title={c.path}
                    >
                      {c.label}
                    </button>
                  </span>
                ))}
              </>
            )}
          </div>
        )}
        <button
          className="nx-tree-caret"
          title={editing ? "取消编辑（Esc）" : "输入路径跳转"}
          aria-label={editing ? "取消编辑路径" : "输入路径跳转"}
          onClick={() => (editing ? setEditing(false) : beginEdit())}
        >
          {editing ? <IconClose size={11} /> : <IconEdit size={11} />}
        </button>
        <button
          className="nx-tree-caret"
          title={homeSupported ? "回到家目录 (~)" : "WinRM 后端不支持 ~ 展开"}
          aria-label={homeSupported ? "回到家目录 (~)" : "WinRM 后端不支持 ~ 展开"}
          disabled={!homeSupported || root === homeTarget}
          onClick={() => {
            if (homeSupported) navigateRoot(homeTarget);
          }}
        >
          <IconHome size={12} />
        </button>
      </div>

      <div
        role="tree"
        aria-label="文件"
        ref={treeDropRef}
        className="relative min-h-0 flex-1 overflow-y-auto px-1 py-1"
        data-file-drop-target={computer ? undefined : ""}
        onContextMenu={openBlankMenu}
        onDragOver={(e) => {
          if (computer || !e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          e.dataTransfer.dropEffect = "copy";
        }}
        onDrop={dropUpload}
      >
        {rootsQuery.isError && (
          <div className="mx-1 my-0.5 flex items-start gap-1.5 rounded border border-red-500/30 bg-red-950/30 px-2 py-1.5">
            <IconXCircle size={12} className="mt-0.5 shrink-0 text-red-300" />
            <div className="min-w-0 flex-1 text-[11px] leading-relaxed text-red-200">
              <div className="break-words">
                {rootsSessionGone
                  ? "会话已在服务端删除，重试不会恢复；重新连接这台主机后在此继续浏览"
                  : `无法读取磁盘根信息：${describeError(rootsQuery.error)}`}
              </div>
              <div className="flex gap-2">
                {!rootsSessionGone && (
                  <button
                    className="nx-link mt-0.5 text-[11px]"
                    onClick={() => void qc.invalidateQueries({ queryKey: ["fsRoots", sessionId] })}
                  >
                    重试
                  </button>
                )}
                {rootsSessionGone && (
                  <button
                    className="nx-link mt-0.5 text-[11px]"
                    disabled={reconnecting}
                    onClick={() => void reconnectHost()}
                  >
                    {reconnecting ? "正在重新连接…" : "重新连接这台主机"}
                  </button>
                )}
              </div>
            </div>
          </div>
        )}
        {virtualized ? (
          <div ref={listRef} className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
            {virtualizer.getVirtualItems().map((item) => (
              <div
                key={item.key}
                role="none"
                data-index={item.index}
                // Only error rows have a variable height; every other row uses the single measured height.
                ref={rows[item.index]?.error ? virtualizer.measureElement : undefined}
                className="absolute left-0 top-0 w-full"
                style={{ transform: `translateY(${item.start - listOffset}px)`, contain: "layout style" }}
              >
                {renderRow(item.index)}
              </div>
            ))}
          </div>
        ) : (
          <div ref={listRef}>
            {rows.map((r, index) => (
              <div key={r.entry ? r.entry.path : `err:${r.dir}`} role="none">
                {renderRow(index)}
              </div>
            ))}
          </div>
        )}

        {loading && rows.length === 0 && (
          <div role="status" aria-label="加载中">
            {Array.from({ length: 8 }, (_, i) => (
              <div key={i} className="nx-skeleton-row" aria-hidden="true">
                <span className="nx-skeleton nx-skeleton-icon" />
                <span className="nx-skeleton min-w-0 flex-1" />
              </div>
            ))}
            <span className="nx-sr-only">加载中…</span>
          </div>
        )}
        {!loading && rows.length === 0 && dirErrors.size === 0 && !rootsQuery.isError && (
          <div className="nx-hint px-2 py-6 text-center">{computer ? "没有可用磁盘" : "这个目录是空的"}</div>
        )}
      </div>

      <div className="flex h-[24px] shrink-0 items-center gap-2 border-t border-neutral-800/60 px-2.5 text-[10.5px] text-neutral-500">
        <span>{entryCount} 项</span>
        {selectedEntry && (
          <>
            <span className="text-neutral-700">|</span>
            <span className="min-w-0 flex-1 truncate" title={selectedEntry.path}>
              {selectedEntry.kind === "dir" ? "目录" : formatSize(selectedEntry.size)}
            </span>
          </>
        )}
      </div>

      <ContextMenu state={menu} onClose={() => setMenu(null)} />
    </aside>
  );
}

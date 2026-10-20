import {
  useEffect,
  useRef,
  useState,
  type DragEvent as ReactDragEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import {
  ask,
  discardStaged,
  finishSave,
  pickLocalFile,
  pickSavePath,
  promptText,
} from "../../ui/dialogs";
import { fsApi, sessionApi, terminalApi } from "../../ipc/commands";
import { listenEvent, EVENTS, type FsProgressEvent } from "../../ipc/events";
import type { FileEntryDto } from "../../ipc/types";
import {
  findWritableTerminal,
  openFileTab,
  openFileTabInSplit,
  openLogTab,
  openTerminalTab,
  reconnectDeadHost,
  useUi,
  type Workspace,
} from "../../app/store";
import { useCoarsePointer } from "../../app/platform";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import { copyText } from "../../ui/clipboard";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { describeError } from "../../ui/errorText";
import "../../ui/skeleton.css";
import { fileVisual, formatSize, isEditableFile, isExtractableArchive } from "./fileTypes";
import {
  HOME,
  baseName,
  directoryCommandFor,
  type DirectoryCommandFailure,
  fsErrorResource,
  isRoot,
  joinPath,
  normalizeTypedPath,
  parentOf,
} from "./pathUtils";
import { progressPercent, reduceFileProgress, visibleFileProgress, type FileProgressMap } from "./fileProgress";
import { checkUploadOverwrite } from "./uploadConfirm";
import { useFileOps } from "./useFileOps";
import { packDownloadSupported, validateEntryName } from "./fileOps";
import { useCwdFollow } from "./cwdFollow";
import { dropFolderUpload, pickFolderUpload } from "./folderUpload";
import { noteHtml5Drop, useNativeFileDrop } from "./nativeDrop";
import {
  createTransferTaskId,
  enqueueDownload,
  enqueueUploads,
  subscribeTransferSettled,
  useTransferStore,
} from "./transferStore";
import { TransferPanel } from "./TransferPanel";
import {
  IconAlert,
  IconArchive,
  IconArrowUp,
  IconCopy,
  IconDownload,
  IconDrive,
  IconEdit,
  IconEye,
  IconFilePlus,
  IconFolder,
  IconFolderOpen,
  IconFolderPlus,
  IconLocate,
  IconLock,
  IconRefresh,
  IconShieldCheck,
  IconTerminal,
  IconTrash,
  IconUpload,
} from "../../ui/icons";

function browserRowKeyDown(event: ReactKeyboardEvent<HTMLElement>): void {
  if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
  const tree = event.currentTarget.closest("[role='tree']");
  if (!tree) return;
  const rows = [...tree.querySelectorAll<HTMLElement>("[role='treeitem']")];
  const index = rows.indexOf(event.currentTarget);
  const next = event.key === "ArrowDown" ? index + 1 : index - 1;
  if (index < 0 || next < 0 || next >= rows.length) return;
  event.preventDefault();
  event.stopPropagation();
  rows[next]?.focus();
}

const fileBrowserReconnects = new Map<string, ReturnType<typeof reconnectDeadHost>>();

export function FileBrowser({ sessionId }: { sessionId: string }) {
  return <FileBrowserInner sessionId={sessionId} />;
}

function FileBrowserInner({ sessionId }: { sessionId: string }) {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  const sessionKind = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.kind);
  const packHidden = sessionKind !== undefined && !packDownloadSupported(sessionKind);
  const [pathChoice, setPath] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const fileOps = useFileOps(sessionId);
  const parentRef = useRef<HTMLDivElement>(null);
  const coarse = useCoarsePointer();
  const rootsQuery = useQuery({
    queryKey: ["fsRoots", sessionId],
    queryFn: () => fsApi.roots(sessionId),
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
  const path = pathChoice ?? (windowsComputer ? null : HOME);
  const computer = path === null && windowsComputer;
  const follow = useCwdFollow(sessionId, path ?? "\u0000", (next) => {
    setPath(next);
    setSelected(null);
  });

  const entries = useQuery({
    queryKey: ["fs", sessionId, path],
    queryFn: () => fsApi.list(sessionId, path as string),
    enabled: environmentSettled && path !== null && !rootsSessionGone,
    refetchOnWindowFocus: false,
    retry: (failureCount, error) =>
      (error as { code?: string } | null)?.code === "not_found" ? false : failureCount < 1,
  });

  const list: FileEntryDto[] = computer
    ? (rootsData?.roots ?? []).map((realRoot) => ({
        name: realRoot.label || realRoot.path,
        path: realRoot.path,
        kind: "dir",
        size: 0,
        mode: "",
        owner: null,
        group: null,
        mtime: 0,
        symlinkTarget: null,
      }))
    : (entries.data ?? []);
  const virtualizer = useVirtualizer({
    count: list.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 30,
    overscan: 20,
  });

  const [progress, setProgress] = useState<FileProgressMap>({});
  useEffect(() => {
    const un = listenEvent<FsProgressEvent>(EVENTS.fsProgress, (event) => {
      if (useTransferStore.getState().tasks.some((task) => task.id === event.taskId)) return;
      if (event.error) {
        pushToast("error", `文件传输失败：${event.error}`);
      }
      setProgress((current) => reduceFileProgress(current, event));
    });
    return () => {
      void un.then((f) => f());
    };
  }, [pushToast]);

  useEffect(
    () =>
      subscribeTransferSettled((task) => {
        if (task.sessionId !== sessionId || !task.refreshDir) return;
        void qc.invalidateQueries({ queryKey: ["fs", sessionId, task.refreshDir] });
      }),
    [qc, sessionId],
  );

  useEffect(() => {
    setDraft(path ?? "");
  }, [path]);

  const goto = (next: string | null) => {
    if (next === path) return;
    setPath(next);
    setSelected(null);
    setMenu(null);
  };

  const openEntry = (entry: (typeof list)[number]) => {
    if (entry.kind === "dir") {
      if (computer) goto(entry.path);
      else if (path) goto(joinPath(path, entry.name));
      return;
    }
    if (!isEditableFile(entry.name)) {
      pushToast("info", `「${entry.name}」不是文本文件，不能在线编辑；可以下载到本机后打开`);
      return;
    }
    openFileTab(sessionId, entry.path);
  };

  const up = () => {
    if (!path) return;
    const parent = parentOf(path);
    if (parent) goto(parent);
    else if (windowsComputer && isRoot(path)) goto(null);
  };

  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["fsRoots", sessionId] });
    if (path) void qc.invalidateQueries({ queryKey: ["fs", sessionId, path] });
  };
  const refreshDir = (dir: string) =>
    void qc.invalidateQueries({ queryKey: ["fs", sessionId, dir] });

  useNativeFileDrop(sessionId, {
    surface: parentRef,
    fallbackDir: () => path ?? "",
    knownSiblings: (dir) => (dir === path ? entries.data : undefined),
    onSettled: refreshDir,
  });

  const sessionGone =
    rootsSessionGone || (entries.isError && fsErrorResource(entries.error) === "session");
  const legacyNotFound =
    entries.isError &&
    fsErrorResource(entries.error) === null &&
    (entries.error as { code?: string }).code === "not_found";
  const legacyRootsNotFound =
    rootsQuery.isError &&
    fsErrorResource(rootsQuery.error) === null &&
    (rootsQuery.error as { code?: string }).code === "not_found";
  const goneHint = "会话已在服务端删除，请重新连接这台主机";
  const [reconnecting, setReconnecting] = useState(false);

  const reconnectHost = async () => {
    const st = useUi.getState();
    let target: { tabId: string; ws: Workspace } | undefined;
    for (const w of st.workspaces) {
      const tab = w.panes
        .flatMap((p) => p.tabs)
        .find((t) => t.kind === "files" && !t.path && t.sessionId === sessionId);
      if (tab) {
        target = { tabId: tab.id, ws: w };
        break;
      }
    }
    if (!target) {
      pushToast("error", "找不到这个文件标签的主机信息，请到左侧资产树里手动连接");
      return;
    }
    let pending = fileBrowserReconnects.get(sessionId);
    if (!pending) {
      pending = reconnectDeadHost(target.ws);
      fileBrowserReconnects.set(sessionId, pending);
    }
    setReconnecting(true);
    try {
      const s = await pending;
      if (!s) {
        pushToast("info", "已取消重连");
        return;
      }
      const latest = useUi.getState();
      latest.setSessions([
        ...latest.sessions.filter((x) => x.id !== sessionId && x.id !== s.id),
        s,
      ]);
      for (const workspace of latest.workspaces) {
        for (const pane of workspace.panes) {
          for (const tab of pane.tabs) {
            if (tab.kind === "files" && !tab.path && tab.sessionId === sessionId) {
              latest.updateTab(tab.id, { sessionId: s.id });
            }
          }
        }
      }
      pushToast("info", "已重新连接，正在重新加载文件列表");
    } catch (e) {
      pushToast("error", `重新连接失败：${describeError(e)}`);
    } finally {
      if (fileBrowserReconnects.get(sessionId) === pending) {
        fileBrowserReconnects.delete(sessionId);
      }
      setReconnecting(false);
    }
  };

  const autoReconnectRef = useRef<string | null>(null);
  useEffect(() => {
    if (!sessionGone) {
      autoReconnectRef.current = null;
      return;
    }
    if (autoReconnectRef.current === sessionId) return;
    autoReconnectRef.current = sessionId;
    void reconnectHost();
  }, [sessionGone, sessionId]);

  const uploadTo = async (dir: string) => {
    const file = await pickLocalFile();
    if (!file) return;
    const remote = joinPath(dir, baseName(file));
    let handed = false;
    try {
      const check = await checkUploadOverwrite(sessionId, dir, remote, dir === path ? entries.data : undefined);
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

  const downloadToLocal = async (remotePath: string) => {
    const target = await pickSavePath(baseName(remotePath));
    if (!target) return;
    pushToast("info", "开始下载…");
    enqueueDownload(sessionId, { remotePath, localPath: target });
  };

  const uploadFolderTo = async (dir: string) => {
    await pickFolderUpload({
      sessionId,
      dir,
      knownSiblings: dir === path ? entries.data : undefined,
      onSettled: () => refreshDir(dir),
    });
  };

  const dropUpload = (event: ReactDragEvent<HTMLDivElement>) => {
    if (!event.dataTransfer.types.includes("Files")) return;
    event.preventDefault();
    noteHtml5Drop();
    const row = (event.target as HTMLElement).closest<HTMLElement>("[data-drop-path]");
    const dir = row?.dataset.dropPath ?? path ?? "";
    if (!dir) {
      pushToast("info", "请先选择实际目录再上传");
      return;
    }
    void dropFolderUpload({
      sessionId,
      dir,
      dataTransfer: event.dataTransfer,
      knownSiblings: dir === path ? entries.data : undefined,
      onSettled: () => refreshDir(dir),
    });
  };

  const removePath = async (remotePath: string, isDir: boolean) => {
    const tip = isDir ? "\n\n目录会被递归删除，不可恢复。" : "";
    if (!(await ask(`删除 ${remotePath}？${tip}`, { kind: "warning" }))) return;
    try {
      await fsApi.delete(sessionId, remotePath, isDir);
      if (selected === remotePath) setSelected(null);
      refresh();
      pushToast("success", "已删除");
    } catch (e) {
      pushToast("error", `删除失败：${describeError(e)}`);
    }
  };

  const mkDirIn = async (dir: string) => {
    const name = await promptText("新建目录名");
    if (name === null) return;
    const problem = validateEntryName(name);
    if (problem) {
      pushToast("error", `无法创建：${problem}`);
      return;
    }
    const p = joinPath(dir, name);
    try {
      await fsApi.mkdir(sessionId, p);
      refreshDir(dir);
      pushToast("success", `已创建 ${p}`);
    } catch (e) {
      pushToast("error", `创建失败：${describeError(e)}`);
    }
  };

  const newFileIn = async (dir: string) => {
    const name = await promptText("新建文件名");
    if (name === null) return;
    const problem = validateEntryName(name);
    if (problem) {
      pushToast("error", `无法创建：${problem}`);
      return;
    }
    const p = joinPath(dir, name);
    try {
      await fsApi.write(sessionId, p, "", false);
      refreshDir(dir);
      pushToast("success", `已创建 ${p}`);
    } catch (e) {
      pushToast("error", `创建失败：${describeError(e)}`);
    }
  };

  const copyPath = async (p: string) => {
    try {
      await copyText(p);
      pushToast("success", "路径已复制");
    } catch (e) {
      pushToast("error", `复制失败：${describeError(e)}`);
    }
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
      refreshDir(parentOf(target) ?? path ?? HOME);
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

  const openRowMenuAt = (entry: FileEntryDto, x: number, y: number) => {
    setSelected(entry.path);
    if (computer) {
      setMenu({
        x,
        y,
        title: entry.path,
        items: [
          { kind: "group", label: "磁盘" },
          { kind: "item", label: "打开此磁盘", onSelect: () => goto(entry.path) },
        ],
      });
      return;
    }
    const isDir = entry.kind === "dir";
    const dir = isDir ? entry.path : (parentOf(entry.path) ?? path ?? HOME);
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
      items.push(
        {
          kind: "item",
          label: "上传到该目录",
          icon: <IconUpload size={13} />,
          onSelect: () => void uploadTo(entry.path),
        },
        {
          kind: "item",
          label: "上传文件夹到该目录",
          icon: <IconFolderPlus size={13} />,
          onSelect: () => void uploadFolderTo(entry.path),
        },
        {
          kind: "item",
          label: "新建子目录",
          icon: <IconFolderPlus size={13} />,
          onSelect: () => void mkDirIn(entry.path),
        },
      );
    } else {
      items.push({
        kind: "item",
        label: "在下方编辑",
        icon: <IconEdit size={13} />,
        hint: isEditableFile(entry.name) ? undefined : "非文本文件",
        disabled: !isEditableFile(entry.name),
        onSelect: () => openFileTabInSplit(sessionId, entry.path),
      });
      items.push({
        kind: "item",
        label: "查看日志",
        icon: <IconEye size={13} />,
        hint: isEditableFile(entry.name) ? "只读分块" : "非文本文件",
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
      items.push(
        {
          kind: "item",
          label: "下载当前文件",
          icon: <IconDownload size={13} />,
          onSelect: () => void downloadToLocal(entry.path),
        },
        {
          kind: "item",
          label: "复制路径",
          icon: <IconCopy size={13} />,
          onSelect: () => void copyPath(entry.path),
        },
      );
    }
    items.push(
      {
        kind: "item",
        label: "重命名",
        icon: <IconEdit size={13} />,
        disabled: fileOps.busy !== null,
        onSelect: () =>
          void fileOps.renameEntry(entry, list, path ?? HOME, (from, to) =>
            setSelected((cur) => (cur === from ? to : cur)),
          ),
      },
      {
        kind: "item",
        label: "权限…",
        icon: <IconLock size={13} />,
        hint: "chmod",
        disabled: fileOps.busy !== null,
        onSelect: () => void fileOps.chmodEntry(entry, path ?? HOME),
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
        onSelect: () => void removePath(entry.path, isDir),
      },
    );
    setMenu({ x, y, title: entry.path, items });
  };

  const openRowMenu = (ev: ReactMouseEvent<HTMLDivElement>, entry: FileEntryDto) => {
    ev.preventDefault();
    ev.stopPropagation();
    openRowMenuAt(entry, ev.clientX, ev.clientY);
  };

  const openBlankMenuAt = (x: number, y: number) => {
    if (path === null) {
      setMenu({
        x,
        y,
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
    const items: MenuItem[] = [{ kind: "group", label: "当前目录" }];
    if (!packHidden) {
      items.push({
        kind: "item",
        label: "打包下载当前目录",
        icon: <IconArchive size={13} />,
        hint: sessionGone ? "会话已删除" : "tar.gz",
        disabled: sessionGone,
        onSelect: () => void packDownload(path),
      });
    }
    items.push(
      {
        kind: "item",
        label: "上传到当前目录",
        icon: <IconUpload size={13} />,
        hint: sessionGone ? "会话已删除" : undefined,
        disabled: sessionGone,
        onSelect: () => void uploadTo(path),
      },
      {
        kind: "item",
        label: "上传文件夹到当前目录",
        icon: <IconFolderPlus size={13} />,
        hint: sessionGone ? "会话已删除" : undefined,
        disabled: sessionGone,
        onSelect: () => void uploadFolderTo(path),
      },
      {
        kind: "item",
        label: "新建目录",
        icon: <IconFolderPlus size={13} />,
        hint: sessionGone ? "会话已删除" : undefined,
        disabled: sessionGone,
        onSelect: () => void mkDirIn(path),
      },
      {
        kind: "item",
        label: "新建空白文件",
        icon: <IconFilePlus size={13} />,
        hint: sessionGone ? "会话已删除" : undefined,
        disabled: sessionGone,
        onSelect: () => void newFileIn(path),
      },
      {
        kind: "item",
        label: "刷新",
        icon: <IconRefresh size={13} />,
        hint: sessionGone ? "会话已删除" : undefined,
        disabled: sessionGone,
        onSelect: refresh,
      },
      { kind: "separator" },
      { kind: "group", label: "终端" },
      {
        kind: "item",
        label: "在当前终端打开",
        icon: <IconTerminal size={13} />,
        hint: "已有终端",
        onSelect: () => cdTerminalTo(path),
      },
      {
        kind: "item",
        label: "在新终端打开",
        icon: <IconTerminal size={13} />,
        hint: "新标签",
        onSelect: () => openTerminalAt(path),
      },
    );
    setMenu({ x, y, title: path, items });
  };

  const openBlankMenu = (ev: ReactMouseEvent<HTMLDivElement>) => {
    ev.preventDefault();
    openBlankMenuAt(ev.clientX, ev.clientY);
  };

  const selectedEntry = computer ? undefined : list.find((e) => e.path === selected);
  const selectedFile = selectedEntry?.kind === "dir" ? undefined : selectedEntry;
  const selectedEditableFile =
    selectedFile && isEditableFile(selectedFile.name) ? selectedFile : undefined;
  const downloadTitle = !selectedEntry
    ? "请先选择要下载的文件"
    : selectedEntry.kind === "dir"
      ? "目录不能直接下载，请使用打包下载"
      : "下载选中的文件";
  const logTitle = !selectedEntry
    ? "请先选择要查看日志的文本文件"
    : selectedEntry.kind === "dir"
      ? "目录不能使用日志查看器"
      : !selectedEditableFile
        ? "非文本文件不能使用日志查看器"
        : "用只读日志查看器打开选中文件（分块读取，不整文件下载）";
  const canUp = path !== null && (parentOf(path) !== null || (windowsComputer && isRoot(path)));

  return (
    <div className="nx-pane">
      <div className="nx-toolbar">
        <button
          className="nx-icon-btn"
          onClick={up}
          disabled={!canUp}
          title="上一级目录"
        >
          <IconArrowUp size={14} />
        </button>
        <div className="nx-field max-w-none min-w-0 flex-1">
          <span className="nx-field-icon">
            <IconFolderOpen size={13} />
          </span>
          <input
            className="nx-input nx-input-sm font-mono"
            value={draft}
            spellCheck={false}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !isImeKeyEvent(e)) {
                e.preventDefault();
                const next = normalizeTypedPath(draft, pathStyle);
                if (!next) {
                  if (draft.trim()) {
                    pushToast(
                      "error",
                      "Windows 驱动器相对路径无法直接跳转，需要绝对路径（如 D:/Projects）",
                    );
                  } else {
                    refresh();
                  }
                } else if (next !== path) {
                  goto(next);
                } else {
                  refresh();
                }
              } else if (e.key === "Escape") {
                e.preventDefault();
                setDraft(path ?? "");
              }
            }}
            onBlur={() => setDraft(path ?? "")}
            placeholder="输入路径后回车，支持 ~ 与 C:/"
            title="直接输入路径后回车跳转"
          />
        </div>
        <button
          className="nx-icon-btn"
          onClick={refresh}
          disabled={sessionGone}
          title={sessionGone ? goneHint : "刷新"}
        >
          <IconRefresh size={14} />
        </button>
        {follow.supported && (
          <button
            className={`nx-icon-btn${follow.enabled ? " is-active" : ""}`}
            title={
              follow.enabled
                ? "跟随终端目录：开。终端切换目录时，文件列表自动跟随"
                : "跟随终端目录：关。点击开启自动跟随"
            }
            aria-label="跟随终端目录"
            aria-pressed={follow.enabled}
            onClick={follow.toggle}
          >
            <IconLocate size={14} />
          </button>
        )}
        <span className="nx-divider-v max-[560px]:hidden" />
        <button
          className="nx-btn nx-btn-sm max-[560px]:hidden"
          onClick={() => void uploadTo(path ?? "")}
          disabled={sessionGone || computer}
          title={sessionGone ? goneHint : computer ? "请选择实际目录" : "上传本地文件到当前目录"}
        >
          <IconUpload size={13} />
          上传
        </button>
        <button
          className="nx-btn nx-btn-sm max-[560px]:hidden"
          onClick={() => void uploadFolderTo(path ?? "")}
          disabled={sessionGone || computer}
          title={sessionGone ? goneHint : computer ? "请选择实际目录" : "上传本地文件夹到当前目录"}
        >
          <IconFolderPlus size={13} />
          文件夹
        </button>
        <button
          className="nx-btn nx-btn-sm max-[560px]:hidden"
          disabled={!selectedFile}
          onClick={() => selectedFile && void downloadToLocal(selectedFile.path)}
          title={downloadTitle}
        >
          <IconDownload size={13} />
          下载
        </button>
        <button
          className="nx-btn nx-btn-sm max-[560px]:hidden"
          disabled={!selectedEditableFile}
          onClick={() => selectedEditableFile && openLogTab(sessionId, selectedEditableFile.path)}
          title={logTitle}
        >
          <IconEye size={13} />
          日志
        </button>
        <button
          className="nx-btn nx-btn-sm max-[560px]:hidden"
          onClick={() => void mkDirIn(path ?? "")}
          disabled={sessionGone || computer}
          title={sessionGone ? goneHint : computer ? "请选择实际目录" : "新建目录"}
        >
          <IconFolder size={13} />
          新建
        </button>
        <button
          className="nx-btn nx-btn-danger nx-btn-sm max-[560px]:hidden"
          disabled={!selectedEntry}
          onClick={() =>
            selectedEntry && void removePath(selectedEntry.path, selectedEntry.kind === "dir")
          }
          title="删除选中项"
        >
          <IconTrash size={13} />
          删除
        </button>
        <button
          className="nx-icon-btn"
          title="更多操作（打包下载 / 上传 / 上传文件夹 / 新建目录 / 新建空白文件 / 刷新）"
          aria-label="更多操作"
          onClick={(e) => {
            const r = e.currentTarget.getBoundingClientRect();
            openBlankMenuAt(r.left, r.bottom);
          }}
        >
          ⋯
        </button>
      </div>

      {visibleFileProgress(progress).map((task) => (
        <div
          key={task.taskId}
          className="nx-progress shrink-0"
          title={`文件任务 ${task.taskId}：${task.transferred}/${task.total} 字节`}
          data-task-id={task.taskId}
        >
          <div className="nx-progress-bar" style={{ width: `${progressPercent(task)}%` }} />
        </div>
      ))}

      <div className="flex shrink-0 items-center gap-2 border-b border-neutral-800/60 px-3 py-1.5 text-[11px] text-neutral-500">
        <span className="w-[18px]" />
        <span className="flex-1">名称</span>
        <span className="w-24 text-right max-[560px]:hidden">大小</span>
        <span className="w-40 text-right max-[560px]:hidden">修改时间</span>
      </div>

      <div
        ref={parentRef}
        className="min-h-0 flex-1 overflow-y-auto"
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
          <div className="nx-alert nx-alert-danger m-3 flex items-start gap-2">
            <IconAlert size={14} className="mt-0.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="break-words">
                {rootsSessionGone
                  ? "会话已在服务端删除，重试不会恢复；重新连接这台主机后在本标签继续浏览"
                  : `无法读取磁盘根信息：${describeError(rootsQuery.error)}`}
              </div>
              <div className="flex gap-2">
                {!rootsSessionGone && (
                  <button
                    className="nx-link mt-1 text-[11px]"
                    onClick={() => void qc.invalidateQueries({ queryKey: ["fsRoots", sessionId] })}
                  >
                    重试
                  </button>
                )}
                {(rootsSessionGone || legacyRootsNotFound) && (
                  <button
                    className="nx-link mt-1 text-[11px]"
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
        {!environmentSettled || entries.isLoading ? (
          <div role="status" aria-label="加载中">
            {Array.from({ length: 10 }, (_, i) => (
              <div key={i} className="nx-skeleton-row" aria-hidden="true">
                <span className="nx-skeleton nx-skeleton-icon" />
                <span className="nx-skeleton min-w-0 flex-1" />
                <span className="nx-skeleton w-24 max-[560px]:hidden" />
                <span className="nx-skeleton w-40 max-[560px]:hidden" />
              </div>
            ))}
            <span className="nx-sr-only">加载中…</span>
          </div>
        ) : entries.isError ? (
          <div className="nx-alert nx-alert-danger m-3 flex items-start gap-2">
            <IconAlert size={14} className="mt-0.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="break-words">
                {sessionGone
                  ? "会话已在服务端删除，重试不会恢复；重新连接这台主机后在本标签继续浏览"
                  : describeError(entries.error)}
              </div>
              <div className="flex gap-2">
                {!sessionGone && (
                  <button className="nx-link mt-1 text-[11px]" onClick={refresh}>
                    重试
                  </button>
                )}
                {(sessionGone || legacyNotFound) && (
                  <button
                    className="nx-link mt-1 text-[11px]"
                    disabled={reconnecting}
                    onClick={() => void reconnectHost()}
                  >
                    {reconnecting ? "正在重新连接…" : "重新连接这台主机"}
                  </button>
                )}
              </div>
            </div>
          </div>
        ) : list.length === 0 ? (
          <div className="nx-empty">{computer ? "没有可用磁盘" : "这个目录是空的"}</div>
        ) : (
          <div className="relative" style={{ height: virtualizer.getTotalSize() }} role="tree" aria-label="文件">
            {virtualizer.getVirtualItems().map((vi) => {
              const e = list[vi.index];
              const isDir = e.kind === "dir";
              const isSel = selected === e.path;
              const { Icon, tone } = fileVisual(e.name, e.kind);
              return (
                <div
                  key={e.path}
                  role="treeitem"
                  aria-level={1}
                  aria-selected={isSel}
                  tabIndex={0}
                  data-drop-path={isDir ? e.path : undefined}
                  className={`nx-row-reserve-actions flex cursor-pointer items-center gap-2 px-3 focus:bg-neutral-800/50 ${
                    isSel ? "bg-blue-500/[0.14]" : "hover:bg-neutral-800/50"
                  }`}
                  style={{ height: vi.size, transform: `translateY(${vi.start}px)`, position: "absolute", top: 0, left: 0, right: 0 }}
                  onClick={() => setSelected(e.path)}
                  onDoubleClick={() => openEntry(e)}
                  onKeyDown={(ev) => {
                    if (ev.target !== ev.currentTarget) return;
                    if (ev.key === "Enter") {
                      ev.preventDefault();
                      ev.stopPropagation();
                      openEntry(e);
                      return;
                    }
                    if (ev.key === " ") {
                      ev.preventDefault();
                      ev.stopPropagation();
                      setSelected(e.path);
                      return;
                    }
                    if ((ev.key === "F10" && ev.shiftKey) || ev.key === "ContextMenu") {
                      ev.preventDefault();
                      ev.stopPropagation();
                      const r = ev.currentTarget.getBoundingClientRect();
                      openRowMenuAt(e, r.left, r.bottom);
                      return;
                    }
                    if (!computer && (ev.key === "Delete" || ev.key === "Backspace")) {
                      ev.preventDefault();
                      ev.stopPropagation();
                      void removePath(e.path, isDir);
                      return;
                    }
                    browserRowKeyDown(ev);
                  }}
                  onContextMenu={(ev) => openRowMenu(ev, e)}
                  title={
                    isDir
                      ? e.path
                      : isEditableFile(e.name)
                        ? `${e.path} · 双击或按回车使用内置编辑器打开`
                        : `${e.path} · 非文本文件，不能在线编辑`
                  }
                >
                  {computer ? (
                    <IconDrive size={14} className={`shrink-0 ${tone}`} />
                  ) : (
                    <Icon size={14} className={`shrink-0 ${tone}`} />
                  )}
                  <span className={`nx-row-name min-w-0 flex-auto truncate font-mono text-[12px] ${isSel ? "text-neutral-100" : ""}`}>
                    {e.name}
                  </span>
                  <span className="w-24 min-w-0 shrink truncate text-right text-[11px] text-neutral-500 max-[560px]:hidden">
                    {isDir ? "—" : formatSize(e.size)}
                  </span>
                  <span className="w-40 min-w-0 shrink truncate text-right text-[11px] text-neutral-500 max-[560px]:hidden">
                    {e.mtime ? new Date(e.mtime).toLocaleString() : "—"}
                  </span>
                  {coarse ? (
                    <button
                      className="nx-icon-btn nx-icon-btn-sm nx-row-more"
                      title={`更多操作 ${e.name}`}
                      aria-label={`更多操作 ${e.name}`}
                      onClick={(ev) => {
                        ev.stopPropagation();
                        const r = ev.currentTarget.getBoundingClientRect();
                        openRowMenuAt(e, r.left, r.bottom);
                      }}
                      onDoubleClick={(ev) => ev.stopPropagation()}
                    >
                      ⋯
                    </button>
                  ) : (
                    <span className="nx-row-actions">
                      <button
                        className="nx-icon-btn nx-icon-btn-sm"
                        title={`更多操作 ${e.name}`}
                        aria-label={`更多操作 ${e.name}`}
                        onClick={(ev) => {
                          ev.stopPropagation();
                          const r = ev.currentTarget.getBoundingClientRect();
                          openRowMenuAt(e, r.left, r.bottom);
                        }}
                      >
                        ⋯
                      </button>
                    </span>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>

      <TransferPanel key={sessionId} sessionId={sessionId} />

      <div className="flex shrink-0 items-center gap-2 border-t border-neutral-800/60 bg-neutral-950/40 px-3 py-1.5 text-[11px] text-neutral-500">
        <span>{list.length} 项</span>
        {selectedEntry && (
          <>
            <span className="text-neutral-700">|</span>
            <span className="truncate font-mono">{selectedEntry.path}</span>
            <span className="text-neutral-700">|</span>
            <span>
              {selectedEntry.mode} · {selectedEntry.owner}
            </span>
          </>
        )}
      </div>

      <ContextMenu state={menu} onClose={() => setMenu(null)} />
    </div>
  );
}

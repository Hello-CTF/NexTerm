// 文件浏览器（M1-T6）：目录列表 + 上传/下载 + 常用操作 + 虚拟滚动 + 右键菜单。
import { useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from "react";
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
import { fsApi, terminalApi } from "../../ipc/commands";
import { listenEvent, EVENTS, type FsProgressEvent } from "../../ipc/events";
import type { FileEntryDto } from "../../ipc/types";
import {
  cdCommandFor,
  findWritableTerminal,
  openFileTab,
  openFileTabInSplit,
  openTerminalTab,
  useUi,
} from "../../app/store";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import { describeError } from "../../ui/errorText";
import { fileVisual, formatSize, isEditableFile, isExtractableArchive } from "./fileTypes";
import { HOME, baseName, joinPath, normalizeTypedPath, parentOf } from "./pathUtils";
import { progressPercent, reduceFileProgress, visibleFileProgress, type FileProgressMap } from "./fileProgress";
import { useFileOps } from "./useFileOps";
import {
  IconAlert,
  IconArchive,
  IconArrowUp,
  IconCopy,
  IconDownload,
  IconEdit,
  IconFolder,
  IconFolderOpen,
  IconFolderPlus,
  IconLock,
  IconRefresh,
  IconShieldCheck,
  IconTerminal,
  IconTrash,
  IconUpload,
} from "../../ui/icons";

export function FileBrowser({ sessionId }: { sessionId: string }) {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  // 根与左栏文件树保持一致（`~` 由后端展开），否则同一个会话在两处看到的不是同一个目录
  const [path, setPath] = useState(HOME);
  /** 地址栏的草稿：**不能**边打边 setPath —— 那会每个字符发一次 fs_list。 */
  const [draft, setDraft] = useState(HOME);
  const [selected, setSelected] = useState<string | null>(null);
  /** 行 / 空白处右键菜单（见 openRowMenu / openBlankMenu）。 */
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  /** rename / chmod / checksum（M60）：与左栏文件树同一套预检与提醒。 */
  const fileOps = useFileOps(sessionId);
  const parentRef = useRef<HTMLDivElement>(null);

  const entries = useQuery({
    queryKey: ["fs", sessionId, path],
    queryFn: () => fsApi.list(sessionId, path),
    refetchOnWindowFocus: false,
  });

  const list = entries.data ?? [];
  const virtualizer = useVirtualizer({
    count: list.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 30,
    overscan: 20,
  });

  const [progress, setProgress] = useState<FileProgressMap>({});
  useEffect(() => {
    const un = listenEvent<FsProgressEvent>(EVENTS.fsProgress, (event) => {
      setProgress((current) => reduceFileProgress(current, event));
    });
    return () => {
      void un.then((f) => f());
    };
  }, []);

  /** 路径变了就同步草稿（点面包屑、进目录、上一级都会走这里）。 */
  useEffect(() => {
    setDraft(path);
  }, [path]);

  const goto = (next: string | null) => {
    if (!next || next === path) return;
    setPath(next);
    setSelected(null);
  };

  const enter = (name: string, kind: string) => {
    if (kind !== "dir") return;
    goto(joinPath(path, name));
  };

  /** 双击文件：能编辑的开编辑器标签，不能编辑的提示走下载。 */
  const openEntry = (entry: (typeof list)[number]) => {
    if (entry.kind === "dir") {
      enter(entry.name, entry.kind);
      return;
    }
    if (!isEditableFile(entry.name)) {
      pushToast("info", `「${entry.name}」不是文本文件，不能在线编辑；用上面的下载按钮取到本机`);
      return;
    }
    openFileTab(sessionId, entry.path);
  };

  /** 上一级；盘符根与 `/` 就停住（`parentOf` 认识 `C:/` 与 `~`）。 */
  const up = () => {
    const parent = parentOf(path);
    if (parent) goto(parent);
  };

  const refresh = () => void qc.invalidateQueries({ queryKey: ["fs", sessionId, path] });
  /** 刷别处：右键往某个子目录上传/新建之后，要失效的是那个目录而不是当前目录。 */
  const refreshDir = (dir: string) =>
    void qc.invalidateQueries({ queryKey: ["fs", sessionId, dir] });

  const uploadTo = async (dir: string) => {
    const file = await pickLocalFile();
    if (!file) return;
    const remote = joinPath(dir, baseName(file));
    pushToast("info", "开始上传…");
    try {
      const bytes = await fsApi.upload(sessionId, file, remote, false);
      refreshDir(dir);
      pushToast("success", `已上传 ${bytes} 字节 → ${remote}`);
    } catch (e) {
      pushToast("error", `上传失败：${describeError(e)}`);
    } finally {
      // 浏览器模式选中的文件会先在盒子上存一份副本（见 `ui/dialogs.ts`），
      // 传完就没有价值了，删掉别让盒子攒垃圾；桌面模式下这个是空操作。
      await discardStaged(file);
    }
  };

  const downloadToLocal = async (remotePath: string) => {
    const target = await pickSavePath(baseName(remotePath));
    if (!target) return;
    pushToast("info", "开始下载…");
    try {
      const bytes = await fsApi.download(sessionId, remotePath, target);
      const where = await finishSave(target, baseName(remotePath));
      pushToast(where ? "success" : "info", where ? `已下载 ${bytes} 字节 → ${where}` : "已取消保存");
    } catch (e) {
      pushToast("error", `下载失败：${describeError(e)}`);
    }
  };

  const removePath = async (remotePath: string, isDir: boolean) => {
    const tip = isDir ? "\n\n目录会被递归删除，不可恢复。" : "";
    if (!(await ask(`删除 ${remotePath}？${tip}`))) return;
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
    const name = await promptText("新建文件夹名");
    if (!name) return;
    const p = joinPath(dir, name);
    try {
      await fsApi.mkdir(sessionId, p);
      refreshDir(dir);
      pushToast("success", `已创建 ${p}`);
    } catch (e) {
      pushToast("error", `创建失败：${describeError(e)}`);
    }
  };

  const copyPath = async (p: string) => {
    try {
      await navigator.clipboard.writeText(p);
      pushToast("success", "路径已复制");
    } catch (e) {
      pushToast("error", `复制失败：${describeError(e)}`);
    }
  };

  /** 「打包下载当前文件夹」：远端 tar.gz → 本地文件。 */
  const packDownload = async (dir: string) => {
    const target = await pickSavePath(`${baseName(dir)}.tar.gz`);
    if (!target) return;
    pushToast("info", "正在远端打包…");
    try {
      const bytes = await fsApi.packDownload(sessionId, dir, target);
      const where = await finishSave(target, `${baseName(dir)}.tar.gz`);
      pushToast(
        where ? "success" : "info",
        where ? `已打包下载 ${bytes} 字节 → ${where}` : "已取消保存",
      );
    } catch (e) {
      pushToast("error", `打包下载失败：${describeError(e)}`);
    }
  };

  /** 「解压」：内核解到「与压缩包同名的目录」，刷掉落点所在那一层。 */
  const extractHere = async (archive: string) => {
    pushToast("info", "正在解压…");
    try {
      const target = await fsApi.extract(sessionId, archive);
      refreshDir(parentOf(target) ?? path);
      pushToast("success", `已解压到 ${target}`);
    } catch (e) {
      pushToast("error", `解压失败：${describeError(e)}`);
    }
  };

  /**
   * 「在终端打开」：新开一个终端标签并 cd 过去。
   *
   * cd 不在这里直接写 —— 此刻 attach 还没完成，没有内核 tabId 可写。
   * 命令挂到标签的 pendingCommand 上，等 XtermView attach 成功再发（与左栏文件树同一套）。
   */
  const openTerminalAt = (dir: string) => {
    const session = useUi.getState().sessions.find((s) => s.id === sessionId);
    if (!session) {
      pushToast("error", "当前会话已不在，无法打开终端");
      return;
    }
    void openTerminalTab(session, undefined, undefined, { command: cdCommandFor(dir) });
  };

  /** 「前进到当前目录」：让已有终端 cd 过去；一个终端都没有就退化成新开一个。 */
  const cdTerminalTo = (dir: string) => {
    const kernelTabId = findWritableTerminal(sessionId);
    if (!kernelTabId) {
      pushToast("info", "当前工作区还没有终端，已改为新开一个");
      openTerminalAt(dir);
      return;
    }
    void terminalApi
      .write(kernelTabId, new TextEncoder().encode(`${cdCommandFor(dir)}\n`))
      .catch((e) => pushToast("error", `切换目录失败：${describeError(e)}`));
  };

  /* ── 右键菜单 ──────────────────────────────────────────────────────── */

  /**
   * 行右键：与左栏文件树保持同一套结构与措辞。
   *
   * 目录落到它自己、文件落到它**所在的目录**（对文件路径 cd 没有意义）；
   * 「进入目录」不再单列 —— 双击就是进入，菜单里再放一条是重复。
   */
  const openRowMenu = (ev: ReactMouseEvent<HTMLDivElement>, entry: FileEntryDto) => {
    ev.preventDefault();
    ev.stopPropagation();
    setSelected(entry.path); // 右键顺手选中，符合直觉
    const isDir = entry.kind === "dir";
    const dir = isDir ? entry.path : (parentOf(entry.path) ?? path);
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
      items.push(
        {
          kind: "item",
          label: "打包下载当前文件夹",
          icon: <IconArchive size={13} />,
          hint: "tar.gz",
          onSelect: () => void packDownload(entry.path),
        },
        {
          kind: "item",
          label: "上传到该目录",
          icon: <IconUpload size={13} />,
          onSelect: () => void uploadTo(entry.path),
        },
        {
          kind: "item",
          label: "新建子文件夹",
          icon: <IconFolderPlus size={13} />,
          onSelect: () => void mkDirIn(entry.path),
        },
      );
    } else {
      items.push({
        kind: "item",
        label: "在下方编辑",
        icon: <IconEdit size={13} />,
        disabled: !isEditableFile(entry.name),
        onSelect: () => openFileTabInSplit(sessionId, entry.path),
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
    // 重命名 / 权限 / 校验值（M60）：目录与文件都有重命名和权限；
    // 校验值只对非目录（符号链接算的是它指向的目标的内容）。
    // rename/chmod 传入的 `path` 是当前列表的缓存键 —— 真实后端的 entry.path
    // 是绝对路径，不能拿它反推缓存键（见 useFileOps 的 listKey 契约）。
    items.push(
      {
        kind: "item",
        label: "重命名",
        icon: <IconEdit size={13} />,
        disabled: fileOps.busy !== null,
        onSelect: () =>
          void fileOps.renameEntry(entry, list, path, (from, to) =>
            setSelected((cur) => (cur === from ? to : cur)),
          ),
      },
      {
        kind: "item",
        label: "权限…",
        icon: <IconLock size={13} />,
        hint: "chmod",
        disabled: fileOps.busy !== null,
        onSelect: () => void fileOps.chmodEntry(entry, path),
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
    setMenu({ x: ev.clientX, y: ev.clientY, title: entry.path, items });
  };

  /**
   * 空白处右键：所有动作都作用于「当前目录」本身。
   *
   * 列表是虚拟滚动的，行之外还有大片空白和"空目录"提示 —— 那些地方同样该能用，
   * 否则用户会以为这块面板不支持右键。
   */
  const openBlankMenu = (ev: ReactMouseEvent<HTMLDivElement>) => {
    ev.preventDefault();
    const items: MenuItem[] = [
      { kind: "group", label: "当前目录" },
      {
        kind: "item",
        label: "打包下载当前文件夹",
        icon: <IconArchive size={13} />,
        hint: "tar.gz",
        onSelect: () => void packDownload(path),
      },
      {
        kind: "item",
        label: "上传到当前目录",
        icon: <IconUpload size={13} />,
        onSelect: () => void uploadTo(path),
      },
      {
        kind: "item",
        label: "新建文件夹",
        icon: <IconFolderPlus size={13} />,
        onSelect: () => void mkDirIn(path),
      },
      { kind: "item", label: "刷新", icon: <IconRefresh size={13} />, onSelect: refresh },
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
    ];
    setMenu({ x: ev.clientX, y: ev.clientY, title: path, items });
  };

  const selectedEntry = list.find((e) => e.path === selected);

  return (
    <div className="nx-pane">
      <div className="nx-toolbar">
        <button
          className="nx-icon-btn"
          onClick={up}
          disabled={!parentOf(path)}
          title="上一级目录"
        >
          <IconArrowUp size={14} />
        </button>
        <div className="nx-field max-w-none">
          <span className="nx-field-icon">
            <IconFolderOpen size={13} />
          </span>
          <input
            className="nx-input nx-input-sm font-mono"
            value={draft}
            spellCheck={false}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                // 先跳转（跳不动就当作刷新）；回车同时给"再查一次当前目录"留个入口
                const next = normalizeTypedPath(draft);
                if (next && next !== path) goto(next);
                else refresh();
              } else if (e.key === "Escape") {
                e.preventDefault();
                setDraft(path);
              }
            }}
            onBlur={() => setDraft(path)}
            placeholder="输入路径后回车，支持 ~ 与 C:/"
            title="直接输入路径后回车跳转"
          />
        </div>
        <button className="nx-icon-btn" onClick={refresh} title="刷新">
          <IconRefresh size={14} />
        </button>
        <span className="nx-divider-v" />
        <button
          className="nx-btn nx-btn-sm"
          onClick={() => void uploadTo(path)}
          title="上传本地文件到当前目录"
        >
          <IconUpload size={13} />
          上传
        </button>
        <button
          className="nx-btn nx-btn-sm"
          disabled={!selected}
          onClick={() => selected && void downloadToLocal(selected)}
          title="下载选中的文件"
        >
          <IconDownload size={13} />
          下载
        </button>
        <button className="nx-btn nx-btn-sm" onClick={() => void mkDirIn(path)} title="新建文件夹">
          <IconFolder size={13} />
          新建
        </button>
        <button
          className="nx-btn nx-btn-danger nx-btn-sm"
          disabled={!selected}
          onClick={() => selected && void removePath(selected, selectedEntry?.kind === "dir")}
        >
          <IconTrash size={13} />
          删除
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
        <span className="w-24 text-right">大小</span>
        <span className="w-40 text-right">修改时间</span>
      </div>

      <div ref={parentRef} className="min-h-0 flex-1 overflow-y-auto" onContextMenu={openBlankMenu}>
        {entries.isLoading ? (
          <div className="nx-hint p-4 text-center">加载中…</div>
        ) : entries.isError ? (
          // 失败不能伪装成空目录（react-query 的 error 不会冒到 window.onerror）
          <div className="nx-alert nx-alert-danger m-3 flex items-start gap-2">
            <IconAlert size={14} className="mt-0.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="break-words">
                {describeError(entries.error)}
              </div>
              <button className="nx-link mt-1 text-[11px]" onClick={refresh}>
                重试
              </button>
            </div>
          </div>
        ) : list.length === 0 ? (
          <div className="nx-empty">这个目录是空的</div>
        ) : (
          <div className="relative" style={{ height: virtualizer.getTotalSize() }}>
            {virtualizer.getVirtualItems().map((vi) => {
              const e = list[vi.index];
              const isDir = e.kind === "dir";
              const isSel = selected === e.path;
              const { Icon, tone } = fileVisual(e.name, e.kind);
              return (
                <div
                  key={e.path}
                  className={`flex cursor-pointer items-center gap-2 px-3 ${
                    isSel ? "bg-blue-500/[0.14]" : "hover:bg-neutral-800/50"
                  }`}
                  style={{ height: vi.size, transform: `translateY(${vi.start}px)`, position: "absolute", top: 0, left: 0, right: 0 }}
                  onClick={() => setSelected(e.path)}
                  onDoubleClick={() => openEntry(e)}
                  onContextMenu={(ev) => openRowMenu(ev, e)}
                  title={isDir ? e.path : `${e.path} · 双击用内置编辑器打开`}
                >
                  <Icon size={14} className={`shrink-0 ${tone}`} />
                  <span className={`min-w-0 flex-1 truncate font-mono text-[12px] ${isSel ? "text-neutral-100" : ""}`}>
                    {e.name}
                  </span>
                  <span className="w-24 shrink-0 text-right text-[11px] text-neutral-500">
                    {isDir ? "—" : formatSize(e.size)}
                  </span>
                  <span className="w-40 shrink-0 text-right text-[11px] text-neutral-500">
                    {e.mtime ? new Date(e.mtime).toLocaleString() : "—"}
                  </span>
                </div>
              );
            })}
          </div>
        )}
      </div>

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

      {/* 菜单 fixed 定位，不占布局 */}
      <ContextMenu state={menu} onClose={() => setMenu(null)} />
    </div>
  );
}

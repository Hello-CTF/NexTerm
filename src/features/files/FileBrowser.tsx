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
import { isImeKeyEvent } from "../../ui/DialogHost";
import { describeError } from "../../ui/errorText";
import { fileVisual, formatSize, isEditableFile, isExtractableArchive } from "./fileTypes";
import { HOME, baseName, joinPath, normalizeTypedPath, parentOf } from "./pathUtils";
import { progressPercent, reduceFileProgress, visibleFileProgress, type FileProgressMap } from "./fileProgress";
import { checkUploadOverwrite } from "./uploadConfirm";
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
  const [path, setPath] = useState(HOME);
  const [draft, setDraft] = useState(HOME);
  const [selected, setSelected] = useState<string | null>(null);
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
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

  const up = () => {
    const parent = parentOf(path);
    if (parent) goto(parent);
  };

  const refresh = () => void qc.invalidateQueries({ queryKey: ["fs", sessionId, path] });
  const refreshDir = (dir: string) =>
    void qc.invalidateQueries({ queryKey: ["fs", sessionId, dir] });

  const uploadTo = async (dir: string) => {
    const file = await pickLocalFile();
    if (!file) return;
    const remote = joinPath(dir, baseName(file));
    try {
      const check = await checkUploadOverwrite(sessionId, dir, remote, dir === path ? list : undefined);
      if (check === "cancelled") {
        pushToast("info", `已取消上传，${remote} 保持原样`);
        return;
      }
      pushToast("info", "开始上传…");
      const bytes = await fsApi.upload(sessionId, file, remote, false);
      refreshDir(dir);
      pushToast("success", `已上传 ${bytes} 字节 → ${remote}`);
    } catch (e) {
      pushToast("error", `上传失败：${describeError(e)}`);
    } finally {
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

  const openTerminalAt = (dir: string) => {
    const session = useUi.getState().sessions.find((s) => s.id === sessionId);
    if (!session) {
      pushToast("error", "当前会话已断开，无法打开终端");
      return;
    }
    void openTerminalTab(session, undefined, undefined, { command: cdCommandFor(dir) });
  };

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

  const openRowMenuAt = (entry: FileEntryDto, x: number, y: number) => {
    setSelected(entry.path);
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
    setMenu({ x, y, title: entry.path, items });
  };

  const openRowMenu = (ev: ReactMouseEvent<HTMLDivElement>, entry: FileEntryDto) => {
    ev.preventDefault();
    ev.stopPropagation();
    openRowMenuAt(entry, ev.clientX, ev.clientY);
  };

  const openBlankMenuAt = (x: number, y: number) => {
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
    setMenu({ x, y, title: path, items });
  };

  const openBlankMenu = (ev: ReactMouseEvent<HTMLDivElement>) => {
    ev.preventDefault();
    openBlankMenuAt(ev.clientX, ev.clientY);
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
        <span className="nx-divider-v max-[560px]:hidden" />
        <button
          className="nx-btn nx-btn-sm max-[560px]:hidden"
          onClick={() => void uploadTo(path)}
          title="上传本地文件到当前目录"
        >
          <IconUpload size={13} />
          上传
        </button>
        <button
          className="nx-btn nx-btn-sm max-[560px]:hidden"
          disabled={!selected}
          onClick={() => selected && void downloadToLocal(selected)}
          title="下载选中的文件"
        >
          <IconDownload size={13} />
          下载
        </button>
        <button
          className="nx-btn nx-btn-sm max-[560px]:hidden"
          onClick={() => void mkDirIn(path)}
          title="新建文件夹"
        >
          <IconFolder size={13} />
          新建
        </button>
        <button
          className="nx-btn nx-btn-danger nx-btn-sm max-[560px]:hidden"
          disabled={!selected}
          onClick={() => selected && void removePath(selected, selectedEntry?.kind === "dir")}
        >
          <IconTrash size={13} />
          删除
        </button>
        <button
          className="nx-icon-btn"
          title="更多操作（打包下载 / 上传 / 新建文件夹 / 刷新）"
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

      <div ref={parentRef} className="min-h-0 flex-1 overflow-y-auto" onContextMenu={openBlankMenu}>
        {entries.isLoading ? (
          <div className="nx-hint p-4 text-center">加载中…</div>
        ) : entries.isError ? (
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
                  <span className="w-24 shrink-0 text-right text-[11px] text-neutral-500 max-[560px]:hidden">
                    {isDir ? "—" : formatSize(e.size)}
                  </span>
                  <span className="w-40 shrink-0 text-right text-[11px] text-neutral-500 max-[560px]:hidden">
                    {e.mtime ? new Date(e.mtime).toLocaleString() : "—"}
                  </span>
                  <span className="nx-row-actions [@media(pointer:coarse)]:flex">
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

      <ContextMenu state={menu} onClose={() => setMenu(null)} />
    </div>
  );
}

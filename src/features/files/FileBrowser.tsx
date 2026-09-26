// 文件浏览器（M1-T6）：目录列表 + 上传/下载 + 常用操作 + 虚拟滚动。
import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ask, pickLocalFile, pickSavePath, promptText } from "../../ui/dialogs";
import { fsApi } from "../../ipc/commands";
import { listenEvent, EVENTS, type FsProgressEvent } from "../../ipc/events";
import { openFileTab, useUi } from "../../app/store";
import { fileVisual, formatSize, isEditableFile } from "./fileTypes";
import {
  IconArrowUp,
  IconDownload,
  IconFolder,
  IconFolderOpen,
  IconRefresh,
  IconTrash,
  IconUpload,
} from "../../ui/icons";

export function FileBrowser({ sessionId }: { sessionId: string }) {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  const [path, setPath] = useState("/");
  const [selected, setSelected] = useState<string | null>(null);
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

  const [progress, setProgress] = useState<FsProgressEvent | null>(null);
  useEffect(() => {
    const un = listenEvent<FsProgressEvent>(EVENTS.fsProgress, (p) => setProgress(p));
    return () => {
      void un.then((f) => f());
    };
  }, []);

  const enter = (name: string, kind: string) => {
    if (kind !== "dir") return;
    const next = path.endsWith("/") ? path + name : `${path}/${name}`;
    setPath(next);
    setSelected(null);
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

  const up = () => {
    if (path === "/") return;
    const parts = path.split("/").filter(Boolean);
    parts.pop();
    setPath("/" + parts.join("/"));
  };

  const refresh = () => void qc.invalidateQueries({ queryKey: ["fs", sessionId, path] });

  const upload = async () => {
    const file = await pickLocalFile();
    if (!file) return;
    const remote = (path.endsWith("/") ? path : path + "/") + file.split(/[\\/]/).pop();
    pushToast("info", "开始上传…");
    try {
      await fsApi.upload(sessionId, file, remote, false);
      refresh();
      pushToast("success", `已上传到 ${remote}`);
    } catch (e) {
      pushToast("error", `上传失败: ${String(e)}`);
    }
  };

  const download = async () => {
    if (!selected) return;
    const target = await pickSavePath(selected.split("/").pop() ?? "download");
    if (!target) return;
    pushToast("info", "开始下载…");
    try {
      await fsApi.download(sessionId, selected, target);
      pushToast("success", `已下载到 ${target}`);
    } catch (e) {
      pushToast("error", `下载失败: ${String(e)}`);
    }
  };

  const deleteSelected = async () => {
    if (!selected) return;
    const entry = list.find((e) => e.path === selected);
    const isDir = entry?.kind === "dir";
    if (!(await ask(`删除 ${selected}？${isDir ? "\n\n目录会被递归删除，不可恢复。" : ""}`))) return;
    try {
      await fsApi.delete(sessionId, selected, isDir);
      setSelected(null);
      refresh();
      pushToast("success", "已删除");
    } catch (e) {
      pushToast("error", `删除失败: ${String(e)}`);
    }
  };

  const mkDir = async () => {
    const name = await promptText("新建文件夹名");
    if (!name) return;
    const p = (path.endsWith("/") ? path : path + "/") + name;
    try {
      await fsApi.mkdir(sessionId, p);
      refresh();
      pushToast("success", `已创建 ${p}`);
    } catch (e) {
      pushToast("error", `创建失败: ${String(e)}`);
    }
  };

  const selectedEntry = list.find((e) => e.path === selected);

  return (
    <div className="nx-pane">
      <div className="nx-toolbar">
        <button
          className="nx-icon-btn"
          onClick={up}
          disabled={path === "/"}
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
            value={path}
            onChange={(e) => setPath(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && refresh()}
            title="直接输入路径后回车跳转"
          />
        </div>
        <button className="nx-icon-btn" onClick={refresh} title="刷新">
          <IconRefresh size={14} />
        </button>
        <span className="nx-divider-v" />
        <button className="nx-btn nx-btn-sm" onClick={() => void upload()} title="上传本地文件到当前目录">
          <IconUpload size={13} />
          上传
        </button>
        <button
          className="nx-btn nx-btn-sm"
          disabled={!selected}
          onClick={() => void download()}
          title="下载选中的文件"
        >
          <IconDownload size={13} />
          下载
        </button>
        <button className="nx-btn nx-btn-sm" onClick={() => void mkDir()} title="新建文件夹">
          <IconFolder size={13} />
          新建
        </button>
        <button
          className="nx-btn nx-btn-danger nx-btn-sm"
          disabled={!selected}
          onClick={() => void deleteSelected()}
        >
          <IconTrash size={13} />
          删除
        </button>
      </div>

      {progress && !progress.done && progress.total > 0 && (
        <div className="nx-progress shrink-0">
          <div
            className="nx-progress-bar"
            style={{ width: `${Math.round((progress.transferred / progress.total) * 100)}%` }}
          />
        </div>
      )}

      <div className="flex shrink-0 items-center gap-2 border-b border-neutral-800/60 px-3 py-1.5 text-[11px] text-neutral-500">
        <span className="w-[18px]" />
        <span className="flex-1">名称</span>
        <span className="w-24 text-right">大小</span>
        <span className="w-40 text-right">修改时间</span>
      </div>

      <div ref={parentRef} className="min-h-0 flex-1 overflow-y-auto">
        {entries.isLoading ? (
          <div className="nx-hint p-4 text-center">加载中…</div>
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
    </div>
  );
}

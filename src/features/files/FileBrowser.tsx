// 文件浏览器（M1-T6）：目录列表 + 上传/下载 + 常用操作 + 虚拟滚动。
import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { open, save } from "@tauri-apps/plugin-dialog";
import { ask, promptText } from "../../ui/dialogs";
import { fsApi } from "../../ipc/commands";
import { listenEvent, EVENTS, type FsProgressEvent } from "../../ipc/events";
import { useUi } from "../../app/store";

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
    estimateSize: () => 28,
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

  const up = () => {
    if (path === "/") return;
    const parts = path.split("/").filter(Boolean);
    parts.pop();
    setPath("/" + parts.join("/"));
  };

  const upload = async () => {
    const file = await open({ multiple: false });
    if (!file) return;
    const remote = (path.endsWith("/") ? path : path + "/") + file.split(/[\\/]/).pop();
    pushToast("info", "开始上传…");
    try {
      await fsApi.upload(sessionId, file, remote, false);
      void qc.invalidateQueries({ queryKey: ["fs", sessionId, path] });
      pushToast("success", "上传完成");
    } catch (e) {
      pushToast("error", `上传失败: ${String(e)}`);
    }
  };

  const download = async () => {
    if (!selected) return;
    const target = await save({ defaultPath: selected.split("/").pop() });
    if (!target) return;
    pushToast("info", "开始下载…");
    try {
      await fsApi.download(sessionId, selected, target);
      pushToast("success", "下载完成");
    } catch (e) {
      pushToast("error", `下载失败: ${String(e)}`);
    }
  };

  const deleteSelected = async () => {
    if (!selected) return;
    const entry = list.find((e) => e.path === selected);
    if (!(await ask(`删除 ${selected}？`))) return;
    try {
      await fsApi.delete(sessionId, selected, entry?.kind === "dir");
      void qc.invalidateQueries({ queryKey: ["fs", sessionId, path] });
    } catch (e) {
      pushToast("error", `删除失败: ${String(e)}`);
    }
  };

  const mkDir = async () => {
    const name = await promptText("新建文件夹名");
    if (!name) return;
    const p = (path.endsWith("/") ? path : path + "/") + name;
    await fsApi.mkdir(sessionId, p);
    void qc.invalidateQueries({ queryKey: ["fs", sessionId, path] });
  };

  const total = list.length;
  const sizeSummary = useMemo(
    () => `${total} 项`,
    [total],
  );

  return (
    <div className="flex h-full flex-col bg-neutral-900 text-sm text-neutral-300">
      <div className="flex items-center gap-1 border-b border-neutral-800 px-2 py-1.5">
        <button className="rounded px-2 py-0.5 hover:bg-neutral-800" onClick={up}>
          ↑
        </button>
        <input
          className="min-w-0 flex-1 rounded bg-neutral-800 px-2 py-0.5 font-mono text-xs outline-none"
          value={path}
          onChange={(e) => setPath(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") void qc.invalidateQueries({ queryKey: ["fs", sessionId, path] });
          }}
        />
        <button className="rounded px-2 py-0.5 hover:bg-neutral-800" onClick={() => void upload()}>
          上传
        </button>
        <button
          className="rounded px-2 py-0.5 hover:bg-neutral-800 disabled:opacity-40"
          disabled={!selected}
          onClick={() => void download()}
        >
          下载
        </button>
        <button className="rounded px-2 py-0.5 hover:bg-neutral-800" onClick={() => void mkDir()}>
          新建文件夹
        </button>
        <button
          className="rounded px-2 py-0.5 text-red-400 hover:bg-neutral-800 disabled:opacity-40"
          disabled={!selected}
          onClick={() => void deleteSelected()}
        >
          删除
        </button>
      </div>
      {progress && !progress.done && progress.total > 0 && (
        <div className="h-1 w-full bg-neutral-800">
          <div
            className="h-1 bg-blue-500 transition-all"
            style={{ width: `${Math.round((progress.transferred / progress.total) * 100)}%` }}
          />
        </div>
      )}
      <div className="flex items-center gap-2 border-b border-neutral-800 px-3 py-1 text-xs text-neutral-500">
        <span className="w-10">类型</span>
        <span className="flex-1">名称</span>
        <span className="w-24 text-right">大小</span>
        <span className="w-36 text-right">修改时间</span>
      </div>
      <div ref={parentRef} className="min-h-0 flex-1 overflow-y-auto">
        {entries.isLoading ? (
          <div className="p-4 text-center text-xs text-neutral-500">加载中…</div>
        ) : (
          <div style={{ height: virtualizer.getTotalSize(), position: "relative" }}>
            {virtualizer.getVirtualItems().map((vi) => {
              const e = list[vi.index];
              return (
                <div
                  key={e.path}
                  className={`absolute left-0 top-0 flex w-full cursor-pointer items-center gap-2 px-3 hover:bg-neutral-800/60 ${
                    selected === e.path ? "bg-blue-900/40" : ""
                  }`}
                  style={{ height: vi.size, transform: `translateY(${vi.start}px)` }}
                  onClick={() => setSelected(e.path)}
                  onDoubleClick={() => enter(e.name, e.kind)}
                  title={e.name}
                >
                  <span className="w-10 text-center">{e.kind === "dir" ? "📁" : "📄"}</span>
                  <span className="flex-1 truncate font-mono">{e.name}</span>
                  <span className="w-24 text-right text-xs text-neutral-500">
                    {e.kind === "dir" ? "-" : formatSize(e.size)}
                  </span>
                  <span className="w-36 text-right text-xs text-neutral-500">
                    {e.mtime ? new Date(e.mtime).toLocaleString() : "-"}
                  </span>
                </div>
              );
            })}
          </div>
        )}
      </div>
      <div className="border-t border-neutral-800 px-3 py-1 text-xs text-neutral-500">{sizeSummary}</div>
    </div>
  );
}

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(2)} GB`;
}

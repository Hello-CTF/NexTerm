// 左栏文件树（连上资产后的常驻左栏）。
//
// 背景：这个产品长期用在「先连一台机器，然后一直在这台机器上干活」的节奏上。
// 所以左栏的默认形态不是资产列表，而是**当前会话的文件树**——
// 资产列表退到图标栏的「资产」按钮后面（它依然是资产管理的主入口）。
//
// 三条交互约定（对齐参考实现）：
//   · 单击选中整行；双击目录展开/收起，双击文件开编辑器标签；
//   · 目录列表按需加载（react-query 按目录缓存，与宽幅文件浏览器共用同一 key）；
//   · 工具栏与右键无关，一切动作都能从工具栏或行内 hover 完成。
import { useEffect, useMemo, useState } from "react";
import { useQueries, useQueryClient } from "@tanstack/react-query";
import type { FileEntryDto } from "../../ipc/types";
import { fsApi } from "../../ipc/commands";
import { ask, pickLocalFile, pickSavePath, promptText } from "../../ui/dialogs";
import { openFileTab, useUi } from "../../app/store";
import { fileVisual, formatSize, isEditableFile } from "./fileTypes";
import {
  IconArrowUp,
  IconChevronDown,
  IconChevronRight,
  IconDownload,
  IconFilePlus,
  IconFoldAll,
  IconFolderPlus,
  IconHome,
  IconRefresh,
  IconTrash,
  IconUpload,
  IconXCircle,
} from "../../ui/icons";

/**
 * 初始根目录。
 *
 * `~` 现在**本地会话与 SSH 会话都由后端展开**（`transport/local.rs` 用 `dirs::home_dir()`，
 * `transport/ssh.rs` 用 SFTP `realpath(".")`），所以正常情况会直接落在用户家目录。
 *
 * 但仍要保留降级路径：WinRM 之类没有家目录概念的通道、以及将来可能出现的后端实现，
 * 都可能在 `~` 上直接报错。碰到就干脆地退到 `/`，**并且把家目录入口关掉** ——
 * 否则点了「回到家目录」会被立刻打回 `/`，看上去就是按钮坏了。
 */
const HOME = "~";

/* ── 路径工具 ──────────────────────────────────────────────────────────── */

/** 后端在 Windows 本地会话上给的是 `C:\Users\x\y`，统一成正斜杠再算路径。 */
function norm(path: string): string {
  return path.replace(/\\/g, "/");
}

/** 到顶了没有：类 Unix 的 `/` 或盘符根 `C:/`。 */
function isRoot(path: string): boolean {
  const p = norm(path);
  return p === "/" || /^[A-Za-z]:\/$/.test(p);
}

function joinPath(dir: string, name: string): string {
  return dir.endsWith("/") ? dir + name : `${dir}/${name}`;
}

/** 上一级；到根返回 null。`~` 的上一级是 `/`（允许从家目录向上浏览整机）。 */
function parentOf(path: string): string | null {
  const p = norm(path);
  if (isRoot(p)) return null;
  if (p === HOME) return "/";
  const i = p.lastIndexOf("/");
  if (i <= 0) return "/";
  const parent = p.slice(0, i);
  // `C:` 要补回盘符根的形式，否则下一次 list 会落到"当前盘的工作目录"
  return /^[A-Za-z]:$/.test(parent) ? `${parent}/` : parent;
}

function baseName(path: string): string {
  return norm(path).split("/").filter(Boolean).pop() ?? path;
}

/** 面包屑：`/`、`~`、盘符根都作为首段，每段可点击跳转。 */
function crumbsOf(path: string): { label: string; path: string }[] {
  const p = norm(path);
  if (p.startsWith("~")) {
    const out = [{ label: "~", path: HOME }];
    let acc = HOME;
    for (const s of p.slice(1).split("/").filter(Boolean)) {
      acc = joinPath(acc, s);
      out.push({ label: s, path: acc });
    }
    return out;
  }
  const drive = /^([A-Za-z]):/.exec(p);
  const rootLabel = drive ? `${drive[1]}:` : "/";
  const rootPath = drive ? `${drive[1]}:/` : "/";
  const out = [{ label: rootLabel, path: rootPath }];
  let acc = rootPath;
  for (const s of p.slice(rootPath.length).split("/").filter(Boolean)) {
    acc = joinPath(acc, s);
    out.push({ label: s, path: acc });
  }
  return out;
}

/** 目录在前、其余按名称排序（贴近 ls / 文件管理器的习惯）。 */
function sortEntries(list: FileEntryDto[]): FileEntryDto[] {
  return [...list].sort((a, b) => {
    const ad = a.kind === "dir" ? 0 : 1;
    const bd = b.kind === "dir" ? 0 : 1;
    if (ad !== bd) return ad - bd;
    return a.name.localeCompare(b.name, "en", { numeric: true });
  });
}

/* ── 组件 ──────────────────────────────────────────────────────────────── */

export function FileTree({ sessionId }: { sessionId: string }) {
  const qc = useQueryClient();
  const { pushToast, leftOpen } = useUi();
  const [root, setRoot] = useState(HOME);
  const [expanded, setExpanded] = useState<string[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  /** 后端认不认 `~`。探测到不认就置 false，家目录入口随之关闭（见 HOME 的注释）。 */
  const [homeSupported, setHomeSupported] = useState(true);

  /** 需要加载的目录 = 根 + 所有展开项（含根，保证根被展开时也在列表里）。 */
  const dirs = useMemo(
    () => [root, ...expanded.filter((d) => d !== root)],
    [root, expanded],
  );

  const results = useQueries({
    queries: dirs.map((dir) => ({
      queryKey: ["fs", sessionId, dir],
      queryFn: () => fsApi.list(sessionId, dir),
      refetchOnWindowFocus: false,
      // 后端不认 `~` 时不要反复重试，直接走下面的退回逻辑
      retry: false,
    })),
  });

  const rootQuery = results[0];

  useEffect(() => {
    if (root !== HOME || !rootQuery?.isError) return;
    // 后端不认 `~`：退到 `/`，关掉家目录入口，并明确告诉用户为什么不在家目录。
    setHomeSupported(false);
    setRoot("/");
    pushToast("info", "当前后端不支持 ~ 展开，文件树已改从根目录 / 开始");
  }, [root, rootQuery?.isError, pushToast]);

  const dirMap = useMemo(() => {
    const map = new Map<string, FileEntryDto[]>();
    dirs.forEach((dir, i) => {
      const data = results[i]?.data;
      if (data) map.set(dir, sortEntries(data));
    });
    return map;
    // results 每次渲染都是新数组，这里按内容较浅地依赖即可
  }, [dirs, results]);

  /**
   * 每个目录的读失败原因。
   *
   * 必须显式拿出来：react-query 的 error 不会冒到 window.onerror，
   * 之前所有失败都被渲染成「这个目录是空的」—— 一次 SFTP 握手超时看起来就只是"空目录"，
   * SSH 下整个文件树废了却完全看不出原因。
   */
  const dirErrors = useMemo(() => {
    const map = new Map<string, string>();
    dirs.forEach((dir, i) => {
      const err = results[i]?.error;
      if (err) map.set(dir, String((err as { message?: string })?.message ?? err));
    });
    return map;
  }, [dirs, results]);

  /** 扁平化成可渲染的行（只有展开的目录才会展开其子项）。 */
  const rows = useMemo(() => {
    const out: { entry?: FileEntryDto; depth: number; error?: string; dir: string }[] = [];
    const walk = (dir: string, depth: number) => {
      for (const entry of dirMap.get(dir) ?? []) {
        out.push({ entry, depth, dir });
        if (entry.kind === "dir" && expanded.includes(entry.path)) {
          walk(entry.path, depth + 1);
        }
      }
      const err = dirErrors.get(dir);
      if (err) out.push({ depth, error: err, dir });
    };
    walk(root, 0);
    return out;
  }, [dirMap, root, expanded, dirErrors]);

  /** 重试某个目录（失败后点「重试」）。 */
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

  const refresh = () => void qc.invalidateQueries({ queryKey: ["fs", sessionId] });

  const toggle = (path: string) =>
    setExpanded((prev) =>
      prev.includes(path) ? prev.filter((p) => p !== path) : [...prev, path],
    );

  const openFile = (entry: FileEntryDto) => {
    if (!isEditableFile(entry.name)) {
      pushToast("info", `「${entry.name}」不是文本文件，不能在线编辑；可用工具栏下载到本机`);
      return;
    }
    openFileTab(sessionId, entry.path);
  };

  /** 新建/上传的目标目录：选中目录就是它本身，选中文件则是它所在目录。 */
  const targetDir = (): string => {
    if (selected) {
      const entry = findEntry(selected);
      if (entry?.kind === "dir") return selected;
      return parentOf(selected) ?? root;
    }
    return root;
  };

  const newFile = async () => {
    const name = await promptText("新建文件名", "");
    if (!name) return;
    const path = joinPath(targetDir(), name);
    try {
      await fsApi.write(sessionId, path, "", false);
      refresh();
      pushToast("success", `已创建 ${path}`);
    } catch (e) {
      pushToast("error", `创建失败：${String(e)}`);
    }
  };

  const newDir = async () => {
    const name = await promptText("新建文件夹名", "");
    if (!name) return;
    const path = joinPath(targetDir(), name);
    try {
      await fsApi.mkdir(sessionId, path);
      refresh();
      if (!expanded.includes(targetDir())) toggle(targetDir());
      pushToast("success", `已创建 ${path}`);
    } catch (e) {
      pushToast("error", `创建失败：${String(e)}`);
    }
  };

  const upload = async () => {
    const file = await pickLocalFile();
    if (!file) return;
    const remote = joinPath(targetDir(), file.split(/[\\/]/).pop() ?? "upload.bin");
    pushToast("info", "开始上传…");
    try {
      await fsApi.upload(sessionId, file, remote, false);
      refresh();
      pushToast("success", `已上传到 ${remote}`);
    } catch (e) {
      pushToast("error", `上传失败：${String(e)}`);
    }
  };

  const download = async (path: string) => {
    const target = await pickSavePath(baseName(path));
    if (!target) return;
    pushToast("info", "开始下载…");
    try {
      await fsApi.download(sessionId, path, target);
      pushToast("success", `已下载到 ${target}`);
    } catch (e) {
      pushToast("error", `下载失败：${String(e)}`);
    }
  };

  const remove = async (path: string) => {
    const entry = findEntry(path);
    const isDir = entry?.kind === "dir";
    const tip = isDir ? "\n\n目录会被递归删除，不可恢复。" : "";
    if (!(await ask(`删除 ${path}？${tip}`))) return;
    try {
      await fsApi.delete(sessionId, path, isDir);
      setSelected(null);
      setExpanded((prev) => prev.filter((p) => p !== path));
      refresh();
      pushToast("success", "已删除");
    } catch (e) {
      pushToast("error", `删除失败：${String(e)}`);
    }
  };

  if (!leftOpen) return null;

  const crumbs = crumbsOf(root);
  const up = parentOf(root);
  const loading = dirs.some((_, i) => results[i]?.isLoading);

  return (
    <aside className="flex h-full w-[248px] shrink-0 flex-col border-r border-neutral-800/60 bg-neutral-950">
      {/* 工具栏：一行放完，全部有 tooltip；危险动作是最后一个 */}
      <div className="flex h-[34px] shrink-0 items-center gap-0.5 px-2">
        <span className="mr-1 shrink-0 text-xs font-semibold tracking-wide text-neutral-200">
          文件
        </span>
        <button className="nx-icon-btn nx-icon-btn-sm" title="新建文件" onClick={() => void newFile()}>
          <IconFilePlus size={14} />
        </button>
        <button className="nx-icon-btn nx-icon-btn-sm" title="新建文件夹" onClick={() => void newDir()}>
          <IconFolderPlus size={14} />
        </button>
        <button className="nx-icon-btn nx-icon-btn-sm" title="刷新" onClick={refresh}>
          <IconRefresh size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="折叠全部"
          onClick={() => {
            setExpanded([]);
            setSelected(null);
          }}
        >
          <IconFoldAll size={14} />
        </button>
        <div className="nx-spacer" />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="上传到当前目录"
          onClick={() => void upload()}
        >
          <IconUpload size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="下载选中项"
          disabled={!selected || selectedEntry?.kind === "dir"}
          onClick={() => selected && void download(selected)}
        >
          <IconDownload size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm is-danger"
          title="删除选中项"
          disabled={!selected}
          onClick={() => selected && void remove(selected)}
        >
          <IconTrash size={14} />
        </button>
      </div>

      {/* 路径面包屑：↑ 上级 · 各段可点 · 🏠 回 ~ */}
      <div className="nx-pathbar">
        <button
          className="nx-tree-caret"
          title={up ? `上级：${up}` : "已经在根目录"}
          disabled={!up}
          onClick={() => up && setRoot(up)}
        >
          <IconArrowUp size={12} />
        </button>
        <div className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto">
          {crumbs.map((c, i) => (
            <span key={c.path} className="flex shrink-0 items-center gap-0.5">
              {i > 0 && <span className="text-neutral-600">/</span>}
              <button
                className={`nx-path-crumb ${i === crumbs.length - 1 ? "is-current" : ""}`}
                onClick={() => setRoot(c.path)}
                title={c.path}
              >
                {c.label}
              </button>
            </span>
          ))}
        </div>
        <button
          className="nx-tree-caret"
          title={
            homeSupported
              ? "回到家目录 (~)"
              : "该后端不支持 ~ 展开（本地 / SFTP 后端都不认）"
          }
          disabled={!homeSupported || root === HOME}
          onClick={() => {
            if (homeSupported) setRoot(HOME);
          }}
        >
          <IconHome size={12} />
        </button>
      </div>

      {/* 目录树 */}
      <div className="min-h-0 flex-1 overflow-y-auto px-1 py-1">
        {rows.map(({ entry, depth, error, dir }) => {
          // 失败的行：把后端的原话摆出来，而不是假装"空目录"
          if (error) {
            return (
              <div
                key={`err:${dir}`}
                className="mx-1 my-0.5 flex items-start gap-1.5 rounded border border-red-500/30 bg-red-950/30 px-2 py-1.5"
                style={{ marginLeft: 2 + depth * 12 }}
              >
                <IconXCircle size={12} className="mt-0.5 shrink-0 text-red-300" />
                <div className="min-w-0 flex-1 text-[11px] leading-relaxed text-red-200">
                  <div className="break-words">{error}</div>
                  <button
                    className="nx-link mt-0.5 text-[11px]"
                    onClick={() => retryDir(dir)}
                  >
                    重试
                  </button>
                </div>
              </div>
            );
          }
          if (!entry) return null;
          const isDir = entry.kind === "dir";
          const open = expanded.includes(entry.path);
          const isSel = selected === entry.path;
          const { Icon, tone } = fileVisual(entry.name, entry.kind);
          return (
            <div
              key={entry.path}
              className={`nx-row group ${isSel ? "is-selected" : ""}`}
              style={{ paddingLeft: 2 + depth * 12 }}
              onClick={() => setSelected(entry.path)}
              onDoubleClick={() => (isDir ? toggle(entry.path) : openFile(entry))}
              title={entry.kind === "symlink" ? `${entry.path} → ${entry.symlinkTarget}` : entry.path}
            >
              {isDir ? (
                <button
                  className="nx-tree-caret"
                  onClick={(e) => {
                    e.stopPropagation();
                    toggle(entry.path);
                  }}
                  title={open ? "收起" : "展开"}
                >
                  {open ? <IconChevronDown size={11} /> : <IconChevronRight size={11} />}
                </button>
              ) : (
                <span className="w-[14px] shrink-0" />
              )}
              <Icon size={13} className={`shrink-0 ${tone}`} />
              <span className="min-w-0 flex-1 truncate text-[12.5px]">{entry.name}</span>
              <span className="nx-row-actions">
                {!isDir && (
                  <button
                    className="nx-icon-btn nx-icon-btn-sm"
                    title="下载"
                    onClick={(e) => {
                      e.stopPropagation();
                      void download(entry.path);
                    }}
                  >
                    <IconDownload size={11} />
                  </button>
                )}
                <button
                  className="nx-icon-btn nx-icon-btn-sm is-danger"
                  title="删除"
                  onClick={(e) => {
                    e.stopPropagation();
                    void remove(entry.path);
                  }}
                >
                  <IconTrash size={11} />
                </button>
              </span>
            </div>
          );
        })}

        {loading && rows.length === 0 && <div className="nx-hint px-2 py-6 text-center">加载中…</div>}
        {!loading && rows.length === 0 && dirErrors.size === 0 && (
          <div className="nx-hint px-2 py-6 text-center">
            这个目录是空的
            <br />
            双击左侧目录可展开
          </div>
        )}
      </div>

      {/* 底栏：数量 + 选中详情 */}
      <div className="flex h-[24px] shrink-0 items-center gap-2 border-t border-neutral-800/60 px-2.5 text-[10.5px] text-neutral-500">
        <span>{rows.filter((r) => r.entry).length} 项</span>
        {selectedEntry && (
          <>
            <span className="text-neutral-700">|</span>
            <span className="min-w-0 flex-1 truncate" title={selectedEntry.path}>
              {selectedEntry.kind === "dir" ? "目录" : formatSize(selectedEntry.size)}
            </span>
          </>
        )}
      </div>
    </aside>
  );
}

import { useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent as ReactMouseEvent } from "react";
import { useQueries, useQueryClient } from "@tanstack/react-query";
import type { FileEntryDto } from "../../ipc/types";
import { fsApi, terminalApi } from "../../ipc/commands";
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
import {
  cdCommandFor,
  findWritableTerminal,
  openFileTab,
  openFileTabInSplit,
  openTerminalTab,
  useUi,
} from "../../app/store";
import { fileVisual, formatSize, isEditableFile, isExtractableArchive } from "./fileTypes";
import {
  HOME,
  baseName,
  crumbsOf,
  joinPath,
  norm,
  normalizeTypedPath,
  parentOf,
} from "./pathUtils";
import { useFileOps } from "./useFileOps";
import {
  IconArchive,
  IconArrowUp,
  IconChevronDown,
  IconChevronRight,
  IconClose,
  IconDownload,
  IconEdit,
  IconFilePlus,
  IconFoldAll,
  IconFolderPlus,
  IconHome,
  IconLock,
  IconRefresh,
  IconShieldCheck,
  IconTerminal,
  IconTrash,
  IconUpload,
  IconXCircle,
} from "../../ui/icons";

function sortEntries(list: FileEntryDto[]): FileEntryDto[] {
  return [...list].sort((a, b) => {
    const ad = a.kind === "dir" ? 0 : 1;
    const bd = b.kind === "dir" ? 0 : 1;
    if (ad !== bd) return ad - bd;
    return a.name.localeCompare(b.name, "en", { numeric: true });
  });
}

function treeRowKeyDown(event: ReactKeyboardEvent<HTMLElement>): void {
  if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
  const tree = event.currentTarget.closest("[role='tree']");
  if (!tree) return;
  const rows = [...tree.querySelectorAll<HTMLElement>("[role='treeitem']")];
  const index = rows.indexOf(event.currentTarget);
  const next = event.key === "ArrowDown" ? index + 1 : index - 1;
  if (index < 0 || next < 0 || next >= rows.length) return;
  event.preventDefault();
  rows[next]?.focus();
}

export function FileTree({ sessionId }: { sessionId: string }) {
  const qc = useQueryClient();
  const { pushToast, leftOpen, leftWidth } = useUi();
  const [root, setRoot] = useState(HOME);
  const [expanded, setExpanded] = useState<string[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [homeSupported, setHomeSupported] = useState(true);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const crumbRef = useRef<HTMLDivElement>(null);
  const fileOps = useFileOps(sessionId);

  useEffect(() => {
    const el = crumbRef.current;
    if (el) el.scrollLeft = el.scrollWidth;
  }, [root, editing]);

  const beginEdit = () => {
    setDraft(root);
    setEditing(true);
  };

  const commitEdit = () => {
    setEditing(false);
    const next = normalizeTypedPath(draft);
    if (!next || next === root) return;
    setExpanded([]);
    setSelected(null);
    setRoot(next);
  };

  const dirs = useMemo(
    () => [root, ...expanded.filter((d) => d !== root)],
    [root, expanded],
  );

  const results = useQueries({
    queries: dirs.map((dir) => ({
      queryKey: ["fs", sessionId, dir],
      queryFn: () => fsApi.list(sessionId, dir),
      refetchOnWindowFocus: false,
      retry: false,
    })),
  });

  const rootQuery = results[0];

  useEffect(() => {
    if (root !== HOME || !rootQuery?.isError) return;
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
  }, [dirs, results]);

  const dirErrors = useMemo(() => {
    const map = new Map<string, string>();
    dirs.forEach((dir, i) => {
      const err = results[i]?.error;
      if (err) map.set(dir, describeError(err));
    });
    return map;
  }, [dirs, results]);

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
      pushToast("error", `创建失败：${describeError(e)}`);
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
      pushToast("error", `创建失败：${describeError(e)}`);
    }
  };

  const upload = async () => {
    const file = await pickLocalFile();
    if (!file) return;
    const remote = joinPath(targetDir(), baseName(file));
    pushToast("info", "开始上传…");
    try {
      await fsApi.upload(sessionId, file, remote, false);
      refresh();
      pushToast("success", `已上传到 ${remote}`);
    } catch (e) {
      pushToast("error", `上传失败：${describeError(e)}`);
    } finally {
      await discardStaged(file);
    }
  };

  const download = async (path: string) => {
    const target = await pickSavePath(baseName(path));
    if (!target) return;
    pushToast("info", "开始下载…");
    try {
      await fsApi.download(sessionId, path, target);
      const where = await finishSave(target, baseName(path));
      pushToast(where ? "success" : "info", where ? `已下载到 ${where}` : "已取消保存");
    } catch (e) {
      pushToast("error", `下载失败：${describeError(e)}`);
    }
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
      const parent = parentOf(target) ?? root;
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

  const openTerminalAt = (dir: string) => {
    const session = useUi.getState().sessions.find((s) => s.id === sessionId);
    if (!session) {
      pushToast("error", "当前会话已不在，无法打开终端");
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
    if (norm(root) === fromN || norm(root).startsWith(`${fromN}/`)) setRoot(remap(root));
  };

  const siblingsOf = (dir: string): FileEntryDto[] => dirMap.get(dir) ?? [];

  const openRowMenuAt = (entry: FileEntryDto, listKey: string, x: number, y: number) => {
    setSelected(entry.path);
    const isDir = entry.kind === "dir";
    const dir = isDir ? entry.path : (parentOf(entry.path) ?? root);
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
      items.push({
        kind: "item",
        label: "打包下载当前文件夹",
        icon: <IconArchive size={13} />,
        hint: "tar.gz",
        onSelect: () => void packDownload(entry.path),
      });
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
  ) => {
    e.preventDefault();
    e.stopPropagation();
    openRowMenuAt(entry, listKey, e.clientX, e.clientY);
  };

  if (!leftOpen) return null;

  const crumbs = crumbsOf(root);
  const up = parentOf(root);
  const loading = dirs.some((_, i) => results[i]?.isLoading);

  return (
    <aside
      className="flex h-full shrink-0 flex-col border-r border-neutral-800/60 bg-neutral-950"
      style={{ width: leftWidth }}
    >
      <div className="flex h-[34px] shrink-0 items-center gap-0.5 overflow-x-auto px-2">
        <span className="mr-1 shrink-0 text-xs font-semibold tracking-wide text-neutral-200">
          文件
        </span>
        <button className="nx-icon-btn nx-icon-btn-sm" title="新建文件" aria-label="新建文件" onClick={() => void newFile()}>
          <IconFilePlus size={14} />
        </button>
        <button className="nx-icon-btn nx-icon-btn-sm" title="新建文件夹" aria-label="新建文件夹" onClick={() => void newDir()}>
          <IconFolderPlus size={14} />
        </button>
        <button className="nx-icon-btn nx-icon-btn-sm" title="刷新" aria-label="刷新文件列表" onClick={refresh}>
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
        <div className="nx-spacer" />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="上传到当前目录"
          aria-label="上传到当前目录"
          onClick={() => void upload()}
        >
          <IconUpload size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="下载选中项"
          aria-label="下载选中项"
          disabled={!selected || selectedEntry?.kind === "dir"}
          onClick={() => selected && void download(selected)}
        >
          <IconDownload size={14} />
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm is-danger"
          title="删除选中项"
          aria-label="删除选中项"
          disabled={!selected}
          onClick={() => selected && void remove(selected)}
        >
          <IconTrash size={14} />
        </button>
      </div>

      <div className="nx-pathbar">
        <button
          className="nx-tree-caret"
          title={up ? `上级：${up}` : "已经在根目录"}
          aria-label={up ? `上级目录 ${up}` : "已经在根目录"}
          disabled={!up}
          onClick={() => up && setRoot(up)}
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
            {crumbs.map((c, i) => (
              <span key={c.path} className="flex shrink-0 items-center gap-0.5">
                {i > 0 && crumbs[i - 1].label !== "/" && (
                  <span className="text-neutral-600">/</span>
                )}
                <button
                  className={`nx-path-crumb ${i === crumbs.length - 1 ? "is-current" : ""}`}
                  aria-current={i === crumbs.length - 1 ? "location" : undefined}
                  onClick={() => setRoot(c.path)}
                  title={c.path}
                >
                  {c.label}
                </button>
              </span>
            ))}
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
          title={
            homeSupported
              ? "回到家目录 (~)"
              : "该后端不支持 ~ 展开（本地 / SFTP 后端都不认）"
          }
          aria-label={homeSupported ? "回到家目录 (~)" : "该后端不支持 ~ 展开"}
          disabled={!homeSupported || root === HOME}
          onClick={() => {
            if (homeSupported) setRoot(HOME);
          }}
        >
          <IconHome size={12} />
        </button>
      </div>

      <div role="tree" aria-label="文件" className="min-h-0 flex-1 overflow-y-auto px-1 py-1">
        {rows.map(({ entry, depth, error, dir }) => {
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
              role="treeitem"
              aria-level={depth + 1}
              aria-expanded={isDir ? open : undefined}
              aria-selected={isSel}
              tabIndex={0}
              className={`nx-row group focus-within:[&_.nx-row-actions]:flex ${isSel ? "is-selected" : ""}`}
              style={{ paddingLeft: 2 + depth * 12 }}
              onClick={() => {
                setSelected(entry.path);
                if (isDir) toggle(entry.path);
              }}
              onDoubleClick={() => (isDir ? enterDir(entry.path) : openFile(entry))}
              onKeyDown={(e) => {
                if (e.target !== e.currentTarget) return;
                if (e.key === "Enter") {
                  e.preventDefault();
                  if (isDir) enterDir(entry.path);
                  else openFile(entry);
                  return;
                }
                if (e.key === " ") {
                  e.preventDefault();
                  setSelected(entry.path);
                  if (isDir) toggle(entry.path);
                  return;
                }
                if (e.key === "ArrowRight" && isDir && !open) {
                  e.preventDefault();
                  toggle(entry.path);
                  return;
                }
                if (e.key === "ArrowLeft" && isDir && open) {
                  e.preventDefault();
                  toggle(entry.path);
                  return;
                }
                treeRowKeyDown(e);
              }}
              onContextMenu={(e) => openRowMenu(e, entry, dir)}
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
                  aria-label={`${open ? "收起" : "展开"} ${entry.name}`}
                  aria-expanded={open}
                >
                  {open ? <IconChevronDown size={11} /> : <IconChevronRight size={11} />}
                </button>
              ) : (
                <span className="w-[14px] shrink-0" />
              )}
              <Icon size={13} className={`shrink-0 ${tone}`} />
              <span className="min-w-0 flex-1 truncate text-[12.5px]">{entry.name}</span>
              <span className="nx-row-actions [@media(pointer:coarse)]:flex">
                {!isDir && (
                  <button
                    className="nx-icon-btn nx-icon-btn-sm"
                    title="下载"
                    aria-label={`下载 ${entry.name}`}
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
                  aria-label={`删除 ${entry.name}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    void remove(entry.path);
                  }}
                >
                  <IconTrash size={11} />
                </button>
                <button
                  className="nx-icon-btn nx-icon-btn-sm"
                  title={`更多操作 ${entry.name}`}
                  aria-label={`更多操作 ${entry.name}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    const r = e.currentTarget.getBoundingClientRect();
                    openRowMenuAt(entry, dir, r.left, r.bottom);
                  }}
                >
                  ⋯
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
            单击目录名可展开，双击可进入
          </div>
        )}
      </div>

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

      <ContextMenu state={menu} onClose={() => setMenu(null)} />
    </aside>
  );
}

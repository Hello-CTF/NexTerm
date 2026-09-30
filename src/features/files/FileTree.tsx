// 左栏文件树（连上资产后的常驻左栏）。
//
// 背景：这个产品长期用在「先连一台机器，然后一直在这台机器上干活」的节奏上。
// 所以左栏的默认形态不是资产列表，而是**当前会话的文件树**——
// 资产列表退到图标栏的「资产」按钮后面（它依然是资产管理的主入口）。
//
// 三条交互约定（对齐参考实现）：
//   · 单击目录展开/收起（文件则只选中整行）；双击目录「进去」（把浏览根切到它），双击文件开编辑器标签；
//   · 目录列表按需加载（react-query 按目录缓存，与宽幅文件浏览器共用同一 key）；
//   · 工具栏与右键无关，一切动作都能从工具栏或行内 hover 完成。
import { useEffect, useMemo, useState, type MouseEvent as ReactMouseEvent } from "react";
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
  normalizeTypedPath,
  parentOf,
} from "./pathUtils";
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
  IconRefresh,
  IconTerminal,
  IconTrash,
  IconUpload,
  IconXCircle,
} from "../../ui/icons";

/* ── 路径工具见 ./pathUtils（与宽幅文件浏览器共用） ───────────────────── */

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
  const { pushToast, leftOpen, leftWidth } = useUi();
  const [root, setRoot] = useState(HOME);
  const [expanded, setExpanded] = useState<string[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  /** 后端认不认 `~`。探测到不认就置 false，家目录入口随之关闭（见 HOME 的注释）。 */
  const [homeSupported, setHomeSupported] = useState(true);
  /** 路径栏的「直接输入路径」模式：只能在面包屑之间挪是不够用的。 */
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  /** 行右键菜单（见 openRowMenu）。 */
  const [menu, setMenu] = useState<ContextMenuState | null>(null);

  const beginEdit = () => {
    setDraft(root);
    setEditing(true);
  };

  /**
   * 提交手输路径。
   *
   * 归一化后去掉尾斜杠；空输入当取消；`/` 单独保留（否则会被尾斜杠正则吃成空串）。
   * 显式跳转时清掉旧的展开与选中 —— 那是另一棵子树的浏览状态，留着会连带发出
   * 一堆无关目录的请求。
   */
  const commitEdit = () => {
    setEditing(false);
    const next = normalizeTypedPath(draft);
    if (!next || next === root) return;
    setExpanded([]);
    setSelected(null);
    setRoot(next);
  };

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
      if (err) map.set(dir, describeError(err));
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
      // 浏览器模式选中的文件会先在盒子上存一份副本（见 `ui/dialogs.ts`），
      // 传完就没有价值了，删掉别让盒子攒垃圾；桌面模式下这个是空操作。
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
    if (!(await ask(`删除 ${path}？${tip}`))) return;
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

  /** 「进目录」：把浏览根切到该目录 —— 只加载一层，不必从 / 一路展开下来。 */
  const enterDir = (path: string) => {
    if (path === root) return;
    setExpanded([]);
    setSelected(null);
    setRoot(path);
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

  /**
   * 「解压」：内核解到「与压缩包同名的目录」。
   *
   * 成功后必须把落点那一层从缓存里作废 —— 新目录不在已展开的 query key 里，
   * 不刷的话用户点完看着像什么都没发生。
   */
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

  /* ── 右键菜单 ──────────────────────────────────────────────────────── */

  /**
   * 「在终端打开」：新开一个终端标签，并让它落地后 cd 到该目录。
   *
   * cd 不在这里直接写 —— 此刻还没有内核 tabId（attach 是异步的）。
   * 命令挂到标签的 pendingCommand 上，等 XtermView attach 成功再发出去。
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

  /**
   * 行右键。
   *
   * 菜单只留「必须对着某个具体文件/目录做」的动作，其余（编码、录制、
   * 上传下载的图标入口）交给工具栏，别在这里重复一遍。
   * 按类型收敛：目录给「打包下载当前文件夹」，文件给「在下方编辑 / 解压 / 下载」。
   * 目录落到它自己、文件落到它**所在的目录** —— 对文件路径本身 cd 没有意义。
   */
  const openRowMenu = (e: ReactMouseEvent<HTMLDivElement>, entry: FileEntryDto) => {
    e.preventDefault();
    e.stopPropagation();
    setSelected(entry.path); // 右键顺手选中，符合直觉
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
      // 解压只对内核认得的归档出现 —— 给一个点了必然报错的入口更糟
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
    setMenu({ x: e.clientX, y: e.clientY, title: entry.path, items });
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
        {editing ? (
          <input
            className="nx-input nx-input-sm min-w-0 flex-1 font-mono"
            autoFocus
            spellCheck={false}
            value={draft}
            placeholder="输入路径后回车（支持 ~ 与 C:/）"
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
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
            className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto"
            onClick={(e) => {
              // 点面包屑后面的空白处 = 直接改路径（和资源管理器一样的手感）；
              // 点到具体的段上仍然是跳转那一段。
              if (e.target === e.currentTarget) beginEdit();
            }}
            title="点空白处可直接输入路径"
          >
            {crumbs.map((c, i) => (
              <span key={c.path} className="flex shrink-0 items-center gap-0.5">
                {/* 前一段是根（`/`）时它自己就是分隔符，再补一个会变成 `//home/...` */}
                {i > 0 && crumbs[i - 1].label !== "/" && (
                  <span className="text-neutral-600">/</span>
                )}
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
        )}
        <button
          className="nx-tree-caret"
          title={editing ? "取消编辑（Esc）" : "输入路径跳转"}
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
              // 单击目录直接展开/收起（省掉「先选中、再双击」那两步）；文件只是选中
              onClick={() => {
                setSelected(entry.path);
                if (isDir) toggle(entry.path);
              }}
              // 双击目录 = 「进去」：把浏览根切到它，不必从 / 一路展开下来
              onDoubleClick={() => (isDir ? enterDir(entry.path) : openFile(entry))}
              onContextMenu={(e) => openRowMenu(e, entry)}
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
            单击目录名可展开，双击可进入
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

      <ContextMenu state={menu} onClose={() => setMenu(null)} />
    </aside>
  );
}

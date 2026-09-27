// 终端标签页：xterm + 工具条（搜索 / 编码 / 录制 / 命令块）。
import { useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from "react";
import { XtermView, type TerminalHandle } from "./XtermView";
import { CommandBlockPanel } from "./CommandBlockPanel";
import type { CommandBlock } from "./commandBlocks";
import { sessionApi, fsApi, terminalApi } from "../../ipc/commands";
import { openTerminalTab, takePendingCommand, useUi } from "../../app/store";
import { pickLocalFile, pickSavePath, promptText } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import { baseName, looksLikePath, resolveRemotePath } from "../files/pathUtils";
import {
  IconArrowDown,
  IconArrowUp,
  IconBot,
  IconClose,
  IconCode,
  IconCommand,
  IconCopy,
  IconDownload,
  IconEdit,
  IconList,
  IconMergeH,
  IconPlay,
  IconPlug,
  IconPlus,
  IconRefresh,
  IconSave,
  IconSearch,
  IconSplitH,
  IconStop,
  IconUpload,
} from "../../ui/icons";

export interface TerminalPaneProps {
  sessionId: string;
  title: string;
  winrm?: boolean;
  /** 非空表示「容器内终端」，attach 走 docker exec。 */
  containerId?: string;
  /** store 里的标签 id：attach 成功后要把内核 tabId 写回该标签，关闭时才能回收 PTY。 */
  storeTabId: string;
  /** 所在标签是否激活（切标签不再卸载组件，靠它同步可见性）。 */
  visible?: boolean;
}

const ENCODINGS = ["utf-8", "gbk", "gb18030", "big5", "latin1"];

/** 命令块数量达到这个值就自动展开侧栏（3 条以上已经值得一眼看到）。 */
const AUTO_OPEN_BLOCKS = 3;

export function TerminalPane({
  sessionId,
  title,
  winrm,
  containerId,
  storeTabId,
  visible,
}: TerminalPaneProps) {
  const [kernelTabId, setKernelTabId] = useState<string | null>(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [recording, setRecording] = useState(false);
  const [encoding, setEncoding] = useState("utf-8");
  const [blocks, setBlocks] = useState<CommandBlock[]>([]);
  const [blocksOpen, setBlocksOpen] = useState(false);
  /** 终端选区右键菜单（目前只有「向 AI 提问」/「复制」）。 */
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  /** 用户是否手动关过侧栏——关过之后就不再自动弹，尊重用户选择。 */
  const userClosedBlocks = useRef(false);
  const searchApi = useRef<{
    findNext: (t: string) => void;
    findPrevious: (t: string) => void;
  } | null>(null);
  const handleRef = useRef<TerminalHandle | null>(null);
  const pushToast = useUi((s) => s.pushToast);
  const sessionStatus = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.status);
  const statusText =
    sessionStatus === "connected"
      ? "已连接"
      : sessionStatus === "reconnecting"
        ? "重连中"
        : sessionStatus === "connecting"
          ? "连接中"
          : sessionStatus === "failed"
            ? "连接失败"
            : "已断开";
  /** 「重新连接」只在会话确实掉线时才有意义（连着的时候重连只是白抖一下）。 */
  const canReconnect =
    sessionStatus === "disconnected" || sessionStatus === "failed" || sessionStatus === undefined;
  // winrm 是非交互行模式，没有「命令 + 输出」的块语义
  const blocksSupported = !winrm;

  // 命令块攒够 AUTO_OPEN_BLOCKS 条就自动展开（用户手动关过则不再打扰）
  useEffect(() => {
    if (!blocksSupported || userClosedBlocks.current) return;
    if (blocks.length >= AUTO_OPEN_BLOCKS) setBlocksOpen(true);
  }, [blocks.length, blocksSupported]);

  const toggleRecord = async () => {
    if (!kernelTabId) return;
    if (!recording) {
      const path = await pickSavePath(`${title}-${Date.now()}.log`);
      if (!path) return;
      await terminalApi.recordStart(kernelTabId, path);
      setRecording(true);
      pushToast("info", `开始录制 → ${path}`);
    } else {
      const bytes = await terminalApi.recordStop(kernelTabId);
      setRecording(false);
      pushToast("success", `录制完成（${bytes} 字节）`);
    }
  };

  /* ── 右键菜单 ──────────────────────────────────────────────────────── */

  /** 切换字符编码（工具条的 select 和右键子菜单共用一份逻辑）。 */
  const applyEncoding = async (v: string) => {
    setEncoding(v);
    if (!kernelTabId) return;
    await terminalApi.switchEncoding(kernelTabId, v).catch(() => undefined);
    pushToast("info", `编码已切换为 ${v}（重连后生效更佳）`);
  };

  /** 把剪贴板内容打进终端，等价于用户按了粘贴。 */
  const pasteIntoTerminal = async () => {
    if (!kernelTabId) return;
    try {
      const text = await navigator.clipboard.readText();
      if (text) await terminalApi.write(kernelTabId, new TextEncoder().encode(text));
    } catch (e) {
      pushToast("error", `读取剪贴板失败：${describeError(e)}`);
    }
  };

  /** 把整段回滚输出搬到剪贴板（走内核 dump，不受当前视口限制）。 */
  const copyAllOutput = async () => {
    if (!kernelTabId) return;
    try {
      const text = await terminalApi.dump(kernelTabId);
      await navigator.clipboard.writeText(text);
      pushToast("success", `已复制 ${text.length} 字符`);
    } catch (e) {
      pushToast("error", `复制失败：${describeError(e)}`);
    }
  };

  /** 复用当前连接再开一个终端标签。 */
  const newTabHere = () => {
    const session = useUi.getState().sessions.find((s) => s.id === sessionId);
    if (!session) {
      pushToast("error", "会话已不在，无法新开标签");
      return;
    }
    void openTerminalTab(session);
  };

  const disconnectSession = async () => {
    try {
      await sessionApi.disconnect(sessionId);
      pushToast("info", "已断开连接");
    } catch (e) {
      pushToast("error", `断开失败：${describeError(e)}`);
    }
  };

  /** 断开之后不用再关掉整个工作区重开 —— 直接走内核的重连。 */
  const reconnectSession = async () => {
    pushToast("info", "正在重连…");
    try {
      const ok = await sessionApi.reconnect(sessionId);
      pushToast(
        ok ? "success" : "info",
        ok ? "已重新连接" : "该会话不支持重连（本地快速会话，或会话已被断开）",
      );
    } catch (e) {
      pushToast("error", `重连失败：${describeError(e)}`);
    }
  };

  /** 把回滚输出写到本地文件（内核 dump → std::fs::write）。 */
  const saveLogToFile = async () => {
    if (!kernelTabId) return;
    const path = await pickSavePath(`${title}-${Date.now()}.log`.replace(/[\\/:*?"<>|]/g, "_"));
    if (!path) return;
    try {
      const bytes = await terminalApi.exportLog(kernelTabId, path);
      pushToast("success", `已保存 ${bytes} 字节 → ${path}`);
    } catch (e) {
      pushToast("error", `保存失败：${describeError(e)}`);
    }
  };

  /**
   * 「粘贴选中文本」：把终端里选中的那段写回终端的输入行。
   *
   * 和上面的「粘贴」差在**不碰剪贴板** —— 典型用法是在远端 `ls` 出几个路径、
   * 选中其中一个再喂给下一条命令，不希望这一来一回把剪贴板里的东西冲掉。
   */
  const pasteSelectionBack = async () => {
    if (!kernelTabId) return;
    const selected = handleRef.current?.getSelection() ?? "";
    if (!selected) return;
    try {
      await terminalApi.write(kernelTabId, new TextEncoder().encode(selected));
    } catch (e) {
      pushToast("error", `写入终端失败：${describeError(e)}`);
    }
  };

  /**
   * 「命令输入」：弹多行输入框，整条命令写进终端（自动补回车 = 直接执行）。
   *
   * 多行是刚需：一段 `for … do … done` 或者带续行符的 docker 命令，
   * 在单行框里根本没法写。所以显式 multiLine，不靠弹窗自己猜。
   */
  const inputCommand = async () => {
    if (!kernelTabId) return;
    const cmd = await promptText("要发送到终端的命令（可多行，回车换行）", "", {
      multiLine: true,
    });
    if (!cmd) return;
    const body = cmd.endsWith("\n") ? cmd : `${cmd}\n`;
    try {
      await terminalApi.write(kernelTabId, new TextEncoder().encode(body));
      pushToast("success", "命令已发送");
    } catch (e) {
      pushToast("error", `发送失败：${describeError(e)}`);
    }
  };

  /** 文件的落点要用终端的当前目录 —— 拿不到就返回 null，让路径保持原样。 */
  const remoteCwd = async (): Promise<string | null> => {
    try {
      return await sessionApi.cwd(sessionId);
    } catch {
      return null;
    }
  };

  /** SCP 上传：本地文件 → 终端当前目录。 */
  const scpUpload = async () => {
    const file = await pickLocalFile();
    if (!file) return;
    const remote = resolveRemotePath(
      await remoteCwd(),
      file.split(/[\\/]/).pop() ?? "upload.bin",
    );
    pushToast("info", `开始上传 → ${remote}…`);
    try {
      const bytes = await fsApi.upload(sessionId, file, remote, false);
      pushToast("success", `已上传 ${bytes} 字节 → ${remote}`);
    } catch (e) {
      pushToast("error", `上传失败：${describeError(e)}`);
    }
  };

  /**
   * SCP 下载：远程文件 → 本机。
   *
   * 远程路径允许手输，但如果终端里正好选中了一段"像路径"的文字（`ls` 出来的
   * 结果通常就是），就把它预填进去 —— 省掉一次手打，又不会替用户做决定。
   */
  const scpDownload = async () => {
    const selected = (handleRef.current?.getSelection() ?? "").trim();
    const remote = await promptText(
      "要下载的远程文件（相对路径按终端当前目录解析）",
      looksLikePath(selected) ? selected : "",
      // 路径就该是单行；不显式指定的话，预填的长路径会被弹窗自动判成多行
      { multiLine: false },
    );
    if (!remote) return;
    const abs = resolveRemotePath(await remoteCwd(), remote);
    const target = await pickSavePath(baseName(abs));
    if (!target) return;
    pushToast("info", `开始下载 ${abs}…`);
    try {
      const bytes = await fsApi.download(sessionId, abs, target);
      pushToast("success", `已下载 ${bytes} 字节 → ${target}`);
    } catch (e) {
      pushToast("error", `下载失败：${describeError(e)}`);
    }
  };

  /**
   * 终端右键菜单。
   *
   * 形态对齐 同类工具 的参考：分组小标 + 右侧快捷键 + 子菜单。
   * 无论有没有选区都弹 —— 菜单本身够厚，不怕空；只是"选区相关"的那几项
   * 在没选中时置灰而不隐藏，这样菜单结构是稳定的、可预期的。
   */
  const buildTerminalMenu = (): MenuItem[] => {
    const selected = (handleRef.current?.getSelection() ?? "").trim();
    const hasSel = selected.length > 0;
    const firstLine = selected.split("\n")[0];
    const st = useUi.getState();
    const ws = st.workspaces.find((w) =>
      w.panes.some((p) => p.tabs.some((t) => t.id === storeTabId)),
    );
    const isSplit = (ws?.panes.length ?? 1) > 1;

    return [
      { kind: "group", label: "操作" },
      {
        kind: "item",
        label: "向 AI 提问",
        icon: <IconBot size={13} />,
        hint: hasSel ? `${selected.length} 字` : undefined,
        disabled: !hasSel,
        onSelect: () => {
          const { setPendingAsk, setRightOpen } = useUi.getState();
          setPendingAsk(selected);
          // 侧栏收起时先展开：不然点了「提问」什么都没发生，像坏了
          setRightOpen(true);
        },
      },
      {
        kind: "item",
        label: "复制",
        icon: <IconCopy size={13} />,
        disabled: !hasSel,
        onSelect: () => void navigator.clipboard.writeText(selected).catch(() => undefined),
      },
      {
        kind: "item",
        label: "粘贴",
        disabled: !kernelTabId,
        onSelect: () => void pasteIntoTerminal(),
      },
      {
        kind: "item",
        label: "粘贴选中文本",
        icon: <IconEdit size={13} />,
        hint: "不动剪贴板",
        disabled: !hasSel || !kernelTabId,
        onSelect: () => void pasteSelectionBack(),
      },
      {
        kind: "item",
        label: "搜索选中内容",
        icon: <IconSearch size={13} />,
        accel: "Ctrl+F",
        disabled: !hasSel,
        onSelect: () => {
          setQuery(firstLine);
          setSearchOpen(true);
          // 搜索栏挂载后才注册好 addon 句柄，等一帧再搜
          window.setTimeout(() => searchApi.current?.findNext(firstLine), 60);
        },
      },
      {
        kind: "item",
        label: "命令输入",
        icon: <IconCommand size={13} />,
        hint: "多行，直接执行",
        disabled: !kernelTabId,
        onSelect: () => void inputCommand(),
      },
      { kind: "separator" },
      { kind: "group", label: "终端" },
      {
        kind: "item",
        label: "复用连接打开新标签",
        icon: <IconPlus size={13} />,
        onSelect: newTabHere,
      },
      {
        kind: "item",
        label: "清屏",
        disabled: !kernelTabId,
        onSelect: () => handleRef.current?.clear(),
      },
      {
        kind: "item",
        label: `编码 (${encoding.toUpperCase()})`,
        icon: <IconCode size={13} />,
        submenu: ENCODINGS.map((enc) => ({
          kind: "item" as const,
          label: enc,
          hint: enc === encoding ? "当前" : undefined,
          onSelect: () => void applyEncoding(enc),
        })),
      },
      {
        kind: "item",
        label: "复制全部输出",
        icon: <IconCopy size={13} />,
        disabled: !kernelTabId,
        onSelect: () => void copyAllOutput(),
      },
      {
        kind: "item",
        label: "保存为日志",
        icon: <IconSave size={13} />,
        hint: "当前回滚缓冲",
        disabled: !kernelTabId,
        onSelect: () => void saveLogToFile(),
      },
      {
        kind: "item",
        label: "重新连接",
        icon: <IconRefresh size={13} />,
        hint: canReconnect ? "已断开" : statusText,
        // 已经连着的时候重连只会白抖一下，没有意义
        disabled: !canReconnect,
        onSelect: () => void reconnectSession(),
      },
      {
        kind: "item",
        label: "断开连接",
        icon: <IconPlug size={13} />,
        danger: true,
        onSelect: () => void disconnectSession(),
      },
      { kind: "separator" },
      { kind: "group", label: "文件传输 (SCP)" },
      {
        kind: "item",
        label: "上传本地文件",
        icon: <IconUpload size={13} />,
        hint: "→ 当前目录",
        onSelect: () => void scpUpload(),
      },
      {
        kind: "item",
        label: "下载远程文件",
        icon: <IconDownload size={13} />,
        hint: "→ 本机",
        onSelect: () => void scpDownload(),
      },
      { kind: "separator" },
      { kind: "group", label: "分屏" },
      {
        kind: "item",
        label: isSplit ? "取消分屏" : "上下分屏",
        icon: isSplit ? <IconMergeH size={13} /> : <IconSplitH size={13} />,
        accel: "Ctrl+\\",
        onSelect: () => {
          if (!ws) return;
          const cur = useUi.getState();
          if (isSplit) void cur.unsplitWorkspace(ws.panes[1].id, ws.id);
          else cur.splitWorkspace(ws.id);
        },
      },
      { kind: "separator" },
      { kind: "group", label: "会话记录" },
      {
        kind: "item",
        label: recording ? "停止录制" : "开始录制到文件",
        icon: recording ? <IconStop size={13} /> : <IconPlay size={13} />,
        disabled: !kernelTabId,
        onSelect: () => void toggleRecord(),
      },
    ];
  };

  const openTerminalMenu = (e: ReactMouseEvent<HTMLDivElement>) => {
    e.preventDefault();
    const selected = (handleRef.current?.getSelection() ?? "").trim();
    setMenu({
      x: e.clientX,
      y: e.clientY,
      // 有选区就把它顶在菜单头上，让用户确认自己要操作的是哪一段
      title: selected ? selected.split("\n")[0].slice(0, 80) : title,
      items: buildTerminalMenu(),
    });
  };

  return (
    <div className="nx-pane bg-term">
      <div className="nx-toolbar">
        <span className="nx-toolbar-title">{title}</span>
        <span
          className={`nx-badge ${
            sessionStatus === "connected"
              ? "nx-badge-green"
              : sessionStatus === "failed"
                ? "nx-badge-red"
                : ""
          }`}
        >
          <span className="nx-dot" />
          {statusText}
        </span>
        {winrm && <span className="nx-badge nx-badge-amber">非交互模式</span>}
        {containerId && <span className="nx-badge nx-badge-purple">容器内 exec</span>}
        <div className="nx-spacer" />

        <button
          className={`nx-btn nx-btn-ghost nx-btn-sm ${searchOpen ? "bg-neutral-800 text-neutral-100" : ""}`}
          title="搜索终端内容 (Ctrl+F)"
          onClick={() => setSearchOpen((v) => !v)}
        >
          <IconSearch size={13} />
          搜索
        </button>

        <select
          className="nx-select nx-input-sm w-[88px] font-mono"
          value={encoding}
          title="终端字符编码（右键菜单里也有）"
          onChange={(e) => void applyEncoding(e.target.value)}
        >
          {ENCODINGS.map((enc) => (
            <option key={enc} value={enc}>
              {enc}
            </option>
          ))}
        </select>

        <button
          className={`nx-btn nx-btn-sm ${recording ? "nx-btn-danger" : "nx-btn-ghost"}`}
          title="把终端输出录制到文件"
          onClick={() => void toggleRecord()}
        >
          {recording ? <IconStop size={12} /> : <span className="nx-dot bg-current" />}
          {recording ? "停止" : "录制"}
        </button>

        {blocksSupported && (
          <>
            <span className="nx-divider-v" />
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              disabled={!blocks.length}
              title="定位到上一条命令"
              onClick={() => handleRef.current?.navigateBlock("prev")}
            >
              <IconArrowUp size={13} />
            </button>
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              disabled={!blocks.length}
              title="定位到下一条命令"
              onClick={() => handleRef.current?.navigateBlock("next")}
            >
              <IconArrowDown size={13} />
            </button>
            <button
              className={`nx-btn nx-btn-sm ${blocksOpen ? "nx-btn-ghost bg-neutral-800 text-neutral-100" : "nx-btn-ghost"}`}
              title="命令块：折叠输出 / 复制 / 定位"
              onClick={() => {
                const next = !blocksOpen;
                setBlocksOpen(next);
                if (!next) userClosedBlocks.current = true;
                else userClosedBlocks.current = false;
              }}
            >
              <IconList size={13} />
              命令块
              {blocks.length > 0 && <span className="nx-count">{blocks.length}</span>}
            </button>
          </>
        )}
      </div>

      {searchOpen && (
        <div className="flex shrink-0 items-center gap-1.5 border-b border-neutral-800/60 bg-neutral-900/70 px-2.5 py-1.5">
          <div className="nx-field max-w-[260px]">
            <span className="nx-field-icon">
              <IconSearch size={12} />
            </span>
            <input
              autoFocus
              className="nx-input nx-input-sm"
              placeholder="搜索终端内容…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && e.shiftKey) searchApi.current?.findPrevious(query);
                else if (e.key === "Enter") searchApi.current?.findNext(query);
                if (e.key === "Escape") setSearchOpen(false);
              }}
            />
          </div>
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title="上一个"
            onClick={() => searchApi.current?.findPrevious(query)}
          >
            <IconArrowUp size={13} />
          </button>
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title="下一个"
            onClick={() => searchApi.current?.findNext(query)}
          >
            <IconArrowDown size={13} />
          </button>
          <div className="nx-spacer" />
          <button className="nx-icon-btn nx-icon-btn-sm" title="关闭搜索" onClick={() => setSearchOpen(false)}>
            <IconClose size={12} />
          </button>
        </div>
      )}

      <div className="flex min-h-0 flex-1">
        {/* 终端内容区接右键；菜单本身渲染在组件末尾（fixed 定位，不占布局） */}
        <div className="min-h-0 min-w-0 flex-1 p-1.5" onContextMenu={openTerminalMenu}>
          <XtermView
            sessionId={sessionId}
            tabId={kernelTabId ?? "pending"}
            winrm={winrm}
            containerId={containerId}
            visible={visible}
            onAttach={(id) => {
              setKernelTabId(id);
              // 必须写回 store：store.closeTab 只有拿到内核 tabId 才会调
              // terminal_close_tab 回收 PTY，否则关标签会泄漏远端 shell。
              useUi.getState().updateTab(storeTabId, { tabId: id });
              // 「在终端打开」要求终端一落地就 cd 过去。必须等到内核 tabId
              // 出来才能发 —— 这正是它挂在 pendingCommand 上、而不是开标签时
              // 就写出去的原因。取出即清空，避免 StrictMode 双跑发两次。
              const pending = takePendingCommand(storeTabId);
              if (pending) {
                void terminalApi
                  .write(id, new TextEncoder().encode(`${pending}\n`))
                  .catch(() => undefined);
              }
            }}
            onBlocks={blocksSupported ? setBlocks : undefined}
            onHandle={(h) => {
              handleRef.current = h;
            }}
            registerSearch={(api) => {
              searchApi.current = api;
            }}
          />
        </div>
        {blocksSupported && blocksOpen && (
          <CommandBlockPanel
            blocks={blocks}
            getText={(i) => handleRef.current?.copyBlock(i) ?? ""}
            onLocate={(i) => handleRef.current?.scrollToBlock(i)}
            onClear={() => handleRef.current?.clearBlocks()}
            onToast={pushToast}
          />
        )}
      </div>

      <ContextMenu state={menu} onClose={() => setMenu(null)} />
    </div>
  );
}

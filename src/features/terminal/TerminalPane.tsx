// 终端标签页：xterm + 工具条（搜索 / 编码 / 录制 / 命令块）。
import { useEffect, useRef, useState } from "react";
import { XtermView, type TerminalHandle } from "./XtermView";
import { CommandBlockPanel } from "./CommandBlockPanel";
import type { CommandBlock } from "./commandBlocks";
import { terminalApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { pickSavePath } from "../../ui/dialogs";
import {
  IconArrowDown,
  IconArrowUp,
  IconClose,
  IconList,
  IconSearch,
  IconStop,
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
  /** 用户是否手动关过侧栏——关过之后就不再自动弹，尊重用户选择。 */
  const userClosedBlocks = useRef(false);
  const searchApi = useRef<{
    findNext: (t: string) => void;
    findPrevious: (t: string) => void;
  } | null>(null);
  const handleRef = useRef<TerminalHandle | null>(null);
  const pushToast = useUi((s) => s.pushToast);
  const sessionStatus = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.status);
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
          title="终端字符编码"
          onChange={async (e) => {
            const v = e.target.value;
            setEncoding(v);
            if (kernelTabId) {
              await terminalApi.switchEncoding(kernelTabId, v).catch(() => undefined);
              pushToast("info", `编码已切换为 ${v}（重连后生效更佳）`);
            }
          }}
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
        <div className="min-h-0 min-w-0 flex-1 p-1.5">
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
    </div>
  );
}

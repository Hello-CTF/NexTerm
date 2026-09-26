// 终端标签页：xterm + 工具条（搜索/编码/录制/命令块 v1）。
import { useRef, useState } from "react";
import { XtermView, type TerminalHandle } from "./XtermView";
import { CommandBlockPanel } from "./CommandBlockPanel";
import type { CommandBlock } from "./commandBlocks";
import { terminalApi } from "../../ipc/commands";
import { useUi } from "../../app/store";

export interface TerminalPaneProps {
  sessionId: string;
  title: string;
  winrm?: boolean;
  /** store 里的标签 id：attach 成功后要把内核 tabId 写回该标签，关闭时才能回收 PTY。 */
  storeTabId: string;
  /** 所在标签是否激活（切标签不再卸载组件，靠它同步可见性）。 */
  visible?: boolean;
}

const ENCODINGS = ["utf-8", "gbk", "gb18030", "big5", "latin1"];

export function TerminalPane({ sessionId, title, winrm, storeTabId, visible }: TerminalPaneProps) {
  const [kernelTabId, setKernelTabId] = useState<string | null>(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [recording, setRecording] = useState(false);
  const [blocks, setBlocks] = useState<CommandBlock[]>([]);
  const [blocksOpen, setBlocksOpen] = useState(false);
  const searchApi = useRef<{ findNext: (t: string) => void; findPrevious: (t: string) => void } | null>(null);
  const handleRef = useRef<TerminalHandle | null>(null);
  const pushToast = useUi((s) => s.pushToast);
  // winrm 是非交互行模式，没有「命令 + 输出」的块语义
  const blocksSupported = !winrm;

  const toggleRecord = async () => {
    if (!kernelTabId) return;
    if (!recording) {
      const path = await import("@tauri-apps/plugin-dialog").then((d) =>
        d.save({ title: "保存终端录制", defaultPath: `${title}-${Date.now()}.log` }),
      );
      if (!path) return;
      await terminalApi.recordStart(kernelTabId, path);
      setRecording(true);
      pushToast("info", "开始录制");
    } else {
      const bytes = await terminalApi.recordStop(kernelTabId);
      setRecording(false);
      pushToast("success", `录制完成（${bytes} 字节）`);
    }
  };

  return (
    <div className="flex h-full flex-col bg-[#12141a]">
      <div className="flex items-center gap-1 border-b border-neutral-800 bg-neutral-900/60 px-2 py-1 text-xs text-neutral-400">
        <span className="mr-1 truncate font-medium text-neutral-300">{title}</span>
        {winrm && (
          <span className="rounded bg-amber-500/20 px-1.5 py-0.5 text-[10px] text-amber-300">
            非交互模式
          </span>
        )}
        <div className="flex-1" />
        <button
          className="rounded px-1.5 py-0.5 hover:bg-neutral-800"
          onClick={() => setSearchOpen((v) => !v)}
          title="搜索 (Ctrl+F)"
        >
          搜索
        </button>
        <select
          className="rounded bg-neutral-800 px-1 py-0.5 outline-none"
          defaultValue="utf-8"
          title="终端编码"
          onChange={async (e) => {
            if (kernelTabId) {
              await terminalApi.switchEncoding(kernelTabId, e.target.value);
              pushToast("info", `编码已切换为 ${e.target.value}（重连后生效更佳）`);
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
          className={`rounded px-1.5 py-0.5 hover:bg-neutral-800 ${recording ? "bg-red-500/20 text-red-300" : ""}`}
          onClick={() => void toggleRecord()}
          title="终端录制"
        >
          {recording ? "停止录制" : "录制"}
        </button>
        {blocksSupported && (
          <>
            <span className="mx-1 h-3 w-px bg-neutral-700" />
            <button
              className="rounded px-1.5 py-0.5 hover:bg-neutral-800 disabled:opacity-40"
              disabled={!blocks.length}
              title="上一条命令"
              onClick={() => handleRef.current?.navigateBlock("prev")}
            >
              ↑
            </button>
            <button
              className="rounded px-1.5 py-0.5 hover:bg-neutral-800 disabled:opacity-40"
              disabled={!blocks.length}
              title="下一条命令"
              onClick={() => handleRef.current?.navigateBlock("next")}
            >
              ↓
            </button>
            <button
              className={`rounded px-1.5 py-0.5 hover:bg-neutral-800 ${blocksOpen ? "bg-neutral-800 text-neutral-200" : ""}`}
              onClick={() => setBlocksOpen((v) => !v)}
              title="命令块：折叠输出 / 复制 / 定位（M1-T9）"
            >
              命令块 {blocks.length > 0 ? blocks.length : ""}
            </button>
          </>
        )}
      </div>
      {searchOpen && (
        <div className="flex items-center gap-1 border-b border-neutral-800 bg-neutral-900 px-2 py-1 text-xs">
          <input
            autoFocus
            className="w-48 rounded bg-neutral-800 px-2 py-0.5 text-neutral-200 outline-none focus:ring-1 focus:ring-blue-500"
            placeholder="搜索终端内容…"
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") searchApi.current?.findNext(query);
              if (e.key === "Enter" && e.shiftKey) searchApi.current?.findPrevious(query);
              if (e.key === "Escape") setSearchOpen(false);
            }}
          />
          <button className="rounded px-1.5 hover:bg-neutral-800" onClick={() => searchApi.current?.findPrevious(query)}>
            ↑
          </button>
          <button className="rounded px-1.5 hover:bg-neutral-800" onClick={() => searchApi.current?.findNext(query)}>
            ↓
          </button>
        </div>
      )}
      <div className="flex min-h-0 flex-1">
        <div className="min-h-0 min-w-0 flex-1 p-1">
          <XtermView
            sessionId={sessionId}
            tabId={kernelTabId ?? "pending"}
            winrm={winrm}
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

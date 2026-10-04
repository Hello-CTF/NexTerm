import { useCallback, useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from "react";
import { XtermView, type TerminalHandle } from "./XtermView";
import { CommandBlockPanel } from "./CommandBlockPanel";
import { TerminalKeysBar } from "./TerminalKeysBar";
import { resolveWinrmMode } from "./terminalPolicy";
import { splitAllowedForHeight } from "./workspaceLayout";
import type { CommandBlock } from "./commandBlocks";
import { sessionApi, terminalApi } from "../../ipc/commands";
import { listenEvent, EVENTS, EventVersionGate, type TerminalControlEvent } from "../../ipc/events";
import { clientId } from "../../ipc/env";
import { takePendingCommand, sessionStatusText, useUi } from "../../app/store";
import { isMac, modHint } from "../../app/platform";
import { disconnectSessionWithConfirm } from "./sessionDisconnect";
import { describeTarget, finishSave, pickSavePath, promptText } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import {
  IconArrowDown,
  IconArrowUp,
  IconClose,
  IconCommand,
  IconCopy,
  IconEdit,
  IconEye,
  IconGamepad,
  IconList,
  IconMergeH,
  IconPlug,
  IconRefresh,
  IconSave,
  IconSearch,
  IconSplitH,
  IconStop,
} from "../../ui/icons";

export interface TerminalPaneProps {
  sessionId: string;
  title: string;
  winrm?: boolean;
  containerId?: string;
  storeTabId: string;
  resumeTabId?: string;
  visible?: boolean;
}

const ENCODINGS = ["utf-8", "gbk", "gb18030", "big5", "latin1"];

const AUTO_OPEN_BLOCKS = 3;

function isStoreTabDead(id: string): boolean {
  return useUi
    .getState()
    .workspaces.some((w) => w.panes.some((p) => p.tabs.some((t) => t.id === id && t.dead === true)));
}

export function TerminalPane({
  sessionId,
  title,
  winrm,
  containerId,
  storeTabId,
  resumeTabId,
  visible,
}: TerminalPaneProps) {
  const [kernelTabId, setKernelTabId] = useState<string | null>(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [recording, setRecording] = useState(false);
  const [encoding, setEncoding] = useState("utf-8");
  const [blocks, setBlocks] = useState<CommandBlock[]>([]);
  const [blocksOpen, setBlocksOpen] = useState(false);
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const [control, setControl] = useState<{
    controller: string | null;
    subscribers: number;
    viewers: number;
    exited: boolean;
  } | null>(null);
  const [remoteGrid, setRemoteGrid] = useState<{
    cols: number;
    rows: number;
    revision: number;
  } | null>(null);
  const [claiming, setClaiming] = useState(false);
  const [attachDead, setAttachDead] = useState(false);
  const [reconnecting, setReconnecting] = useState(false);
  const [epoch, setEpoch] = useState(0);
  const me = clientId();
  const resumeRef = useRef(resumeTabId);
  const observerHintAt = useRef(0);
  const writeErrorAt = useRef(0);
  const controlRef = useRef(control);
  controlRef.current = control;
  const kernelTabIdRef = useRef<string | null>(null);
  kernelTabIdRef.current = kernelTabId;
  const userClosedBlocks = useRef(false);
  const recordTarget = useRef<{ path: string; name: string } | null>(null);
  const searchApi = useRef<{
    findNext: (t: string) => void;
    findPrevious: (t: string) => void;
  } | null>(null);
  const handleRef = useRef<TerminalHandle | null>(null);
  const paneRef = useRef<HTMLDivElement>(null);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const pushToast = useUi((s) => s.pushToast);
  const sessionKind = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.kind);
  const sessionStatus = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.status);
  const sessionName = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.name) ?? title;
  const effectiveWinrm = resolveWinrmMode(winrm, sessionKind);
  const statusText = sessionStatusText(sessionStatus);
  const canReconnect =
    sessionStatus === "disconnected" || sessionStatus === "failed" || sessionStatus === undefined;
  const blocksSupported = !effectiveWinrm;
  const mod = modHint();

  useEffect(() => {
    if (!blocksSupported || userClosedBlocks.current) return;
    if (blocks.length >= AUTO_OPEN_BLOCKS) setBlocksOpen(true);
  }, [blocks.length, blocksSupported]);

  const controlSupported = !containerId && !effectiveWinrm;
  const isObserver = controlSupported && control !== null && control.controller !== me;
  const canResize = !isObserver;

  const refreshControl = useCallback(
    async (tabIdArg?: string) => {
      const tabId = tabIdArg ?? kernelTabId;
      if (!tabId) return;
      try {
        const list = await terminalApi.listLive();
        const hit = list.find((t) => t.tabId === tabId);
        if (hit) {
          setControl({
            controller: hit.controller,
            subscribers: hit.subscribers,
            viewers: hit.viewers,
            exited: hit.exited,
          });
        } else {
          setControl((c) => (c ? { ...c, exited: true } : c));
        }
      } catch {
      }
    },
    [kernelTabId],
  );

  const sendData = useCallback(
    async (text: string) => {
      if (!kernelTabId) return;
      if (isObserver) {
        const now = Date.now();
        if (now - observerHintAt.current > 4000) {
          observerHintAt.current = now;
          pushToast("info", "终端正在其他设备上操作中，点「接管控制」可接手");
        }
        return;
      }
      try {
        await terminalApi.write(kernelTabId, new TextEncoder().encode(text));
      } catch (e) {
        const code = (e as { code?: string } | null)?.code;
        if (code === "not_controller") {
          if (controlRef.current?.controller === me) {
            setControl((c) => (c ? { ...c, controller: "__other__" } : c));
            void refreshControl();
          }
          const now = Date.now();
          if (now - observerHintAt.current > 4000) {
            observerHintAt.current = now;
            pushToast("info", "终端正在其他设备上操作中，点「接管控制」可接手");
          }
          return;
        }
        const now = Date.now();
        if (now - writeErrorAt.current > 5000) {
          writeErrorAt.current = now;
          pushToast("error", `写入终端失败：${describeError(e)}`);
        }
      }
    },
    [kernelTabId, isObserver, me, pushToast, refreshControl],
  );

  const takeControl = useCallback(async () => {
    if (!kernelTabId || claiming) return;
    setClaiming(true);
    try {
      const prev = await terminalApi.claim(kernelTabId);
      setControl((c) =>
        c ? { ...c, controller: me } : { controller: me, subscribers: 1, viewers: 1, exited: false },
      );
      handleRef.current?.fit();
      pushToast("success", prev && prev !== me ? "已接管控制权（对方转为只读观看）" : "已取得控制权");
    } catch (e) {
      pushToast("error", `接管失败：${describeError(e)}`);
    } finally {
      setClaiming(false);
    }
  }, [kernelTabId, claiming, me, pushToast]);

  const controlVersions = useRef(new EventVersionGate());
  useEffect(() => {
    if (controlSupported && control?.exited) {
      useUi.getState().updateTab(storeTabId, { exited: true });
    }
  }, [controlSupported, control?.exited, storeTabId]);
  useEffect(() => {
    let unlisten: (() => void) | null = null;
    let cancelled = false;
    void listenEvent<TerminalControlEvent>(EVENTS.terminalControl, (p) => {
      if (p.tabId !== kernelTabIdRef.current) return;
      if (!controlVersions.current.accept(p.tabId, p.version)) return;
      setControl({
        controller: p.controller,
        subscribers: p.subscribers,
        viewers: p.viewers,
        exited: p.exited,
      });
      if (p.gridRevision > 0) {
        setRemoteGrid({ cols: p.cols, rows: p.rows, revision: p.gridRevision });
      }
    }).then((off) => {
      if (cancelled) off();
      else unlisten = off;
    });
    return () => {
      cancelled = true;
      unlisten?.();
    };
  }, []);

  useEffect(() => {
    const pane = paneRef.current;
    if (!pane) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.repeat || isImeKeyEvent(e)) return;
      const mac = isMac();
      const primary = mac ? e.metaKey : e.ctrlKey;
      const secondary = mac ? e.ctrlKey : e.metaKey;
      if (!primary || secondary || e.shiftKey || e.altKey || e.key.toLowerCase() !== "f") return;
      e.preventDefault();
      e.stopPropagation();
      const selected = (handleRef.current?.getSelection() ?? "").trim();
      if (selected) {
        const first = selected.split("\n")[0];
        setQuery(first);
        setSearchOpen(true);
        window.setTimeout(() => searchApi.current?.findNext(first), 60);
      } else {
        setSearchOpen(true);
      }
      window.setTimeout(() => searchInputRef.current?.focus(), 0);
    };
    pane.addEventListener("keydown", onKey, true);
    return () => pane.removeEventListener("keydown", onKey, true);
  }, []);

  const handleAttachDead = useCallback(
    (code: string) => {
      if (code !== "not_found") return;
      useUi.getState().updateTab(storeTabId, { tabId: undefined, dead: true });
      setKernelTabId(null);
      setAttachDead(true);
    },
    [storeTabId],
  );

  const reconnectNow = useCallback(async () => {
    const st = useUi.getState();
    const ws = st.workspaces.find((w) =>
      w.panes.some((p) => p.tabs.some((t) => t.id === storeTabId)),
    );
    const assetId = ws?.assetId;
    if (!assetId) {
      pushToast("error", "找不到这台主机的资产信息，请到左侧资产树里手动连接");
      return;
    }
    setReconnecting(true);
    try {
      const s = await sessionApi.connect(assetId);
      const list = useUi.getState().sessions;
      useUi.getState().setSessions([...list.filter((x) => x.id !== s.id), s]);
      useUi.getState().updateTab(storeTabId, { sessionId: s.id, tabId: undefined, dead: false, exited: false });
      resumeRef.current = undefined;
      setControl(null);
      setRemoteGrid(null);
      setAttachDead(false);
      setKernelTabId(null);
      setEpoch((n) => n + 1);
      pushToast("info", "已重新连接，正在打开一个新的终端");
    } catch (e) {
      pushToast("error", `重新连接失败：${describeError(e)}`);
    } finally {
      setReconnecting(false);
    }
  }, [storeTabId, pushToast]);

  const toggleRecord = async () => {
    if (!kernelTabId) return;
    if (!recording) {
      const name = `${title}-${Date.now()}.log`;
      try {
        const path = await pickSavePath(name);
        if (!path) return;
        await terminalApi.recordStart(kernelTabId, path);
        recordTarget.current = { path, name };
        setRecording(true);
        pushToast("info", `开始录制 → ${describeTarget(path, name)}`);
      } catch (e) {
        pushToast("error", `开始录制失败：${describeError(e)}`);
      }
    } else {
      const target = recordTarget.current;
      recordTarget.current = null;
      let bytes: number;
      try {
        bytes = await terminalApi.recordStop(kernelTabId);
      } catch (e) {
        setRecording(false);
        pushToast("error", `停止录制失败：${describeError(e)}`);
        return;
      }
      setRecording(false);
      try {
        const where = target ? await finishSave(target.path, target.name) : null;
        pushToast("success", `录制完成（${bytes} 字节）${where ? ` → ${where}` : ""}`);
      } catch (e) {
        pushToast("error", `录制已停止（${bytes} 字节），但保存到本地失败：${describeError(e)}`);
      }
    }
  };

  const applyEncoding = async (v: string) => {
    if (!kernelTabId) {
      pushToast("error", "终端尚未连接，无法切换编码");
      return;
    }
    try {
      await terminalApi.switchEncoding(kernelTabId, v);
      setEncoding(v);
      pushToast("info", `编码已切换为 ${v}（对之后的输出生效，已显示的内容不变）`);
    } catch (e) {
      pushToast("error", `编码切换失败：${describeError(e)}`);
    }
  };

  const pasteIntoTerminal = async () => {
    if (!kernelTabId) return;
    try {
      const text = await navigator.clipboard.readText();
      if (text) handleRef.current?.paste(text);
    } catch (e) {
      pushToast("error", `读取剪贴板失败：${describeError(e)}`);
    }
  };

  const disconnectSession = async () => {
    await disconnectSessionWithConfirm(sessionId, sessionName);
  };

  const reconnectSession = async () => {
    try {
      const started = await sessionApi.reconnect(sessionId);
      pushToast(
        started ? "info" : "error",
        started ? "正在重连…结果会显示在终端状态上" : "重连未能启动",
      );
    } catch (e) {
      pushToast("error", `重连失败：${describeError(e)}`);
    }
  };

  const saveLogToFile = async () => {
    if (!kernelTabId) return;
    const name = `${title}-${Date.now()}.log`.replace(/[\\/:*?"<>|]/g, "_");
    const path = await pickSavePath(name);
    if (!path) return;
    try {
      const bytes = await terminalApi.exportLog(kernelTabId, path);
      const where = await finishSave(path, name);
      pushToast(
        where ? "success" : "info",
        where ? `已保存 ${bytes} 字节 → ${where}` : "已取消保存",
      );
    } catch (e) {
      pushToast("error", `保存失败：${describeError(e)}`);
    }
  };

  const pasteSelectionBack = async () => {
    if (!kernelTabId) return;
    const selected = handleRef.current?.getSelection() ?? "";
    if (!selected) return;
    handleRef.current?.paste(selected);
  };

  const inputCommand = async () => {
    if (!kernelTabId) return;
    const cmd = await promptText("要发送到终端的命令（可多行，回车换行）", "", {
      multiLine: true,
    });
    if (!cmd) return;
    const body = cmd.endsWith("\n") ? cmd : `${cmd}\n`;
    try {
      await sendData(body);
      pushToast("success", "命令已发送");
    } catch (e) {
      pushToast("error", `发送失败：${describeError(e)}`);
    }
  };

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
        accel: `${mod}+F`,
        disabled: !hasSel,
        onSelect: () => {
          setQuery(firstLine);
          setSearchOpen(true);
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
        label: "清屏",
        disabled: !kernelTabId,
        onSelect: () => handleRef.current?.clear(),
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
      { kind: "group", label: "分屏" },
      {
        kind: "item",
        label: isSplit ? "取消分屏" : "上下分屏",
        icon: isSplit ? <IconMergeH size={13} /> : <IconSplitH size={13} />,
        accel: `${modHint()}+\\`,
        disabled: !isSplit && !splitAllowedForHeight(window.innerHeight),
        hint:
          !isSplit && !splitAllowedForHeight(window.innerHeight)
            ? "窗口高度不足"
            : undefined,
        onSelect: () => {
          if (!ws) return;
          const cur = useUi.getState();
          if (isSplit) void cur.unsplitWorkspace(ws.panes[1].id, ws.id);
          else if (splitAllowedForHeight(window.innerHeight)) cur.splitWorkspace(ws.id);
        },
      },
    ];
  };

  const openTerminalMenu = (e: ReactMouseEvent<HTMLDivElement>) => {
    e.preventDefault();
    const selected = (handleRef.current?.getSelection() ?? "").trim();
    setMenu({
      x: e.clientX,
      y: e.clientY,
      title: selected ? selected.split("\n")[0].slice(0, 80) : title,
      items: buildTerminalMenu(),
    });
  };

  return (
    <div ref={paneRef} className="nx-pane bg-term">
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
        {effectiveWinrm && <span className="nx-badge nx-badge-amber">非交互模式</span>}
        {containerId && <span className="nx-badge nx-badge-purple">容器内 exec</span>}
        <div className="nx-spacer" />

        <button
          className={`nx-btn nx-btn-ghost nx-btn-sm ${searchOpen ? "bg-neutral-800 text-neutral-100" : ""}`}
          title={`搜索终端内容 (${mod}+F)`}
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
              ref={searchInputRef}
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

      <div className="nx-terminal-body flex min-h-0 flex-1">
        <div className="relative min-h-0 min-w-0 flex-1" onContextMenu={openTerminalMenu}>
          <div className="h-full w-full p-1.5">
            <XtermView
              key={epoch}
              sessionId={sessionId}
              tabId={kernelTabId ?? "pending"}
              winrm={effectiveWinrm}
              containerId={containerId}
              resumeTabId={resumeRef.current}
              canResize={canResize}
              remoteGrid={remoteGrid ?? undefined}
              visible={visible}
              onData={(d) => void sendData(d)}
              onAttachFailed={handleAttachDead}
              onAttachInfo={(info) =>
                setControl({
                  controller: info.controller,
                  subscribers: info.subscribers,
                  viewers: info.viewers,
                  exited: info.exited,
                })
              }
              onAttach={(id) => {
                setKernelTabId(id);
                setAttachDead(false);
                useUi.getState().updateTab(storeTabId, { tabId: id, exited: false });
                if (isStoreTabDead(storeTabId)) {
                  useUi.getState().updateTab(storeTabId, { dead: false });
                }
                const pending = takePendingCommand(storeTabId);
                if (pending) {
                  void terminalApi
                    .write(id, new TextEncoder().encode(`${pending}\n`))
                    .catch(() => undefined);
                }
                void refreshControl(id);
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

          {controlSupported && control && (control.exited || isObserver) && (
            <div className="pointer-events-none absolute inset-0 z-10 flex flex-col items-center justify-center gap-2.5 bg-neutral-950/70 px-6 text-center backdrop-blur-[1px]">
              <span className="flex items-center gap-1.5 text-[13px] font-semibold text-neutral-100">
                <IconEye size={15} className="text-amber-300" />
                {control.exited
                  ? "终端进程已结束"
                  : control.controller
                    ? "终端正在其他设备上操作中"
                    : "当前无人操作"}
              </span>
              {control.exited ? (
                <span className="max-w-[440px] text-[11.5px] leading-relaxed text-neutral-400">
                  进程已经退出，这里只能查看它最后的内容。要继续操作请新建一个终端。
                </span>
              ) : (
                <>
                  <span className="max-w-[440px] text-[11.5px] leading-relaxed text-neutral-400">
                    多个设备可以同时观看，但同一时刻只有一个设备能操作。接管后，对方将转为只读观看，
                    终端尺寸也会按你的窗口重排。
                  </span>
                  {control.subscribers > 1 && (
                    <span className="nx-badge">{control.viewers} 个设备正在观看</span>
                  )}
                  <button
                    className="nx-btn nx-btn-primary nx-btn-sm pointer-events-auto"
                    disabled={claiming}
                    onClick={() => void takeControl()}
                  >
                    {claiming ? (
                      <IconRefresh size={13} className="animate-spin" />
                    ) : (
                      <IconGamepad size={13} />
                    )}
                    接管控制
                  </button>
                </>
              )}
            </div>
          )}

          {attachDead && (
            <div className="absolute inset-0 z-20 flex flex-col items-center justify-center gap-2.5 bg-neutral-950/80 px-6 text-center backdrop-blur-[1px]">
              <span className="flex items-center gap-1.5 text-[13px] font-semibold text-neutral-100">
                <IconRefresh size={15} className="text-amber-300" />
                这个终端已失效
              </span>
              <span className="max-w-[440px] text-[11.5px] leading-relaxed text-neutral-400">
                它所属的连接在服务端已经不在了（服务端重启，或连接已被回收）。
              </span>
              <div className="flex items-center gap-2">
                <button
                  className="nx-btn nx-btn-primary nx-btn-sm"
                  disabled={reconnecting}
                  onClick={() => void reconnectNow()}
                >
                  {reconnecting ? (
                    <IconRefresh size={13} className="animate-spin" />
                  ) : (
                    <IconRefresh size={13} />
                  )}
                  重新连接这台主机
                </button>
                <button
                  className="nx-btn nx-btn-ghost nx-btn-sm"
                  onClick={() => void useUi.getState().closeTab(storeTabId)}
                >
                  <IconClose size={13} />
                  关闭标签
                </button>
              </div>
            </div>
          )}
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

      <TerminalKeysBar
        onSend={(data) => void sendData(data)}
        onFocus={() => handleRef.current?.focus()}
      />
      <ContextMenu state={menu} onClose={() => setMenu(null)} />
    </div>
  );
}

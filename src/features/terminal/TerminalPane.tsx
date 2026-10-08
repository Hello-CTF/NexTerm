import { useCallback, useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from "react";
import { XtermView, type TerminalHandle } from "./XtermView";
import { CommandBlockPanel } from "./CommandBlockPanel";
import { TerminalKeysBar } from "./TerminalKeysBar";
import { Cwd } from "./Cwd";
import { Daemon } from "./Daemon";
import { resolveWinrmMode } from "./terminalPolicy";
import { splitAllowedForHeight } from "./workspaceLayout";
import type { CommandBlock } from "./commandBlocks";
import { sessionApi, terminalApi, filesApi } from "../../ipc/commands";
import { listenEvent, EVENTS, EventVersionGate, type TerminalControlEvent, type TerminalThrottledEvent } from "../../ipc/events";
import { onEventsResync } from "../../ipc/webTransport";
import { fetchImageService, uploadImage } from "../../ipc/webFiles";
import { clientId, WEB } from "../../ipc/env";
import { takePendingCommand, sessionStatusText, applyRemoteTabTitle, useUi } from "../../app/store";
import { formatBinding, matchKeybinding, useKeybindings } from "../../app/keybindings";
import { confirmHostKeyIfNeeded, connectWithHostKeyConfirm } from "../../app/hostKeys";
import { disconnectSessionWithConfirm } from "./sessionDisconnect";
import { createOsc9Notifier, createOsc52Handler } from "./oscHandlers";
import { bytesToBase64, describeImageUploadFailure, markdownImageLink } from "./imagePaste";
import {
  THROTTLE_RECOVERED_MS,
  throttleStateFrom,
  throttleView,
  type ThrottleState,
} from "./terminalThrottle";
import { ask, describeTarget, finishSave, pickSavePath, promptText } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { hasActiveOverlay, isEditableTarget, isImeKeyEvent } from "../../ui/DialogHost";
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

type TerminalControlStateEvent = TerminalControlEvent & {
  cwd?: string;
  durable?: boolean;
};

function isStoreTabDead(id: string): boolean {
  return useUi
    .getState()
    .workspaces.some((w) => w.panes.some((p) => p.tabs.some((t) => t.id === id && t.dead === true)));
}

function canPlaceOverlayFocus(pane: HTMLElement | null): boolean {
  if (hasActiveOverlay()) return false;
  const active = document.activeElement;
  if (!(active instanceof HTMLElement) || active === document.body) return true;
  if (active.closest(".nx-overlay, .nx-menu, .nx-modal, .nx-command-modal")) return false;
  if (isEditableTarget(active)) return false;
  const activePane = active.closest(".nx-pane");
  if (activePane && activePane !== pane) return false;
  return true;
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
  const [throttle, setThrottle] = useState<ThrottleState | null>(null);
  const [, setThrottleTick] = useState(0);
  const [claiming, setClaiming] = useState(false);
  const [attachDead, setAttachDead] = useState(false);
  const [reconnecting, setReconnecting] = useState(false);
  const [epoch, setEpoch] = useState(0);
  const [cwd, setCwd] = useState<string | null>(null);
  const [durable, setDurable] = useState(false);
  const [imagePaste, setImagePaste] = useState<{
    phase: "uploading" | "saving" | "success" | "error";
    message: string;
  } | null>(null);
  const me = clientId();
  const resumeRef = useRef(resumeTabId);
  const observerHintAt = useRef(0);
  const writeErrorAt = useRef(0);
  const controlRef = useRef(control);
  controlRef.current = control;
  const controlRefreshGen = useRef(0);
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
  const exitedCloseBtnRef = useRef<HTMLButtonElement>(null);
  const reconnectBtnRef = useRef<HTMLButtonElement>(null);
  const pushToast = useUi((s) => s.pushToast);
  const sessionKind = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.kind);
  const sessionAssetId = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.assetId);
  const sessionStatus = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.status);
  const sessionName = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.name) ?? title;
  const effectiveWinrm = resolveWinrmMode(winrm, sessionKind);
  const statusText = sessionStatusText(sessionStatus);
  const canReconnect =
    sessionStatus === "disconnected" || sessionStatus === "failed" || sessionStatus === undefined;
  const canDisconnect =
    sessionStatus === "connected" ||
    sessionStatus === "connecting" ||
    sessionStatus === "reconnecting";
  const blocksSupported = !effectiveWinrm;
  const bindings = useKeybindings();
  const searchBindingLabel = formatBinding(bindings.terminalSearch);
  const sessionNameRef = useRef(sessionName);
  sessionNameRef.current = sessionName;
  const osc52Handler = useRef(
    createOsc52Handler({
      enabled: () => useUi.getState().terminalOsc52,
      writeText: async (text) => navigator.clipboard.writeText(text),
      onDenied: () =>
        pushToast(
          "info",
          `「${sessionNameRef.current}」尝试写入剪贴板（OSC 52），已拒绝：此功能默认关闭，可在「设置 → 终端」中开启。`,
        ),
      onError: (e) =>
        pushToast(
          "error",
          `「${sessionNameRef.current}」写入剪贴板失败：${describeError(e)}。剪贴板权限可能被浏览器或系统拒绝。`,
        ),
    }),
  ).current;
  const osc9Notifier = useRef(
    createOsc9Notifier({
      notify: (body) => pushToast("info", `「${sessionNameRef.current}」通知：${body}`),
    }),
  ).current;
  const handleOscTitle = useCallback(
    (t: string) => {
      applyRemoteTabTitle(storeTabId, t);
    },
    [storeTabId],
  );

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
      const gen = ++controlRefreshGen.current;
      try {
        const list = await terminalApi.listLive();
        if (gen !== controlRefreshGen.current) return;
        if (tabId !== kernelTabIdRef.current) return;
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
    async (text: string, opts?: { fromCommand?: boolean }): Promise<"sent" | "observer" | "failed"> => {
      if (!kernelTabId) return "failed";
      const hintObserver = () => {
        const now = Date.now();
        if (opts?.fromCommand || now - observerHintAt.current > 4000) {
          observerHintAt.current = now;
          pushToast("info", "终端正由其他设备操作，点「接管控制」可接手");
        }
      };
      if (isObserver) {
        hintObserver();
        return "observer";
      }
      try {
        await terminalApi.write(kernelTabId, new TextEncoder().encode(text));
        return "sent";
      } catch (e) {
        const code = (e as { code?: string } | null)?.code;
        if (code === "not_controller") {
          if (controlRef.current?.controller === me) {
            setControl((c) => (c ? { ...c, controller: "__other__" } : c));
            void refreshControl();
          }
          hintObserver();
          return "observer";
        }
        const now = Date.now();
        if (opts?.fromCommand || now - writeErrorAt.current > 5000) {
          writeErrorAt.current = now;
          pushToast("error", `写入终端失败：${describeError(e)}`);
        }
        return "failed";
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
  const pendingControl = useRef(new Map<string, TerminalControlStateEvent>());
  const applyControl = useCallback((p: TerminalControlStateEvent) => {
    if (!controlVersions.current.accept(p.tabId, p.version)) return;
    controlRefreshGen.current += 1;
    setControl({
      controller: p.controller,
      subscribers: p.subscribers,
      viewers: p.viewers,
      exited: p.exited,
    });
    if (p.gridRevision > 0) {
      setRemoteGrid({ cols: p.cols, rows: p.rows, revision: p.gridRevision });
    }
    if (p.cwd !== undefined) setCwd(p.cwd || null);
    if (p.durable !== undefined) setDurable(p.durable);
  }, []);
  useEffect(() => {
    setCwd(null);
    setDurable(false);
    if (!kernelTabId) return;
    const pending = pendingControl.current.get(kernelTabId);
    if (pending) {
      pendingControl.current.delete(kernelTabId);
      applyControl(pending);
    }
  }, [kernelTabId, applyControl]);
  useEffect(() => {
    if (controlSupported && control?.exited) {
      useUi.getState().updateTab(storeTabId, { exited: true });
    }
  }, [controlSupported, control?.exited, storeTabId]);
  useEffect(() => {
    if (visible === false) return;
    if (attachDead) {
      if (canPlaceOverlayFocus(paneRef.current)) reconnectBtnRef.current?.focus();
      return;
    }
    if (controlSupported && control?.exited && canPlaceOverlayFocus(paneRef.current)) {
      exitedCloseBtnRef.current?.focus();
    }
  }, [visible, attachDead, controlSupported, control?.exited]);
  useEffect(() => {
    let unlisten: (() => void) | null = null;
    let cancelled = false;
    void listenEvent<TerminalControlStateEvent>(EVENTS.terminalControl, (p) => {
      if (p.tabId !== kernelTabIdRef.current) {
        if (p.cwd !== undefined || p.durable !== undefined) {
          pendingControl.current.set(p.tabId, p);
          if (pendingControl.current.size > 64) {
            const oldest = pendingControl.current.keys().next().value;
            if (oldest !== undefined) pendingControl.current.delete(oldest);
          }
        }
        return;
      }
      applyControl(p);
    }).then((off) => {
      if (cancelled) off();
      else unlisten = off;
    });
    return () => {
      cancelled = true;
      unlisten?.();
    };
  }, []);

  const throttleVersions = useRef(new EventVersionGate());
  useEffect(() => {
    let unlisten: (() => void) | null = null;
    let cancelled = false;
    void listenEvent<TerminalThrottledEvent>(EVENTS.terminalThrottled, (p) => {
      if (p.tabId !== kernelTabIdRef.current) return;
      if (!throttleVersions.current.accept(p.tabId, p.version)) return;
      setThrottle((current) => throttleStateFrom(current, p, Date.now()));
    }).then((off) => {
      if (cancelled) off();
      else unlisten = off;
    });
    return () => {
      cancelled = true;
      unlisten?.();
    };
  }, []);

  useEffect(
    () =>
      onEventsResync(() => {
        throttleVersions.current = new EventVersionGate();
        setThrottle(null);
        void refreshControl();
      }),
    [refreshControl],
  );

  useEffect(() => {
    if (throttle?.recoveredAt == null) return;
    const clearTimer = window.setTimeout(
      () => setThrottleTick((n) => n + 1),
      THROTTLE_RECOVERED_MS + 50,
    );
    return () => {
      window.clearTimeout(clearTimer);
    };
  }, [throttle]);

  const throttleNow = throttleView(throttle, Date.now());

  useEffect(() => {
    const pane = paneRef.current;
    if (!pane) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.repeat || isImeKeyEvent(e)) return;
      if (!matchKeybinding(e, "terminalSearch")) return;
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
      const s = await connectWithHostKeyConfirm(() => sessionApi.connect(assetId));
      if (!s) {
        pushToast("info", "已取消重连");
        return;
      }
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

  const handleSelectionCopy = (text: string, error: unknown | null) => {
    if (error) {
      const name = (error as { name?: string } | null)?.name;
      const reason = name === "NotAllowedError" ? "剪贴板权限被拒绝" : describeError(error);
      pushToast("error", `自动复制选中内容失败：${reason}`);
      return;
    }
    pushToast("success", `已自动复制 ${text.length} 个字符`);
  };

  const disconnectSession = async () => {
    await disconnectSessionWithConfirm(sessionId, sessionName);
  };

  const reconnectSession = async () => {
    try {
      if (!(await confirmHostKeyIfNeeded(sessionAssetId ?? "", sessionKind))) {
        pushToast("info", "已取消重连");
        return;
      }
      const started = await sessionApi.reconnect(sessionId);
      pushToast(
        started ? "info" : "error",
        started ? "正在重连…结果会显示在工具栏的状态徽标上" : "重连未能启动：服务正在关闭",
      );
    } catch (e) {
      if ((e as { code?: string } | null)?.code === "not_found") {
        pushToast("error", "会话已在服务端删除，请改用「重新连接这台主机」新建连接");
        return;
      }
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

  const handlePasteImages = useCallback(
    async (files: File[]) => {
      if (!kernelTabId) return;
      if (isObserver) {
        pushToast("info", "终端正由其他设备操作，点「接管控制」可接手");
        return;
      }
      setImagePaste({ phase: "uploading", message: `正在上传 ${files.length} 张图片到服务端图床（限时公开链接）…` });
      try {
        const service = await fetchImageService();
        if (service) {
          const links: string[] = [];
          for (const file of files) {
            const result = await uploadImage(file);
            links.push(markdownImageLink(file.name, result.url));
          }
          handleRef.current?.paste(links.join(" "));
          setImagePaste({
            phase: "success",
            message: `已插入 ${links.length} 张图片的 Markdown 链接（限时公开链接，过期后失效）`,
          });
          return;
        }
        if (WEB) {
          setImagePaste({
            phase: "error",
            message:
              "服务端未提供图片上传服务（未配置图床）。图片没有被上传到任何服务器；可请管理员为服务端配置数据目录后重试。",
          });
          return;
        }
        const confirmed = await ask(
          "当前连接没有可用的图片上传服务。改为把图片保存到本地文件，并在光标处插入本地路径的 Markdown 链接？图片不会上传到任何服务器。",
          { title: "图片粘贴", kind: "info" },
        );
        if (!confirmed) {
          setImagePaste(null);
          return;
        }
        setImagePaste({ phase: "saving", message: "正在保存到本地…" });
        const links: string[] = [];
        try {
          for (const file of files) {
            const path = await pickSavePath(file.name || "pasted-image.png");
            if (!path) {
              setImagePaste({
                phase: "error",
                message: "已取消保存：图片未上传，终端输入未改动。",
              });
              return;
            }
            const bytes = new Uint8Array(await file.arrayBuffer());
            const saved = await filesApi.saveImage(path, bytesToBase64(bytes));
            links.push(markdownImageLink(file.name, saved.path));
          }
        } catch (e) {
          setImagePaste({ phase: "error", message: `保存图片到本地失败：${describeError(e)}` });
          return;
        }
        handleRef.current?.paste(links.join(" "));
        setImagePaste({
          phase: "success",
          message: `已保存到本地并插入 ${links.length} 个 Markdown 链接（未上传到任何服务器）`,
        });
      } catch (e) {
        setImagePaste({ phase: "error", message: describeImageUploadFailure(e) });
      }
    },
    [kernelTabId, isObserver, pushToast],
  );

  const pasteImagesFromClipboard = async () => {
    if (!kernelTabId) return;
    const files: File[] = [];
    try {
      const items = await navigator.clipboard.read();
      for (const item of items) {
        const type = item.types.find((t) => t.startsWith("image/"));
        if (!type) continue;
        const blob = await item.getType(type);
        files.push(new File([blob], `pasted-image.${type.slice("image/".length) || "png"}`, { type }));
      }
    } catch (e) {
      pushToast("error", `读取剪贴板图片失败：${describeError(e)}`);
      return;
    }
    if (files.length === 0) {
      pushToast("info", "剪贴板里没有图片：先截图或复制图片文件，再在终端里 Ctrl+V 或拖入");
      return;
    }
    await handlePasteImages(files);
  };

  useEffect(() => {
    if (imagePaste?.phase !== "success") return;
    const timer = window.setTimeout(() => setImagePaste(null), 4500);
    return () => window.clearTimeout(timer);
  }, [imagePaste]);

  const inputCommand = async () => {
    if (!kernelTabId) return;
    const cmd = await promptText("要发送到终端的命令（可多行，回车换行）", "", {
      multiLine: true,
    });
    if (!cmd) return;
    const body = cmd.endsWith("\n") ? cmd : `${cmd}\n`;
    const result = await sendData(body, { fromCommand: true });
    if (result === "sent") pushToast("success", "命令已发送");
  };

  const toggleBlocksPanel = () => {
    const next = !blocksOpen;
    setBlocksOpen(next);
    userClosedBlocks.current = !next;
  };

  const buildToolbarOverflowMenu = (): MenuItem[] => [
    { kind: "group", label: "操作" },
    {
      kind: "item",
      label: "搜索终端内容",
      icon: <IconSearch size={13} />,
      accel: searchBindingLabel,
      onSelect: () => {
        const next = !searchOpen;
        setSearchOpen(next);
        if (next) window.setTimeout(() => searchInputRef.current?.focus(), 0);
      },
    },
    {
      kind: "item",
      label: recording ? "停止录制" : "录制输出",
      icon: recording ? <IconStop size={13} /> : <IconSave size={13} />,
      hint: "保存到文件",
      disabled: !kernelTabId,
      onSelect: () => void toggleRecord(),
    },
    ...(blocksSupported
      ? ([
          { kind: "separator" },
          { kind: "group", label: "命令块" },
          {
            kind: "item",
            label: "定位到上一条命令",
            icon: <IconArrowUp size={13} />,
            disabled: !blocks.length,
            onSelect: () => handleRef.current?.navigateBlock("prev"),
          },
          {
            kind: "item",
            label: "定位到下一条命令",
            icon: <IconArrowDown size={13} />,
            disabled: !blocks.length,
            onSelect: () => handleRef.current?.navigateBlock("next"),
          },
          {
            kind: "item",
            label: "命令块",
            icon: <IconList size={13} />,
            hint: blocks.length > 0 ? `${blocks.length} 条` : "折叠输出 / 复制 / 定位",
            onSelect: toggleBlocksPanel,
          },
        ] satisfies MenuItem[])
      : []),
    { kind: "separator" },
    {
      kind: "item",
      label: "字符编码",
      submenu: ENCODINGS.map((enc) => ({
        kind: "item",
        label: enc,
        hint: enc === encoding ? "当前" : undefined,
        onSelect: () => void applyEncoding(enc),
      })),
    },
  ];

  const openToolbarOverflow = (e: ReactMouseEvent<HTMLButtonElement>) => {
    e.stopPropagation();
    const r = e.currentTarget.getBoundingClientRect();
    setMenu({ x: r.left, y: r.bottom, title: "终端操作", items: buildToolbarOverflowMenu() });
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
    const staleSession = sessionStatus === undefined || isStoreTabDead(storeTabId);

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
        label: "粘贴图片",
        hint: "上传图床或存本地，插入链接",
        disabled: !kernelTabId,
        onSelect: () => void pasteImagesFromClipboard(),
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
        accel: searchBindingLabel,
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
        hint: "屏幕与滚动历史",
        disabled: !kernelTabId,
        onSelect: () => void saveLogToFile(),
      },
      {
        kind: "item",
        label: staleSession ? "重新连接这台主机" : "重连会话（保留当前终端）",
        icon: <IconRefresh size={13} />,
        hint: staleSession ? "连接已失效，本标签将打开新终端" : statusText,
        disabled: staleSession ? reconnecting : !canReconnect,
        onSelect: () => void (staleSession ? reconnectNow() : reconnectSession()),
      },
      {
        kind: "item",
        label: "断开连接",
        icon: <IconPlug size={13} />,
        danger: true,
        disabled: !canDisconnect,
        hint: canDisconnect ? undefined : statusText,
        onSelect: () => void disconnectSession(),
      },
      { kind: "separator" },
      { kind: "group", label: "分屏" },
      {
        kind: "item",
        label: isSplit ? "取消分屏" : "上下分屏",
        icon: isSplit ? <IconMergeH size={13} /> : <IconSplitH size={13} />,
        accel: formatBinding(bindings.toggleSplit),
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
        {effectiveWinrm && (
          <span
            className="nx-badge nx-badge-amber"
            title="WinRM 非交互模式：每条命令在新的 shell 中执行，不支持 vim/top 等交互界面"
          >
            非交互模式
          </span>
        )}
        {containerId && (
          <span
            className="nx-badge nx-badge-purple"
            title="在容器里执行命令的终端（docker exec）：关闭标签会结束这个进程，不能转入后台"
          >
            容器内 exec
          </span>
        )}
        {throttleNow?.active && (
          <span
            className="nx-badge nx-badge-amber"
            title={`终端输出积压（${throttleNow.inflightBytes} 字节待发送），画面可能有延迟`}
          >
            输出积压
          </span>
        )}
        {throttleNow?.recovered && (
          <span className="nx-badge nx-badge-green" title="终端输出积压已消退，画面恢复实时">
            输出已恢复
          </span>
        )}
        <Cwd cwd={cwd} />
        {kernelTabId && <Daemon durable={durable} sessionKind={sessionKind} />}
        <div className="nx-spacer" />

        <button
          className={`nx-btn nx-btn-ghost nx-btn-sm max-[560px]:hidden ${searchOpen ? "bg-neutral-800 text-neutral-100" : ""}`}
          title={`搜索终端内容 (${searchBindingLabel})`}
          onClick={() => setSearchOpen((v) => !v)}
        >
          <IconSearch size={13} />
          搜索
        </button>

        <select
          className="nx-select nx-input-sm w-[88px] font-mono max-[560px]:hidden"
          value={encoding}
          title="终端字符编码（对之后的输出生效）"
          onChange={(e) => void applyEncoding(e.target.value)}
        >
          {ENCODINGS.map((enc) => (
            <option key={enc} value={enc}>
              {enc}
            </option>
          ))}
        </select>

        <button
          className={`nx-btn nx-btn-sm max-[560px]:hidden ${recording ? "nx-btn-danger" : "nx-btn-ghost"}`}
          disabled={!kernelTabId}
          title={kernelTabId ? "把终端输出录制到文件" : "终端连接后才能录制"}
          onClick={() => void toggleRecord()}
        >
          {recording ? <IconStop size={12} /> : <span className="nx-dot bg-current" />}
          {recording ? "停止" : "录制"}
        </button>

        {blocksSupported && (
          <>
            <span className="nx-divider-v max-[560px]:hidden" />
            <button
              className="nx-icon-btn nx-icon-btn-sm max-[560px]:hidden"
              disabled={!blocks.length}
              title="定位到上一条命令"
              onClick={() => handleRef.current?.navigateBlock("prev")}
            >
              <IconArrowUp size={13} />
            </button>
            <button
              className="nx-icon-btn nx-icon-btn-sm max-[560px]:hidden"
              disabled={!blocks.length}
              title="定位到下一条命令"
              onClick={() => handleRef.current?.navigateBlock("next")}
            >
              <IconArrowDown size={13} />
            </button>
            <button
              className={`nx-btn nx-btn-sm max-[560px]:hidden ${blocksOpen ? "nx-btn-ghost bg-neutral-800 text-neutral-100" : "nx-btn-ghost"}`}
              title="命令块：折叠输出 / 复制 / 定位"
              onClick={toggleBlocksPanel}
            >
              <IconList size={13} />
              命令块
              {blocks.length > 0 && <span className="nx-count">{blocks.length}</span>}
            </button>
          </>
        )}
        <button
          className="nx-icon-btn nx-icon-btn-sm hidden max-[560px]:flex"
          title="更多终端操作（搜索 / 编码 / 录制 / 命令块）"
          aria-label="更多终端操作"
          onClick={openToolbarOverflow}
        >
          ⋯
        </button>
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
              onAttachInfo={(info) => {
                setThrottle(null);
                setControl({
                  controller: info.controller,
                  subscribers: info.subscribers,
                  viewers: info.viewers,
                  exited: info.exited,
                });
              }}
              onAttach={(id) => {
                setKernelTabId(id);
                setAttachDead(false);
                setThrottle(null);
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
              onTitle={handleOscTitle}
              onNotification={osc9Notifier}
              onClipboard={osc52Handler}
              onPasteImages={(files) => void handlePasteImages(files)}
              onHandle={(h) => {
                handleRef.current = h;
              }}
              onSelectionCopy={handleSelectionCopy}
              registerSearch={(api) => {
                searchApi.current = api;
              }}
            />
          </div>

          {controlSupported && control && (control.exited || isObserver) && (
            <div
              role="alert"
              className="pointer-events-none absolute inset-0 z-10 flex flex-col items-center justify-center gap-2.5 bg-neutral-950/70 px-6 text-center backdrop-blur-[1px]"
            >
              <span className="flex items-center gap-1.5 text-[13px] font-semibold text-neutral-100">
                <IconEye size={15} className="text-amber-300" />
                {control.exited
                  ? "终端进程已结束"
                  : control.controller
                    ? "终端正由其他设备操作"
                    : "当前无人操作"}
              </span>
              {control.exited ? (
                <>
                  <span className="max-w-[440px] text-[11.5px] leading-relaxed text-neutral-400">
                    进程已经退出，这里只能查看它最后的内容。要继续操作请新建一个终端。
                  </span>
                  <button
                    ref={exitedCloseBtnRef}
                    className="nx-btn nx-btn-ghost nx-btn-sm pointer-events-auto"
                    onClick={() => void useUi.getState().closeTab(storeTabId)}
                  >
                    <IconClose size={13} />
                    关闭标签
                  </button>
                </>
              ) : (
                <>
                  <span className="max-w-[440px] text-[11.5px] leading-relaxed text-neutral-400">
                    {control.controller
                      ? "多个设备可以同时观看，但同一时刻只有一个设备能操作。接管后，对方将转为只读观看，终端尺寸也会按你的窗口重排。"
                      : "多个设备可以同时观看，但同一时刻只有一个设备能操作。接管后即可输入，终端尺寸会按你的窗口重排。"}
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

          {imagePaste && (
            <div
              role="status"
              aria-live="polite"
              className={`absolute left-1/2 top-3 z-30 flex max-w-[92%] -translate-x-1/2 items-center gap-2 rounded-md border px-3 py-1.5 text-[12px] shadow-lg ${
                imagePaste.phase === "error"
                  ? "border-red-500/50 bg-neutral-900/95 text-red-300"
                  : imagePaste.phase === "success"
                    ? "border-green-500/50 bg-neutral-900/95 text-green-300"
                    : "border-neutral-600/60 bg-neutral-900/95 text-neutral-200"
              }`}
            >
              {(imagePaste.phase === "uploading" || imagePaste.phase === "saving") && (
                <IconRefresh size={13} className="shrink-0 animate-spin" />
              )}
              <span className="min-w-0 break-all">{imagePaste.message}</span>
              <button
                type="button"
                className="nx-icon-btn nx-icon-btn-sm shrink-0"
                aria-label="关闭图片粘贴提示"
                title="关闭"
                onClick={() => setImagePaste(null)}
              >
                <IconClose size={12} />
              </button>
            </div>
          )}

          {attachDead && (
            <div
              role="alert"
              className="absolute inset-0 z-20 flex flex-col items-center justify-center gap-2.5 bg-neutral-950/80 px-6 text-center backdrop-blur-[1px]"
            >
              <span className="flex items-center gap-1.5 text-[13px] font-semibold text-neutral-100">
                <IconRefresh size={15} className="text-amber-300" />
                这个终端已失效
              </span>
              <span className="max-w-[440px] text-[11.5px] leading-relaxed text-neutral-400">
                它所属的连接在服务端已经不在了（服务端重启，或连接已被回收）。重新连接会在这个标签里打开一个新的终端，当前显示的内容会被清空；已经产生的输出仍可在「终端历史」中查看。
              </span>
              <div className="flex items-center gap-2">
                <button
                  ref={reconnectBtnRef}
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

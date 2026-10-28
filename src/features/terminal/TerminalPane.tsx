import { useCallback, useEffect, useRef, useState, useSyncExternalStore, type MouseEvent as ReactMouseEvent } from "react";
import { XtermView, type TerminalHandle, type TerminalLinkMenuRequest } from "./XtermView";
import { EARLIER_HISTORY_PANEL_ATTRIBUTE, EarlierHistory } from "./EarlierHistoryPanel";
import { NO_TERMINAL_HISTORY, terminalHistoryInfo, type TerminalHistoryInfo } from "./earlierHistory";
import { cleanTerminalLink, displayTerminalLink } from "./terminalLinks";
import { CommandBlockPanel } from "./CommandBlockPanel";
import { FollowerSizeBadge } from "./FollowerSizeBadge";
import type { FollowerScale, FollowerZoom } from "./followerScale";
import { TerminalKeysBar } from "./TerminalKeysBar";
import { takeTapForComposer } from "./terminalInputMode";
import { Cwd } from "./Cwd";
import { PersistenceHintBar } from "./PersistenceHintBar";
import { persistenceHint, persistenceHintFailure, persistenceHintKey, persistenceHintMessage, persistenceHintNote } from "./persistenceHint";
import {
  dismissPersistenceHint,
  forgetPersistenceHints,
  prunePersistenceHints,
  publishPersistenceTab,
  usePersistenceDismissals,
} from "./persistenceHintStore";
import { localDurableEnabled, useLocalHelperStatus } from "./localHelperStatus";
import { showSettingsSection } from "../settings/settingsNavigation";
import { HelperOperationDialog } from "./HelperOperationDialog";
import { replacedText } from "./HelperOperationView";
import { resolveWinrmMode } from "./terminalPolicy";
import { splitAllowedForHeight } from "./workspaceLayout";
import {
  BroadcastInputQueue,
  broadcastRiskMessage,
  collectBroadcastCandidates,
  getBroadcastConfirmationChallenge,
  localRemoteMix,
  quickBroadcastIds,
} from "./broadcast";
import { BroadcastStrip } from "./BroadcastPanel";
import { BroadcastPickerModal } from "./BroadcastPickerModal";
import { PreviewShareMenu } from "../sharing/PreviewShareMenu";
import { ProductionConfirmationDialog } from "../execbatch/ProductionConfirmationDialog";
import { ProductionWriteRiskNotice } from "../production/ProductionWarning";
import { useAssetProductionClassification } from "../production/productionSettings";
import type { CommandBlock } from "./commandBlocks";
import {
  terminalApi,
  fsApi,
  assetApi,
  syncApi,
  type HelperOperationResult,
  type HelperReplaceFailureInfo,
} from "../../ipc/commands";
import type { BroadcastEnvironmentConfirmation, BroadcastProductionChallenge } from "../production/productionChallenge";
import { listenEvent, EVENTS, EventVersionGate, type TerminalControlEvent, type TerminalThrottledEvent } from "../../ipc/events";
import { onEventsResync } from "../../ipc/webTransport";
import { clientId, WEB } from "../../ipc/env";
import { openTerminalTab, takePendingDirectory, sessionStatusText, isSessionSuspect, confirmSuspectReconnect, connectProgressText, reconnectLiveSession, reconnectDeadHost, requestCloseTab, useUi, fallbackTerminalReason, useKnownDurableReadiness, type AppTab, type BroadcastState, type Workspace } from "../../app/store";
import { applyRemoteTabTitleToView, reconcileBackendTabTitle, resetTerminalView } from "../../app/terminalViewStore";
import { directoryCommandFor } from "../files/pathUtils";
import { formatBinding, matchKeybinding, useKeybindings } from "../../app/keybindings";
import { modHint } from "../../app/platform";
import { disconnectSessionWithConfirm } from "./sessionDisconnect";
import { createOsc52Handler } from "./oscHandlers";
import {
  bytesToBase64,
  displayFileName,
  MAX_PASTE_FILE_BYTES,
  markdownFileLink,
  pasteFileExtension,
  splitOversizedFiles,
  TERMINAL_CONTROL,
  uniquePasteFileNames,
} from "./imagePaste";
import { DEFAULT_PASTE_DIRS, joinPastePath, pasteDirSettings, terminalPastePath, type PasteDirSettings } from "./pasteUploadDir";
import { fileChunkSource, UPLOAD_CHUNK_SIZE, uploadChunked } from "../files/chunkedUpload";
import { createTransferTaskId } from "../files/transferTaskId";
import { confirmMultilinePaste } from "./terminalPaste";
import {
  THROTTLE_RECOVERED_MS,
  THROTTLE_RESYNC_MS,
  throttleStateFrom,
  throttleView,
  type ThrottleState,
} from "./terminalThrottle";
import type { TerminalStreamBacklog } from "./terminalStream";
import { ask, askChoice, finishSave, pickSavePath, promptText } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { copyText, readText } from "../../ui/clipboard";
import { hasActiveOverlay, isEditableTarget, isImeKeyEvent } from "../../ui/DialogHost";
import { clipText } from "../../ui/textClip";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import {
  IconArrowDown,
  IconArrowUp,
  IconClose,
  IconCommand,
  IconCopy,
  IconEdit,
  IconEye,
  IconList,
  IconMergeH,
  IconPlug,
  IconRefresh,
  IconSave,
  IconSearch,
  IconSplitH,
  IconSplitV,
  IconZap,
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

function isStoreTabDead(id: string): boolean {
  return useUi
    .getState()
    .workspaces.some((w) => w.panes.some((p) => p.tabs.some((t) => t.id === id && t.dead === true)));
}

function findWorkspacePane(
  workspaces: Workspace[],
  storeTabId: string,
): { wsId: string; paneId: string } | null {
  for (const w of workspaces) {
    for (const p of w.panes) {
      if (p.tabs.some((t) => t.id === storeTabId)) return { wsId: w.id, paneId: p.id };
    }
  }
  return null;
}

function findStoreTab(workspaces: Workspace[], storeTabId: string): AppTab | undefined {
  for (const w of workspaces) {
    for (const p of w.panes) {
      const tab = p.tabs.find((t) => t.id === storeTabId);
      if (tab) return tab;
    }
  }
  return undefined;
}

interface BroadcastContext {
  generation: number;
  targetIds: string[];
}

interface PendingBroadcastConfirmation {
  sequence: number;
  data: string;
  generation: number;
  challenge: BroadcastProductionChallenge;
}

function readBroadcastContext(
  state: {
    workspaces: Workspace[];
    activeWorkspaceId: string | null;
    broadcast: BroadcastState | null;
  },
  storeTabId: string,
): BroadcastContext | null {
  const broadcast = state.broadcast;
  if (
    !broadcast ||
    broadcast.origin.storeTabId !== storeTabId ||
    state.activeWorkspaceId !== broadcast.origin.workspaceId
  ) {
    return null;
  }
  const workspace = state.workspaces.find((item) => item.id === broadcast.origin.workspaceId);
  const pane = workspace?.panes.find((item) => item.id === workspace.activePaneId);
  if (!pane || pane.activeTabId !== storeTabId || !pane.tabs.some((tab) => tab.id === storeTabId)) {
    return null;
  }
  return { generation: broadcast.generation, targetIds: broadcast.targetIds };
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

function replaceFailureFromError(error: unknown): HelperReplaceFailureInfo | null {
  if (!error || typeof error !== "object") return null;
  if ((error as { code?: unknown }).code !== "helper_replace_failed") return null;
  const detail = (error as { detail?: unknown }).detail;
  if (!detail || typeof detail !== "object") return null;
  const candidate = detail as Partial<HelperReplaceFailureInfo>;
  if (typeof candidate.stage !== "string") return null;
  return {
    stage: candidate.stage,
    stopped: candidate.stopped === true,
    forced: candidate.forced === true,
    rolledBack: candidate.rolledBack === true,
    cause: typeof candidate.cause === "string" ? candidate.cause : undefined,
  };
}

function rollbackResultFromError(error: unknown): HelperOperationResult | null {
  if (!error || typeof error !== "object") return null;
  const detail = (error as { detail?: unknown }).detail;
  if (!detail || typeof detail !== "object") return null;
  const candidate = detail as Partial<HelperOperationResult>;
  if (typeof candidate.sessionId !== "string" || candidate.update?.rolledBack !== true) return null;
  return candidate as HelperOperationResult;
}

// Desktop read-only preview depends on a logged-in server account ("account sync" link): a shared singleton polls
// syncApi.status; the first subscribing TerminalPane starts it and it stops after all unmount; login/disconnect
// reflects in the entry's visibility within at most PREVIEW_STATUS_POLL_MS. TerminalPane's mount environment does not guarantee
// a react-query Provider, so useQuery is not used. Transport has only web/desktop values;
// only WEB is used here (some existing tests' env mocks lack the DESKTOP export). A logged-out desktop
// returns false and TerminalPane does not render the preview menu entry, i.e. the gating formula
// (WEB || (DESKTOP && logged in)) && kernelTabId.
const PREVIEW_STATUS_POLL_MS = 5_000;

let previewStatusLoggedIn = false;
let previewStatusKnown = false;
const previewStatusListeners = new Set<() => void>();
let previewStatusTimer: ReturnType<typeof setInterval> | null = null;

async function pollPreviewStatus(): Promise<void> {
  let loggedIn = false;
  try {
    loggedIn = (await syncApi.status()).loggedIn;
  } catch {
    loggedIn = false;
  }
  if (previewStatusKnown && loggedIn === previewStatusLoggedIn) return;
  previewStatusLoggedIn = loggedIn;
  previewStatusKnown = true;
  for (const listener of previewStatusListeners) listener();
}

function subscribePreviewStatus(listener: () => void): () => void {
  if (WEB) return () => {};
  previewStatusListeners.add(listener);
  if (previewStatusTimer === null) {
    void pollPreviewStatus();
    previewStatusTimer = setInterval(() => void pollPreviewStatus(), PREVIEW_STATUS_POLL_MS);
  }
  return () => {
    previewStatusListeners.delete(listener);
    if (previewStatusListeners.size === 0 && previewStatusTimer !== null) {
      clearInterval(previewStatusTimer);
      previewStatusTimer = null;
    }
  };
}

export function usePreviewShareEnabled(): boolean {
  const loggedIn = useSyncExternalStore(subscribePreviewStatus, () => previewStatusLoggedIn);
  return WEB || loggedIn;
}

// PasteUploadRun tracks one paste upload so the banner can cancel it: the flag stops further files and the current
// transfer attempt is cancelled on the backend.
interface PasteUploadRun {
  cancelled: boolean;
  transfer: { taskId: string; attemptId: string } | null;
}

class PasteUploadCancelled extends Error {
  constructor() {
    super("cancelled");
  }
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
  const previewShareEnabled = usePreviewShareEnabled();
  const [searchOpen, setSearchOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [encoding, setEncoding] = useState("utf-8");
  const [blocks, setBlocks] = useState<CommandBlock[]>([]);
  const [blocksOpen, setBlocksOpen] = useState(false);
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const [control, setControl] = useState<{
    sizeOwner: string | null;
    subscribers: number;
    viewers: number;
    exited?: boolean;
  } | null>(null);
  const [remoteGrid, setRemoteGrid] = useState<{
    cols: number;
    rows: number;
    revision: number;
  } | null>(null);
  const [throttle, setThrottle] = useState<ThrottleState | null>(null);
  // While another device owns the PTY size: how this view scales the grid, and whether it fits the rows only.
  const [followerScale, setFollowerScale] = useState<FollowerScale | null>(null);
  const [followerZoom, setFollowerZoom] = useState<FollowerZoom>("fit");
  const [streamBacklog, setStreamBacklog] = useState<TerminalStreamBacklog | null>(null);
  const [, setThrottleTick] = useState(0);
  const [switching, setSwitching] = useState(false);
  const [attachDead, setAttachDead] = useState(false);
  const [execInterrupted, setExecInterrupted] = useState(false);
  const [reconnecting, setReconnecting] = useState(false);
  const [epoch, setEpoch] = useState(0);
  const [cwd, setCwd] = useState<string | null>(null);
  const [durable, setDurable] = useState(false);
  const [historyInfo, setHistoryInfo] = useState<TerminalHistoryInfo>(NO_TERMINAL_HISTORY);
  const [forceRegular, setForceRegular] = useState(false);
  const [ordinaryStarting, setOrdinaryStarting] = useState(false);
  const [helperActionBusy, setHelperActionBusy] = useState(false);
  const [helperActionLabel, setHelperActionLabel] = useState("正在处理…");
  const [helperPanelOpen, setHelperPanelOpen] = useState(false);
  const [helperStatusLoading, setHelperStatusLoading] = useState(false);
  const [helperReady, setHelperReady] = useState(false);
  const [helperDetails, setHelperDetails] = useState<HelperOperationResult | null>(null);
  const [replaceFailure, setReplaceFailure] = useState<HelperReplaceFailureInfo | null>(null);
  const [helperPanelError, setHelperPanelError] = useState<string | null>(null);
  const [offlinePath, setOfflinePath] = useState<string | null>(null);
  const [imagePaste, setImagePaste] = useState<{
    phase: "uploading" | "success" | "error";
    message: string;
    cancellable?: boolean;
  } | null>(null);
  const pasteUploadRef = useRef<PasteUploadRun | null>(null);
  const [broadcastPickerOpen, setBroadcastPickerOpen] = useState(false);
  const me = clientId();
  const resumeRef = useRef(resumeTabId);
  const resumePropRef = useRef(resumeTabId);
  if (resumePropRef.current !== resumeTabId) {
    resumePropRef.current = resumeTabId;
    if (resumeTabId !== kernelTabId) resumeRef.current = resumeTabId;
  }
  const resumeAttemptedRef = useRef(false);
  const writeErrorAt = useRef(0);
  const [broadcastQueue] = useState(() => new BroadcastInputQueue());
  const [broadcastConfirmation, setBroadcastConfirmation] = useState<PendingBroadcastConfirmation | null>(null);
  const broadcastConfirmationRef = useRef<PendingBroadcastConfirmation | null>(null);
  const pendingBroadcastConfirmations = useRef<PendingBroadcastConfirmation[]>([]);
  const broadcastConfirmationSequence = useRef(0);
  const broadcastConfirmationEpoch = useRef(0);
  const broadcastConfirmingRef = useRef(false);
  const broadcastVisibleRef = useRef(visible !== false);
  broadcastVisibleRef.current = visible !== false;
  const controlRefreshGen = useRef(0);
  const kernelTabIdRef = useRef<string | null>(null);
  kernelTabIdRef.current = kernelTabId;
  const searchApi = useRef<{
    findNext: (t: string) => void;
    findPrevious: (t: string) => void;
  } | null>(null);
  const handleRef = useRef<TerminalHandle | null>(null);
  const composerRef = useRef<HTMLInputElement | null>(null);
  const paneRef = useRef<HTMLDivElement>(null);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const exitedCloseBtnRef = useRef<HTMLButtonElement>(null);
  const reconnectBtnRef = useRef<HTMLButtonElement>(null);
  const reconnectBusyRef = useRef(false);
  const switchingTimerRef = useRef<number | null>(null);
  const wasVisibleRef = useRef(visible !== false);
  const pushToast = useUi((s) => s.pushToast);
  const sessionKind = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.kind);
  const localHelper = useLocalHelperStatus(sessionKind === "local" && !WEB);
  const sessionAssetId = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.assetId);
  const sessionStatus = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.status);
  const sessionLiveness = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.liveness);
  const productionClassification = useAssetProductionClassification(sessionAssetId);
  const staleConfig = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.staleConfig === true);
  const execTabAssetId = useUi((s) => s.workspaces.flatMap((w) => w.panes).flatMap((p) => p.tabs).find((tab) => tab.id === storeTabId)?.assetId);
  const execAttachedSessionRef = useRef(sessionId);
  const helperDetailsSessionRef = useRef(sessionId);
  helperDetailsSessionRef.current = sessionId;
  const previousHelperSessionRef = useRef(sessionId);
  const helperRequestGenerationRef = useRef(0);
  const nextHelperRequestGeneration = () => {
    helperRequestGenerationRef.current += 1;
    return helperRequestGenerationRef.current;
  };
  const helperRequestIsCurrent = (generation: number, requestSessionId: string) =>
    helperRequestGenerationRef.current === generation && helperDetailsSessionRef.current === requestSessionId;
  const execTargetAssetRef = useRef(execTabAssetId ?? sessionAssetId);
  if (execTargetAssetRef.current === undefined && sessionAssetId !== undefined) {
    execTargetAssetRef.current = sessionAssetId;
  }
  const execTargetMatches = sessionAssetId !== undefined && sessionAssetId === execTargetAssetRef.current;
  const connectProgress = useUi((s) =>
    sessionAssetId ? s.connectProgress[sessionAssetId] : undefined,
  );
  const helperOperation = useUi((s) => s.helperOperations.operations[storeTabId]);
  const regularReason = useUi((s) => findStoreTab(s.workspaces, storeTabId)?.regularReason);
  const helperRetryPending = useUi((s) => s.helperRetries[sessionId]?.tabId === storeTabId);
  const sessionName = useUi((s) => s.sessions.find((x) => x.id === sessionId)?.name) ?? title;
  const recording = useUi((s) => (kernelTabId ? s.recordings[kernelTabId] : undefined));
  const workspaceId = useUi(
    (s) => findWorkspacePane(s.workspaces, storeTabId)?.wsId ?? null,
  );
  const myPaneId = useUi(
    (s) => findWorkspacePane(s.workspaces, storeTabId)?.paneId ?? null,
  );
  const broadcastCount = useUi((s) => {
    const wsId = findWorkspacePane(s.workspaces, storeTabId)?.wsId;
    const bc = s.broadcast;
    if (!wsId || !bc || bc.origin.workspaceId !== wsId || bc.origin.storeTabId !== storeTabId) return 0;
    return bc.targetIds.length;
  });
  const memberBroadcastCount = useUi((s) => {
    const found = findWorkspacePane(s.workspaces, storeTabId);
    const bc = s.broadcast;
    if (!found || !bc || bc.origin.workspaceId !== found.wsId || bc.origin.storeTabId !== storeTabId) return 0;
    const members = new Set(bc.targetIds);
    return collectBroadcastCandidates(s.workspaces, s.sessions).filter(
      (candidate) => members.has(candidate.storeTabId) && candidate.eligible,
    ).length;
  });
  const broadcastRevision = useUi(
    (state) => readBroadcastContext(state, storeTabId)?.generation ?? null,
  );
  const [assetEncoding, setAssetEncoding] = useState<string | null>(null);
  useEffect(() => {
    if (!sessionAssetId || typeof assetApi.list !== "function") return;
    let cancelled = false;
    void assetApi
      .list()
      .then((assets) => {
        if (cancelled) return;
        const raw = assets.find((a) => a.id === sessionAssetId)?.options?.encoding;
        if (typeof raw !== "string") return;
        const value = raw.trim().toLowerCase();
        const normalized = value === "utf8" ? "utf-8" : value === "iso-8859-1" ? "latin1" : value;
        if (ENCODINGS.includes(normalized)) setAssetEncoding(normalized);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [sessionAssetId]);
  const encodingTouchedRef = useRef(false);
  useEffect(() => {
    if (assetEncoding && !encodingTouchedRef.current) setEncoding(assetEncoding);
  }, [assetEncoding]);
  const effectiveWinrm = resolveWinrmMode(winrm, sessionKind);
  const statusText = sessionStatusText(sessionStatus, sessionLiveness);
  const showConnectProgress =
    sessionStatus === "connecting" || sessionStatus === "reconnecting";
  const canReconnect =
    sessionStatus === "disconnected" || sessionStatus === "failed" || sessionStatus === undefined ||
    (staleConfig && sessionStatus === "connected") ||
    isSessionSuspect(sessionStatus, sessionLiveness);
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
      writeText: async (text) => copyText(text),
      authorize: async () => {
        const choice = await askChoice(
          `「${sessionNameRef.current}」请求写入剪贴板（OSC 52）。允许该会话写剪贴板？`,
          {
            title: "剪贴板写入请求",
            level: "warning",
            choices: [
              { key: "once", label: "允许本次", hint: "只写入这一次，下次再询问", primary: true },
              { key: "session", label: "本会话允许", hint: "该终端会话内不再询问" },
              { key: "deny", label: "拒绝", hint: "本会话内一律静默丢弃", danger: true },
            ],
          },
        );
        return choice === "once" || choice === "session" || choice === "deny" ? choice : null;
      },
      onError: (e) =>
        pushToast(
          "error",
          `「${sessionNameRef.current}」写入剪贴板失败：${describeError(e)}。剪贴板权限可能被浏览器或系统拒绝。`,
        ),
    }),
  ).current;
  const handleOscTitle = useCallback(
    (t: string) => {
      applyRemoteTabTitleToView(storeTabId, t);
    },
    [storeTabId],
  );

  const controlSupported = !containerId && !effectiveWinrm;
  // Every attached client may type. The PTY follows the size of the client
  // that typed last; the others render the PTY size until they type.
  const canResize = !controlSupported || control === null || control.sizeOwner === me;

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
            sizeOwner: hit.sizeOwner,
            subscribers: hit.subscribers,
            viewers: hit.viewers,
            exited: hit.exited,
          });
        } else {
          setControl((c) => (c ? { ...c, exited: undefined } : c));
        }
        useUi.getState().updateTab(storeTabId, { exited: hit?.exited });
      } catch {
      }
    },
    [kernelTabId, storeTabId],
  );

  const broadcastContext = useCallback((): BroadcastContext | null => {
    if (visible === false) return null;
    return readBroadcastContext(useUi.getState(), storeTabId);
  }, [storeTabId, visible]);

  useEffect(() => {
    broadcastQueue.sync(broadcastRevision);
  }, [broadcastQueue, broadcastRevision]);

  useEffect(() => {
    if (visible === false) broadcastQueue.reset();
  }, [broadcastQueue, visible]);

  useEffect(() => {
    broadcastQueue.activate();
    return () => broadcastQueue.dispose();
  }, [broadcastQueue]);

  const advanceBroadcastConfirmation = useCallback(() => {
    const next = pendingBroadcastConfirmations.current.shift() ?? null;
    broadcastConfirmationRef.current = next;
    setBroadcastConfirmation(next);
  }, []);

  const queueBroadcastConfirmation = useCallback((pending: Omit<PendingBroadcastConfirmation, "sequence">) => {
    pendingBroadcastConfirmations.current.push({
      ...pending,
      sequence: ++broadcastConfirmationSequence.current,
    });
    if (broadcastConfirmationRef.current === null) advanceBroadcastConfirmation();
  }, [advanceBroadcastConfirmation]);

  const clearBroadcastConfirmations = useCallback((notify: boolean) => {
    const hadPending = broadcastConfirmationRef.current !== null || pendingBroadcastConfirmations.current.length > 0;
    if (notify) broadcastConfirmationEpoch.current += 1;
    pendingBroadcastConfirmations.current = [];
    broadcastConfirmationRef.current = null;
    setBroadcastConfirmation(null);
    if (notify && hadPending) pushToast("info", "已取消生产广播，待确认输入未发送");
  }, [pushToast]);

  useEffect(() => {
    if (broadcastConfirmationRef.current !== null || pendingBroadcastConfirmations.current.length > 0) {
      pushToast("info", "广播目标已变化，待确认输入未发送");
    }
    clearBroadcastConfirmations(false);
  }, [broadcastRevision, clearBroadcastConfirmations, pushToast]);

  const sendData = useCallback(
    async (
      text: string,
      opts?: { fromCommand?: boolean },
      productionConfirmation?: BroadcastEnvironmentConfirmation,
    ): Promise<"sent" | "failed"> => {
      if (containerId && (execInterrupted || useUi.getState().sessions.find((x) => x.id === sessionId)?.status !== "connected")) {
        return "failed";
      }
      if (!kernelTabId) return "failed";
      const broadcast = broadcastContext();
      if (broadcast) {
        const state = useUi.getState();
        const confirmationEpoch = broadcastConfirmationEpoch.current;
        try {
          const outcome = await broadcastQueue.enqueue({
            revision: broadcast.generation,
            candidates: collectBroadcastCandidates(state.workspaces, state.sessions),
            targetIds: broadcast.targetIds,
            data: text,
            productionConfirmation,
            prepare: (targetTabIds, bytes) => terminalApi.prepareBroadcast(targetTabIds, bytes, productionConfirmation),
            write: (id, bytes, options) => terminalApi.write(id, bytes, options),
            isCurrent: () =>
              broadcastConfirmationEpoch.current === confirmationEpoch &&
              broadcastVisibleRef.current &&
              readBroadcastContext(useUi.getState(), storeTabId)?.generation === broadcast.generation,
            resolveTarget: (targetId) => {
              const latest = useUi.getState();
              return collectBroadcastCandidates(latest.workspaces, latest.sessions).find(
                (candidate) => candidate.storeTabId === targetId,
              );
            },
            onProgress: (result) => {
              useUi.getState().reportBroadcastTarget(broadcast.generation, result);
            },
          });
          return outcome && outcome.sent > 0 ? "sent" : "failed";
        } catch (cause) {
          const challenge = getBroadcastConfirmationChallenge(cause);
          if (challenge) {
            if (
              broadcastConfirmationEpoch.current === confirmationEpoch &&
              readBroadcastContext(useUi.getState(), storeTabId)?.generation === broadcast.generation
            ) {
              queueBroadcastConfirmation({ data: text, generation: broadcast.generation, challenge });
            }
          } else {
            const now = Date.now();
            if (opts?.fromCommand || now - writeErrorAt.current > 5000) {
              writeErrorAt.current = now;
              pushToast("error", `广播写入失败：${describeError(cause)}`);
            }
          }
          return "failed";
        }
      }
      broadcastQueue.reset();
      try {
        await terminalApi.write(kernelTabId, new TextEncoder().encode(text));
        return "sent";
      } catch (e) {
        const now = Date.now();
        if (opts?.fromCommand || now - writeErrorAt.current > 5000) {
          writeErrorAt.current = now;
          pushToast("error", `写入终端失败：${describeError(e)}`);
        }
        return "failed";
      }
    },
    [kernelTabId, pushToast, broadcastContext, broadcastQueue, queueBroadcastConfirmation, containerId, execInterrupted, sessionId, storeTabId],
  );

  const confirmBroadcastProduction = useCallback((environmentNames?: string[]) => {
    const pending = broadcastConfirmationRef.current;
    if (!pending || broadcastConfirmingRef.current) return;
    broadcastConfirmingRef.current = true;
    void (async () => {
      try {
        const current = readBroadcastContext(useUi.getState(), storeTabId);
        if (current?.generation !== pending.generation) {
          pushToast("info", "广播目标已变化，过期生产输入未发送");
          return;
        }
        await sendData(pending.data, undefined, {
          confirmed: true,
          targetSetHash: pending.challenge.targetSetHash,
          inputHash: pending.challenge.inputHash,
          ...(environmentNames ? { environmentNames } : {}),
        });
      } finally {
        broadcastConfirmingRef.current = false;
        if (broadcastConfirmationRef.current === pending) advanceBroadcastConfirmation();
      }
    })();
  }, [advanceBroadcastConfirmation, pushToast, sendData, storeTabId]);

  useEffect(() => () => {
    if (switchingTimerRef.current !== null) window.clearTimeout(switchingTimerRef.current);
  }, []);

  // Keep tab switches visually stable while xterm is made visible and fitted to the new pane.
  // The terminal remains mounted while its tab is hidden, so this only runs on a hidden -> visible
  // transition and never flashes during the initial mount of the active tab.
  useEffect(() => {
    const nextVisible = visible !== false;
    const wasVisible = wasVisibleRef.current;
    wasVisibleRef.current = nextVisible;
    if (!nextVisible) {
      if (switchingTimerRef.current !== null) {
        window.clearTimeout(switchingTimerRef.current);
        switchingTimerRef.current = null;
      }
      setSwitching(false);
      return;
    }
    if (wasVisible || !kernelTabId || attachDead || control?.exited) return;
    const sizeChanged = handleRef.current?.fit() ?? false;
    if (!sizeChanged) return;
    setSwitching(true);
    if (switchingTimerRef.current !== null) window.clearTimeout(switchingTimerRef.current);
    switchingTimerRef.current = window.setTimeout(() => {
      switchingTimerRef.current = null;
      setSwitching(false);
    }, 650);
  }, [visible, kernelTabId, attachDead, control?.exited]);

  const enableBroadcastScope = useCallback(
    async (scope: "pane" | "all") => {
      const st = useUi.getState();
      const found = findWorkspacePane(st.workspaces, storeTabId);
      if (!found) return;
      const candidates = collectBroadcastCandidates(st.workspaces, st.sessions);
      const ids = quickBroadcastIds(candidates, scope, found.paneId);
      if (ids.length < 2) {
        pushToast(
          "info",
          scope === "pane"
            ? "当前分屏的可广播终端不足 2 个"
            : "全部已打开工作区的可广播终端不足 2 个",
        );
        return;
      }
      const mix = localRemoteMix(candidates.filter((candidate) => ids.includes(candidate.storeTabId)));
      const confirmed = await ask(broadcastRiskMessage(ids.length, mix), {
        title: "开启命令广播",
        kind: "warning",
      });
      if (!confirmed) return;
      st.setBroadcast({
        origin: { workspaceId: found.wsId, storeTabId },
        targetIds: ids,
      });
      pushToast("info", `广播已开启：输入将发送到 ${ids.length} 个终端`);
    },
    [storeTabId, pushToast],
  );

  const closeBroadcast = useCallback(() => {
    useUi.getState().setBroadcast(null);
    pushToast("info", "广播已关闭，输入只发送到当前终端");
  }, [pushToast]);

  const controlVersions = useRef(new EventVersionGate());
  const pendingControl = useRef(new Map<string, TerminalControlEvent>());
  const applyControl = useCallback((p: TerminalControlEvent) => {
    if (!controlVersions.current.accept(p.tabId, p.version)) return;
    controlRefreshGen.current += 1;
    setControl({
      sizeOwner: p.sizeOwner,
      subscribers: p.subscribers,
      viewers: p.viewers,
      exited: p.exited,
    });
    useUi.getState().updateTab(storeTabId, { exited: p.exited });
    if (p.gridRevision > 0) {
      setRemoteGrid({ cols: p.cols, rows: p.rows, revision: p.gridRevision });
    }
    if (p.cwd !== undefined) setCwd(p.cwd || null);
    if (p.durable !== undefined) {
      setDurable(p.durable);
      useUi.getState().updateTab(storeTabId, { durable: p.durable });
    }
    const history = terminalHistoryInfo(p);
    setHistoryInfo((current) =>
      current.base === history.base && current.truncated === history.truncated
        ? current
        : history,
    );
    // Control snapshots never write back (rename is null), so there is no promise.
    void reconcileBackendTabTitle(storeTabId, p, null);
  }, [storeTabId]);
  useEffect(() => {
    setCwd(null);
    setDurable(false);
    setHistoryInfo(NO_TERMINAL_HISTORY);
    if (!kernelTabId) return;
    const pending = pendingControl.current.get(kernelTabId);
    if (pending) {
      pendingControl.current.delete(kernelTabId);
      applyControl(pending);
    }
  }, [kernelTabId, applyControl]);
  useEffect(() => {
    if (!kernelTabId) return;
    void useUi.getState().resyncRecording(kernelTabId);
  }, [kernelTabId]);
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
    void listenEvent<TerminalControlEvent>(EVENTS.terminalControl, (p) => {
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
      void useUi.getState().resyncRecording(p.tabId);
    }).then((off) => {
      if (cancelled) off();
      else unlisten = off;
    });
    return () => {
      cancelled = true;
      unlisten?.();
    };
  }, [applyControl]);

  const throttleVersions = useRef(new EventVersionGate());
  // This view's output channel: a resync notice concerns only its own view.
  const outputChannelRef = useRef<string | undefined>(undefined);
  useEffect(() => {
    let unlisten: (() => void) | null = null;
    let cancelled = false;
    void listenEvent<TerminalThrottledEvent>(EVENTS.terminalThrottled, (p) => {
      if (p.tabId !== kernelTabIdRef.current) return;
      if (!throttleVersions.current.accept(p.tabId, p.version)) return;
      const own = p.channelId === outputChannelRef.current;
      setThrottle((current) => throttleStateFrom(current, p, Date.now(), own));
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
        setStreamBacklog(null);
        void refreshControl();
        if (kernelTabIdRef.current) void useUi.getState().resyncRecording(kernelTabIdRef.current);
      }),
    [refreshControl],
  );

  // The per-tab view state (OSC title) lives in the root view store: drop this tab's
  // entry on unmount so a closed tab or an account switch never leaves a stale title
  // for precise-selector chrome.
  useEffect(() => () => resetTerminalView(storeTabId), [storeTabId]);

  useEffect(() => {
    if (throttle?.recoveredAt == null && throttle?.resyncAt == null) return;
    const clearTimer = window.setTimeout(
      () => setThrottleTick((n) => n + 1),
      Math.max(THROTTLE_RECOVERED_MS, THROTTLE_RESYNC_MS) + 50,
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
      // The earlier-history replay is not searchable through the live terminal.
      if (e.target instanceof Element && e.target.closest(`[${EARLIER_HISTORY_PANEL_ATTRIBUTE}]`)) return;
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

  const previousSessionStatus = useRef(sessionStatus);
  useEffect(() => {
    const previous = previousSessionStatus.current;
    previousSessionStatus.current = sessionStatus;
    if (previous === "connected" || sessionStatus !== "connected" || !attachDead || !resumeRef.current || forceRegular) return;
    setKernelTabId(null);
    setEpoch((value) => value + 1);
  }, [sessionStatus, attachDead, storeTabId, forceRegular]);

  const markExecInterrupted = useCallback(() => {
    if (!containerId) return;
    const id = kernelTabIdRef.current;
    kernelTabIdRef.current = null;
    setKernelTabId(null);
    setExecInterrupted(true);
    useUi.getState().updateTab(storeTabId, { exited: true });
    if (id) void terminalApi.closeTab(id, "kill", execAttachedSessionRef.current).catch(() => undefined);
  }, [containerId, storeTabId]);

  const execWasConnected = useRef(sessionStatus === "connected");
  useEffect(() => {
    if (!containerId) return;
    if (sessionStatus === "connected") {
      execWasConnected.current = true;
      return;
    }
    if (!execInterrupted && kernelTabId && (execAttachedSessionRef.current !== sessionId || sessionStatus !== undefined || execWasConnected.current)) {
      markExecInterrupted();
    }
  }, [containerId, execInterrupted, kernelTabId, markExecInterrupted, sessionId, sessionStatus]);

  const restartExec = useCallback(() => {
    if (!containerId || sessionStatus !== "connected" || !execTargetMatches) return;
    resumeRef.current = undefined;
    resumeAttemptedRef.current = false;
    useUi.getState().updateTab(storeTabId, { tabId: undefined, exited: undefined, dead: false });
    setControl(null);
    setExecInterrupted(false);
    setEpoch((value) => value + 1);
  }, [containerId, execTargetMatches, sessionStatus, storeTabId]);

  const handleAttachDead = useCallback(
    (code: string) => {
      if (containerId && (code === "interrupted" || code === "not_found")) {
        markExecInterrupted();
        return;
      }
      if (code !== "not_found") {
        if (ordinaryStarting) {
          setOrdinaryStarting(false);
          pushToast("error", "无法新建临时终端，原持久终端没有被改动。请稍后重试");
        }
        return;
      }
      const st = useUi.getState();
      const tab = st.workspaces
        .flatMap((w) => w.panes)
        .flatMap((p) => p.tabs)
        .find((t) => t.id === storeTabId);
      const session = st.sessions.find((x) => x.id === tab?.sessionId);
      if (forceRegular) {
        setOrdinaryStarting(false);
        st.updateTab(storeTabId, { dead: true });
        setKernelTabId(null);
        setAttachDead(true);
        pushToast("error", "无法新建临时终端：连接已不存在，原持久终端没有被改动。请先重新连接");
        return;
      }
      if (!containerId && !effectiveWinrm && session?.kind !== "ssh" && session?.kind !== "device" && tab?.tabId !== undefined && session?.status === "connected") {
        resumeAttemptedRef.current = false;
        resumeRef.current = undefined;
        st.updateTab(storeTabId, { tabId: undefined });
        setKernelTabId(null);
        setEpoch((n) => n + 1);
        pushToast("info", "原终端在服务端已不存在，已打开新终端");
        return;
      }
      st.updateTab(storeTabId, { dead: true });
      if (session?.kind === "ssh" || session?.kind === "device") {
        st.failHelperRestore(storeTabId, "原持久终端恢复失败；远端进程是否存活仍未知");
      }
      setKernelTabId(null);
      setAttachDead(true);
    },
    [storeTabId, containerId, effectiveWinrm, markExecInterrupted, pushToast, forceRegular, ordinaryStarting],
  );

  const reconnectNow = useCallback(async () => {
    const st = useUi.getState();
    const ws = st.workspaces.find((w) =>
      w.panes.some((p) => p.tabs.some((t) => t.id === storeTabId)),
    );
    if (!ws?.assetId) {
      pushToast("error", "找不到该主机的资产信息，请在左侧资产树中手动连接");
      return;
    }
    if (reconnectBusyRef.current) return;
    reconnectBusyRef.current = true;
    const assetId = ws.assetId;
    setReconnecting(true);
    try {
      const s = await reconnectDeadHost(ws);
      if (!s) {
        pushToast("info", "已取消重新连接");
        return;
      }
      const list = useUi.getState().sessions;
      useUi.getState().setSessions([...list.filter((x) => x.id !== s.id), s]);
      const current = useUi
        .getState()
        .workspaces.flatMap((w) => w.panes)
        .flatMap((p) => p.tabs)
        .find((t) => t.id === storeTabId);
      const resumeId = ws.assetKind === "ssh" || ws.assetKind === "device"
        ? current?.tabId
        : resumeAttemptedRef.current ? undefined : current?.tabId;
      resumeAttemptedRef.current = resumeId !== undefined;
      useUi.getState().updateTab(storeTabId, { sessionId: s.id, tabId: resumeId });
      resumeRef.current = resumeId;
      setControl(null);
      setRemoteGrid(null);
      setForceRegular(false);
      setOrdinaryStarting(false);
      setKernelTabId(null);
      setEpoch((n) => n + 1);
    } catch (e) {
      pushToast("error", `重新连接失败：${describeError(e)}。请检查网络后重试`);
    } finally {
      reconnectBusyRef.current = false;
      setReconnecting(false);
      useUi.getState().setConnectProgress(assetId, null);
    }
  }, [storeTabId, pushToast]);

  const applyEncoding = async (v: string) => {
    if (!kernelTabId) {
      pushToast("error", "终端尚未连接，无法切换编码");
      return;
    }
    try {
      await terminalApi.switchEncoding(kernelTabId, v);
      encodingTouchedRef.current = true;
      setEncoding(v);
      pushToast("info", `编码已切换为 ${v}（仅影响后续输出）`);
    } catch (e) {
      pushToast("error", `编码切换失败：${describeError(e)}`);
    }
  };

  const pasteIntoTerminal = async () => {
    if (!kernelTabId) return;
    try {
      const text = await readText();
      if (text && (await confirmMultilinePaste(text, storeTabId, handleRef.current?.bracketedPasteMode() ?? false))) handleRef.current?.paste(text);
    } catch (e) {
      pushToast("error", `未能读取剪贴板：${describeError(e)}`);
    }
  };

  const handleSelectionCopy = (_text: string, error: unknown | null) => {
    if (error) {
      const name = (error as { name?: string } | null)?.name;
      const reason = name === "NotAllowedError" ? "剪贴板权限被拒绝" : describeError(error);
      pushToast("error", `复制选中内容失败：${reason}`);
      return;
    }
    pushToast("success", "已复制选中内容");
  };

  const disconnectSession = async () => {
    await disconnectSessionWithConfirm(sessionId, sessionName);
  };

  const reconnectSession = async () => {
    if (reconnectBusyRef.current) return;
    reconnectBusyRef.current = true;
    setReconnecting(true);
    try {
      await reconnectSessionOnce();
    } finally {
      reconnectBusyRef.current = false;
      setReconnecting(false);
    }
  };

  const reconnectSessionOnce = async () => {
    const workspace = useUi.getState().workspaces.find((item) =>
      item.panes.some((pane) => pane.tabs.some((tab) => tab.id === storeTabId)),
    );
    try {
      if (isSessionSuspect(sessionStatus, sessionLiveness) && !(await confirmSuspectReconnect(workspace?.id))) return;
      const result = await reconnectLiveSession(sessionId, workspace?.id);
      if (result.kind === "missing") {
        pushToast("error", "原连接已不存在。请再次选择「重新连接」以重新连接该主机");
        return;
      }
      if (result.kind === "pending") return;
      if (result.kind === "canceled") {
        pushToast("info", "已取消重新连接");
        return;
      }
      const current = useUi
        .getState()
        .workspaces.flatMap((w) => w.panes)
        .flatMap((p) => p.tabs)
        .find((t) => t.id === storeTabId);
      const resumeId = result.session.kind === "ssh" || result.session.kind === "device"
        ? current?.tabId
        : resumeAttemptedRef.current ? undefined : current?.tabId;
      resumeAttemptedRef.current = resumeId !== undefined;
      useUi.getState().updateTab(storeTabId, { tabId: resumeId });
      resumeRef.current = resumeId;
      setControl(null);
      setRemoteGrid(null);
      setForceRegular(false);
      setOrdinaryStarting(false);
      setKernelTabId(null);
      setEpoch((n) => n + 1);
    } catch (e) {
      if ((e as { code?: string } | null)?.code === "not_found") {
        pushToast("error", "原连接已不存在。请再次选择「重新连接」以重新连接该主机");
        return;
      }
      pushToast("error", `重新连接失败：${describeError(e)}。请检查网络后重试`);
    }
  };

  const runHelperAction = async (action: () => Promise<void>, generation?: number, label = "正在处理…") => {
    if (helperActionBusy) return;
    setHelperActionBusy(true);
    setHelperStatusLoading(false);
    setHelperActionLabel(label);
    try {
      await action();
    } catch (error) {
      if (helperDetailsSessionRef.current === sessionId && (generation === undefined || helperRequestIsCurrent(generation, sessionId))) {
        setHelperPanelError(describeError(error));
        pushToast("error", `持久终端组件操作失败：${describeError(error)}`);
      }
    } finally {
      if (helperDetailsSessionRef.current === sessionId && (generation === undefined || helperRequestIsCurrent(generation, sessionId))) {
        setHelperActionBusy(false);
      }
    }
  };

  const retryPersistent = () => runHelperAction(async () => {
    const operation = helperOperation;
    if (!operation) return;
    await useUi.getState().retryHelperOperation(storeTabId);
    if (operation.kind === "reattach" && operation.originalTabId) {
      resumeRef.current = operation.originalTabId;
      resumeAttemptedRef.current = true;
      setForceRegular(false);
      setOrdinaryStarting(false);
      setKernelTabId(null);
      setEpoch((value) => value + 1);
    }
    closeHelperPanel();
  });

  const continueOrdinary = () => runHelperAction(async () => {
    const operation = helperOperation;
    if (!operation) return;
    await useUi.getState().continueHelperOrdinary(storeTabId, false);
    if (operation.kind === "reattach" && operation.originalTabId) {
      resumeRef.current = undefined;
      resumeAttemptedRef.current = false;
      useUi.getState().updateTab(storeTabId, { tabId: undefined });
      setForceRegular(true);
      setOrdinaryStarting(true);
      setKernelTabId(null);
      setEpoch((value) => value + 1);
    }
    closeHelperPanel();
  });

  const cancelHelper = () => runHelperAction(async () => {
    await useUi.getState().cancelHelperOperation(storeTabId);
    closeHelperPanel();
  });

  const refreshHelperDetails = useCallback(async () => {
    setHelperPanelOpen(true);
    if (helperActionBusy) return;
    const generation = nextHelperRequestGeneration();
    setHelperPanelError(null);
    setHelperStatusLoading(true);
    try {
      const result = await terminalApi.helperOperation(sessionId, "", "status");
      if (!helperRequestIsCurrent(generation, sessionId)) return;
      setHelperDetails(result);
      setOfflinePath(null);
      setReplaceFailure(null);
    } catch (error) {
      if (!helperRequestIsCurrent(generation, sessionId)) return;
      const message = describeError(error);
      setHelperPanelError(message);
      pushToast("error", `未能读取持久终端组件状态：${message}`);
    } finally {
      if (helperRequestIsCurrent(generation, sessionId)) setHelperStatusLoading(false);
    }
  }, [sessionId, pushToast, helperActionBusy]);

  useEffect(() => {
    if (previousHelperSessionRef.current === sessionId) return;
    previousHelperSessionRef.current = sessionId;
    helperRequestGenerationRef.current += 1;
    useUi.getState().dismissHelperOperation(storeTabId);
    setHelperPanelOpen(false);
    setHelperActionBusy(false);
    setHelperStatusLoading(false);
    setHelperReady(false);
    setHelperDetails(null);
    setReplaceFailure(null);
    setHelperPanelError(null);
    setOfflinePath(null);
  }, [sessionId, storeTabId]);

  useEffect(() => {
    if (helperOperation?.status !== "succeeded") return;
    useUi.getState().dismissHelperOperation(storeTabId, helperOperation.operationId);
    setHelperPanelOpen(false);
    if (durable) return;
    // This tab started as a regular terminal while the helper deployed; only new tabs become durable.
    setHelperReady(true);
  }, [helperOperation?.operationId, helperOperation?.status, storeTabId, durable]);

  const readiness = useKnownDurableReadiness(sessionId);
  const hint = persistenceHint({
    durable,
    eligible: (sessionKind === "ssh" || sessionKind === "local") && !containerId && !effectiveWinrm,
    local: sessionKind === "local",
    operation: helperOperation && { status: helperOperation.status, kind: helperOperation.kind, message: helperOperation.reason?.message },
    ownedReady: helperReady,
    readiness,
    localHelper,
    regularReason,
  });
  const hintKey = hint ? persistenceHintKey(hint) : null;
  const dismissedHints = usePersistenceDismissals(storeTabId);
  useEffect(() => {
    const live = new Set(useUi.getState().workspaces.flatMap((ws) => ws.panes.flatMap((pane) => pane.tabs.map((tab) => tab.id))));
    prunePersistenceHints(live);
    return () => {
      // Dismissals last for the life of the tab; a remount of a live tab keeps them.
      if (!findStoreTab(useUi.getState().workspaces, storeTabId)) forgetPersistenceHints(storeTabId);
    };
  }, [storeTabId]);
  const hintShown = hint !== null && hintKey !== null && !dismissedHints.has(hintKey);
  const failureKey = hint && hintKey && !hintShown && persistenceHintFailure(hint) ? hintKey : undefined;
  const failureMark = hint && failureKey ? persistenceHintMessage(hint, sessionKind === "local") : undefined;
  const hintNote = hint ? persistenceHintNote(hint) : undefined;
  useEffect(() => {
    // The tab strip names the live state; a dismissed failure that still applies leaves a mark on the tab.
    publishPersistenceTab(storeTabId, { note: hintNote, failureMark, failureKey });
  }, [storeTabId, hintNote, failureMark, failureKey]);
  const hintShownRef = useRef(hintShown);
  hintShownRef.current = hintShown && visible !== false;

  useEffect(() => {
    if (helperOperation?.status !== "awaiting_choice") return;
    // The hint bar already explains a deployment that needs a choice when it is on screen.
    if (helperOperation.kind !== "reattach" && hintShownRef.current) return;
    // A toast instead of opening the dialog keeps focus with whatever the user is typing.
    pushToast(
      helperOperation.kind === "reattach" ? "error" : "info",
      helperOperation.kind === "reattach" ? "未能恢复原持久终端，请选择重试或新建临时终端" : "暂时无法使用持久终端，该标签页已改用临时终端",
      { label: "查看选项", onSelect: () => setHelperPanelOpen(true) },
    );
  }, [helperOperation?.operationId, helperOperation?.status, helperOperation?.kind, pushToast]);

  // A new durable terminal next to this one, in the same folder when it is known; this tab stays open.
  const openDurableTerminal = () => {
    const session = useUi.getState().sessions.find((item) => item.id === sessionId);
    if (session) void openTerminalTab(session, undefined, myPaneId ?? undefined, cwd ? { directory: cwd } : undefined);
  };
  const openHelper = () => {
    if (helperOperation) setHelperPanelOpen(true);
    else void refreshHelperDetails();
  };
  const openLocalHelperSettings = () => {
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
    showSettingsSection("settings-terminal");
  };
  // Only sessions served by a durable helper can reopen as durable or manage the helper.
  const durableHelper = sessionKind === "ssh" || (sessionKind === "local" && localDurableEnabled(localHelper));
  // A managed device's helper is managed from the same window: its agent bridges the consent the user gives here.
  const manageHelper = sessionKind === "device" ? openHelper : !durableHelper ? undefined : sessionKind === "local" ? openLocalHelperSettings : openHelper;

  const closeHelperPanel = () => setHelperPanelOpen(false);

  const importOfflineHelper = async () => {
    if (helperActionBusy) return;
    const requestSessionId = sessionId;
    const selected = await promptText("离线组件文件的本地路径（请先核对 SHA-256 与协议，确认前不会用于部署）", "");
    if (!selected?.trim() || helperDetailsSessionRef.current !== requestSessionId) return;
    const path = selected.trim();
    setHelperPanelOpen(true);
    const generation = nextHelperRequestGeneration();
    await runHelperAction(async () => {
      const result = await terminalApi.helperOffline(sessionId, path);
      if (!helperRequestIsCurrent(generation, sessionId)) return;
      setHelperDetails(result);
      setOfflinePath(path);
      setHelperPanelError(null);
    }, generation);
  };

  const confirmOfflineHelper = () => {
    if (helperActionBusy) return;
    const generation = nextHelperRequestGeneration();
    return runHelperAction(async () => {
      const offline = helperDetails?.offline;
      if (!offlinePath || !offline) return;
      const result = await terminalApi.helperOffline(sessionId, offlinePath, offline.sha256, true);
      if (!helperRequestIsCurrent(generation, sessionId)) return;
      setHelperDetails((current) => current ? { ...current, offline: result.offline } : result);
      if (result.offline?.trusted) {
        closeHelperPanel();
        pushToast("success", "已信任所选的离线组件文件，后续连接将使用它");
      }
    }, generation);
  };

  const updateHelper = (interruptActive: boolean) => {
    if (helperActionBusy) return;
    const generation = nextHelperRequestGeneration();
    return runHelperAction(async () => {
      try {
        if (interruptActive) {
          const confirmed = await ask("更新将结束该主机上所有持久终端中运行的进程，且无法恢复。是否中断并更新？", {
            title: "中断进程并更新", kind: "warning",
          });
          if (!confirmed || !helperRequestIsCurrent(generation, sessionId)) return;
        }
        const result = await terminalApi.helperUpdate(sessionId, interruptActive);
        if (!helperRequestIsCurrent(generation, sessionId)) return;
        setHelperDetails(result);
        setHelperPanelError(null);
        const status = result.update?.status;
        if (status === "current" || status === "updated" || (status === "interrupted" && result.update?.updated)) {
          setHelperPanelOpen(false);
          pushToast("success", status === "current" ? "持久终端组件已是最新版本" : "持久终端组件已更新");
        } else if (status === "deferred") {
          setHelperPanelOpen(false);
          pushToast("info", "当前有进程在运行，组件更新已延后", {
            label: "查看", onSelect: () => {
              if (helperRequestIsCurrent(generation, sessionId)) setHelperPanelOpen(true);
            },
          });
        } else {
          setHelperPanelError(result.update?.detail || "暂时无法确认更新结果，请重新检查组件状态");
        }
      } catch (error) {
        if (!helperRequestIsCurrent(generation, sessionId)) return;
        const rollback = rollbackResultFromError(error);
        if (!rollback) throw error;
        setHelperDetails(rollback);
        setHelperPanelError(describeError(error));
      }
    }, generation, "正在更新持久终端组件…");
  };

  // Ends the older helper the user agreed to in the helper window, with the sessions it showed.
  const replaceLegacyHelper = (consentToken: string) => {
    if (helperActionBusy) return;
    const generation = nextHelperRequestGeneration();
    setReplaceFailure(null);
    return runHelperAction(async () => {
      let result: HelperOperationResult;
      try {
        result = await terminalApi.helperReplaceLegacy(sessionId, consentToken);
      } catch (error) {
        const failure = replaceFailureFromError(error);
        if (!failure) throw error;
        if (!helperRequestIsCurrent(generation, sessionId)) return;
        // The window keeps the failure and its stage; the old helper's detail no longer applies.
        setReplaceFailure(failure);
        setHelperDetails((current) => current ? { ...current, legacy: null } : current);
        setHelperPanelError(null);
        return;
      }
      if (!helperRequestIsCurrent(generation, sessionId)) return;
      setHelperDetails(result);
      setHelperPanelError(null);
      if (result.update?.status === "replaced") {
        setHelperPanelOpen(false);
        const forced = result.update.stopped === true && result.update.forced ? "；旧版组件被强制结束，部分后台进程可能仍在运行" : "";
        pushToast(result.update.stopped !== true || result.update.forced ? "info" : "success", `${replacedText(result.update)}${forced}`);
      } else if (result.legacy?.stale) {
        setHelperPanelError("旧版组件的状态已变化，请核对最新的会话数后重新确认");
      }
    }, generation, "正在终止旧版组件并升级…");
  };

  const deferHelperUpdate = () => {
    setHelperDetails((current) => current?.update ? {
      ...current,
      update: { status: "deferred", detail: "已选择稍后更新" },
    } : current);
    closeHelperPanel();
    pushToast("info", "暂不更新，下次查看持久终端状态时仍会提示");
  };

  const startRecording = async () => {
    if (!kernelTabId) return;
    const name = `${title}-${Date.now()}.log`.replace(/[\\/:*?"<>|]/g, "_");
    const path = await pickSavePath(name);
    if (!path) return;
    try {
      await terminalApi.recordStart(kernelTabId, path);
    } catch (e) {
      pushToast("error", `无法开始录制：${describeError(e)}`);
    } finally {
      await useUi.getState().resyncRecording(kernelTabId);
    }
  };

  const stopRecording = async () => {
    if (!kernelTabId) return;
    try {
      await terminalApi.recordStop(kernelTabId);
    } catch (e) {
      pushToast("error", `无法停止录制：${describeError(e)}`);
    } finally {
      await useUi.getState().resyncRecording(kernelTabId);
    }
  };

  const saveLogToFile = async () => {
    if (!kernelTabId) return;
    const name = `${title}-${Date.now()}.log`.replace(/[\\/:*?"<>|]/g, "_");
    const path = await pickSavePath(name);
    if (!path) return;
    try {
      await terminalApi.exportLog(kernelTabId, path);
      const where = await finishSave(path, name);
      pushToast(
        where ? "success" : "info",
        where ? `已导出到 ${where}` : "已取消保存",
      );
    } catch (e) {
      pushToast("error", `导出失败：${describeError(e)}。请更换保存位置后重试`);
    }
  };

  const pasteSelectionBack = async () => {
    if (!kernelTabId) return;
    const selected = handleRef.current?.getSelection() ?? "";
    if (!selected) return;
    if (!(await confirmMultilinePaste(selected, storeTabId, handleRef.current?.bracketedPasteMode() ?? false))) return;
    handleRef.current?.paste(selected);
  };

  const handlePasteFiles = useCallback(
    async (files: File[], skippedEntries: string[] = []) => {
      // /tmp and the file channel only make sense for POSIX hosts: WinRM has no /tmp and a 48 KiB write limit,
      // and a container terminal would receive files on the host instead of in the container.
      if (effectiveWinrm || sessionKind === "docker" || containerId) {
        pushToast("info", "当前终端类型不支持粘贴上传文件，请在文件面板中上传");
        return;
      }
      if (!kernelTabId) return;
      const { accepted, skipped } = splitOversizedFiles(files);
      const notes: string[] = [];
      if (skipped.length > 0) {
        notes.push(
          `已跳过超过 ${MAX_PASTE_FILE_BYTES / 1024 / 1024} MB 的文件：${skipped.map((f) => displayFileName(f.name)).join("、")}`,
        );
      }
      if (skippedEntries.length > 0) notes.push(`已跳过无法上传的条目：${skippedEntries.join("、")}`);
      const skippedNote = notes.join("；");
      if (skippedNote) pushToast("info", skippedNote);
      if (accepted.length === 0) {
        setImagePaste({ phase: "error", message: skippedNote || "没有可上传的文件" });
        return;
      }
      // Hosts with Windows paths (a local session on a Windows desktop, OpenSSH on Windows) have no /tmp; the private
      // directory below is POSIX-only, so refuse instead of guessing a temp directory.
      let pathStyle: string | undefined;
      try {
        pathStyle = (await fsApi.roots(sessionId)).pathStyle;
      } catch {
        pathStyle = undefined;
      }
      if (pathStyle === "windows") {
        setImagePaste({ phase: "error", message: "该主机使用 Windows 路径，没有 /tmp；请改用文件面板上传" });
        return;
      }
      // The asset may map the upload directory (as the file channel sees it) to another path in the shell, for
      // chrooted SFTP accounts. Read it at paste time so an edited asset applies without reconnecting.
      let pasteDirs: PasteDirSettings = DEFAULT_PASTE_DIRS;
      if (sessionAssetId && typeof assetApi.get === "function") {
        let options: Record<string, unknown> | undefined;
        try {
          options = (await assetApi.get(sessionAssetId))?.options as Record<string, unknown> | undefined;
        } catch {
          options = undefined;
        }
        const resolved = pasteDirSettings(options);
        if (!resolved.ok) {
          setImagePaste({ phase: "error", message: `资产高级选项中的${resolved.error}；请在资产设置中修正` });
          return;
        }
        pasteDirs = resolved.settings;
      }
      const run: PasteUploadRun = { cancelled: false, transfer: null };
      pasteUploadRef.current = run;
      const totalBytes = accepted.reduce((sum, file) => sum + file.size, 0);
      const uploadingMessage = (done: number) =>
        `正在上传 ${accepted.length} 个文件到该主机的 ${pasteDirs.terminalDir}…${totalBytes > 0 ? ` ${Math.min(100, Math.floor((done / totalBytes) * 100))}%` : ""}`;
      setImagePaste({ phase: "uploading", message: uploadingMessage(0), cancellable: true });
      // An unguessable, exclusively created 0700 directory: nobody else on the host can pre-create, link or read it.
      const dir = joinPastePath(pasteDirs.uploadDir, `nexterm-paste-${crypto.randomUUID()}`);
      let dirCreated = false;
      try {
        await fsApi.mkdirPrivate(sessionId, dir);
        dirCreated = true;
        const names = uniquePasteFileNames(
          accepted.map((file) => {
            if (file.name) return file.name;
            return file.type.startsWith("image/") ? `image.${pasteFileExtension(file)}` : "file";
          }),
        );
        const links: string[] = [];
        let doneBytes = 0;
        for (const [index, file] of accepted.entries()) {
          if (run.cancelled) throw new PasteUploadCancelled();
          const name = names[index];
          const path = `${dir}/${name}`;
          const transfer = { taskId: createTransferTaskId(), attemptId: createTransferTaskId() };
          run.transfer = transfer;
          if (file.size <= UPLOAD_CHUNK_SIZE) {
            const bytes = new Uint8Array(await file.arrayBuffer());
            if (run.cancelled) throw new PasteUploadCancelled();
            await fsApi.write(sessionId, path, bytesToBase64(bytes), false, transfer.taskId, transfer.attemptId);
          } else {
            const before = doneBytes;
            await uploadChunked({
              sessionId,
              remotePath: path,
              source: fileChunkSource(file),
              ...transfer,
              onProgress: (transferred) => {
                if (pasteUploadRef.current === run && !run.cancelled) {
                  setImagePaste({ phase: "uploading", message: uploadingMessage(before + transferred), cancellable: true });
                }
              },
            });
          }
          run.transfer = null;
          doneBytes += file.size;
          if (pasteUploadRef.current === run && !run.cancelled) {
            setImagePaste({ phase: "uploading", message: uploadingMessage(doneBytes), cancellable: true });
          }
          links.push(markdownFileLink(name, terminalPastePath(pasteDirs, path)));
        }
        if (run.cancelled) throw new PasteUploadCancelled();
        const text = links.join(" ");
        // Last line of defence: the link goes straight to the shell, so no control byte may reach it.
        if (TERMINAL_CONTROL.test(text)) throw new Error("生成的链接包含控制字符，已拒绝插入");
        handleRef.current?.paste(text);
        setImagePaste({
          phase: "success",
          message: `已上传 ${links.length} 个文件到该主机的 ${pasteDirs.terminalDir} 并插入链接${skippedNote ? `；${skippedNote}` : ""}`,
        });
      } catch (e) {
        let cleanupNote = "";
        if (dirCreated) {
          try {
            await fsApi.delete(sessionId, dir, true);
          } catch {
            cleanupNote = `；未能清理 ${dir}，请手动删除`;
          }
        }
        const message =
          e instanceof PasteUploadCancelled || run.cancelled
            ? `已取消上传${cleanupNote}`
            : (e as { code?: unknown } | null)?.code === "not_found"
              ? pasteDirs.custom
                ? `文件上传到 ${pasteDirs.uploadDir} 失败：该主机的 SFTP 中没有该目录，请检查资产高级选项中的粘贴上传目录${cleanupNote}`
                : // The SFTP view has no /tmp, typically a chrooted SFTP account.
                  `文件上传到 /tmp 失败：该主机的 SFTP 中没有 /tmp（常见于受限的 chroot SFTP 账号），请改用文件面板上传到可写目录，或在资产高级选项中设置粘贴上传目录${cleanupNote}`
              : `文件上传到 ${pasteDirs.uploadDir} 失败：${describeError(e)}${cleanupNote}`;
        setImagePaste({ phase: "error", message });
      } finally {
        if (pasteUploadRef.current === run) pasteUploadRef.current = null;
      }
    },
    [kernelTabId, pushToast, sessionId, sessionKind, sessionAssetId, containerId, effectiveWinrm],
  );

  const cancelPasteUpload = () => {
    const run = pasteUploadRef.current;
    if (!run) return;
    run.cancelled = true;
    setImagePaste({ phase: "uploading", message: "正在取消上传…" });
    if (run.transfer) void fsApi.cancel(run.transfer.taskId, run.transfer.attemptId).catch(() => undefined);
  };

  const pasteImagesFromClipboard = async () => {
    if (!kernelTabId) return;
    const pasteHint = `请在终端内按 ${modHint()}+V 粘贴文件，或将文件拖入终端`;
    if (typeof navigator.clipboard?.read !== "function") {
      handleRef.current?.focus();
      pushToast("info", `当前环境不支持直接读取剪贴板图片；${pasteHint}`);
      return;
    }
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
      const name = (e as { name?: string } | null)?.name;
      if (name === "NotAllowedError" || name === "SecurityError") {
        handleRef.current?.focus();
        pushToast("info", `当前环境未允许直接读取剪贴板图片；${pasteHint}`);
      } else {
        pushToast("error", `未能读取剪贴板图片：${describeError(e)}`);
      }
      return;
    }
    if (files.length === 0) {
      pushToast("info", "剪贴板中没有图片：请先截图或复制图片文件，再在终端中按 Ctrl+V 或拖入");
      return;
    }
    await handlePasteFiles(files);
  };

  useEffect(() => {
    // A finished upload's outcome is transient feedback; only an upload in progress stays on screen.
    if (!imagePaste || imagePaste.phase === "uploading") return;
    const timer = window.setTimeout(() => setImagePaste(null), imagePaste.phase === "error" ? 8000 : 4500);
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
    setBlocksOpen(!blocksOpen);
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
    ...(blocksSupported
      ? ([
          { kind: "separator" },
          { kind: "group", label: "命令列表" },
          {
            kind: "item",
            label: "上一条命令",
            icon: <IconArrowUp size={13} />,
            disabled: !blocks.length,
            onSelect: () => handleRef.current?.navigateBlock("prev"),
          },
          {
            kind: "item",
            label: "下一条命令",
            icon: <IconArrowDown size={13} />,
            disabled: !blocks.length,
            onSelect: () => handleRef.current?.navigateBlock("next"),
          },
          {
            kind: "item",
            label: "命令列表",
            icon: <IconList size={13} />,
            hint: blocks.length > 0 ? `${blocks.length} 条` : "折叠输出 / 复制 / 定位",
            onSelect: toggleBlocksPanel,
          },
        ] satisfies MenuItem[])
      : []),
    { kind: "separator" },
    { kind: "group", label: "命令广播" },
    {
      kind: "item",
      label: "广播到当前分屏",
      icon: <IconZap size={13} />,
      hint: "同面板全部终端",
      onSelect: () => void enableBroadcastScope("pane"),
    },
    {
      kind: "item",
      label: "广播到全部终端",
      hint: "全部已打开工作区",
      onSelect: () => void enableBroadcastScope("all"),
    },
    {
      kind: "item",
      label: "选择广播目标…",
      hint: broadcastCount > 0 ? `已选 ${broadcastCount} 个` : "逐个勾选",
      onSelect: () => setBroadcastPickerOpen(true),
    },
    ...(broadcastCount > 0
      ? ([
          {
            kind: "item",
            label: "关闭广播",
            danger: true,
            onSelect: closeBroadcast,
          },
        ] satisfies MenuItem[])
      : []),
    { kind: "separator" },
    { kind: "group", label: "录制" },
    {
      kind: "item",
      label: recording?.state === "running" ? "停止录制" : recording?.state === "failed" ? "重新开始录制…" : "开始录制…",
      icon: <IconSave size={13} />,
      hint: recording?.state === "running" ? "停止并保存当前录制" : "录制后续终端输出",
      disabled: !kernelTabId,
      onSelect: () => void (recording?.state === "running" ? stopRecording() : startRecording()),
    },
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
    ...(sessionKind === "ssh"
      ? ([
          { kind: "separator" },
          {
            kind: "item",
            label: "持久终端组件…",
            onSelect: () => void refreshHelperDetails(),
          },
          ...(!WEB
            ? ([{ kind: "item", label: "导入离线组件文件…", onSelect: () => void importOfflineHelper() }] satisfies MenuItem[])
            : []),
        ] satisfies MenuItem[])
      : []),
  ];

  const openToolbarOverflow = (e: ReactMouseEvent<HTMLButtonElement>) => {
    e.stopPropagation();
    const r = e.currentTarget.getBoundingClientRect();
    setMenu({ x: r.left, y: r.bottom, title: "终端操作", items: buildToolbarOverflowMenu() });
  };

  // Link actions for the terminal menu: right-click on a hovered link, or a
  // touch long-press on one (touch has no modifier for opening).
  const buildLinkMenu = (uri: string): MenuItem[] => [
    { kind: "group", label: "链接" },
    {
      kind: "item",
      label: "打开链接",
      onSelect: () => handleRef.current?.openLink(uri),
    },
    {
      kind: "item",
      label: "复制链接",
      icon: <IconCopy size={13} />,
      onSelect: () =>
        void copyText(cleanTerminalLink(uri)).then(
          () => pushToast("success", "已复制链接"),
          (e: unknown) => pushToast("error", `复制链接失败：${describeError(e)}`),
        ),
    },
  ];

  const openLinkMenu = (request: TerminalLinkMenuRequest) => {
    setMenu({ x: request.x, y: request.y, title: displayTerminalLink(request.uri), items: buildLinkMenu(request.uri) });
  };

  const buildTerminalMenu = (): MenuItem[] => {
    const selected = (handleRef.current?.getSelection() ?? "").trim();
    const hasSel = selected.length > 0;
    const firstLine = selected.split("\n")[0];
    const st = useUi.getState();
    const ws = st.workspaces.find((w) =>
      w.panes.some((p) => p.tabs.some((t) => t.id === storeTabId)),
    );
    const pane = ws?.panes.find((p) => p.tabs.some((t) => t.id === storeTabId));
    const isSplit = (ws?.panes.length ?? 1) > 1;
    const staleSession = sessionStatus === undefined || isStoreTabDead(storeTabId);

    return [
      { kind: "group", label: "操作" },
      {
        kind: "item",
        label: "复制",
        icon: <IconCopy size={13} />,
        disabled: !hasSel,
        onSelect: () => void copyText(selected).catch(() => undefined),
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
        hint: "上传到该主机的 /tmp，插入链接",
        disabled: !kernelTabId,
        onSelect: () => void pasteImagesFromClipboard(),
      },
      {
        kind: "item",
        label: "粘贴选中文本",
        icon: <IconEdit size={13} />,
        hint: "不修改剪贴板",
        disabled: !hasSel || !kernelTabId,
        onSelect: () => void pasteSelectionBack(),
      },
      {
        kind: "item",
        label: "搜索选中内容的首行",
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
        label: recording?.state === "running" ? "停止录制" : recording?.state === "failed" ? "重新开始录制…" : "开始录制…",
        icon: <IconSave size={13} />,
        hint: recording?.state === "running" ? "停止并保存当前录制" : "录制后续终端输出",
        disabled: !kernelTabId,
        onSelect: () => void (recording?.state === "running" ? stopRecording() : startRecording()),
      },
      {
        kind: "item",
        label: "导出当前输出…",
        icon: <IconSave size={13} />,
        hint: "屏幕与滚动历史",
        disabled: !kernelTabId,
        onSelect: () => void saveLogToFile(),
      },
      {
        kind: "item",
        label: "重新连接",
        icon: <IconRefresh size={13} />,
        hint: staleSession ? "连接已失效，该标签页将换成新终端" : reconnecting ? "正在重新连接…" : statusText,
        disabled: reconnecting || (!staleSession && !canReconnect),
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
      ...([
        { direction: "right", label: "向右分屏", icon: <IconSplitV size={13} />, binding: bindings.splitRight },
        { direction: "down", label: "向下分屏", icon: <IconSplitH size={13} />, binding: bindings.splitDown },
      ] as const).map(({ direction, label, icon, binding }): MenuItem => {
        const tooShort = direction === "down" && !splitAllowedForHeight(window.innerHeight);
        return {
          kind: "item",
          label,
          icon,
          accel: formatBinding(binding),
          disabled: !ws || !pane || tooShort,
          hint: tooShort ? "窗口高度不足" : undefined,
          onSelect: () => {
            if (ws && pane) useUi.getState().splitPane(direction, pane.id, ws.id);
          },
        };
      }),
      {
        kind: "item",
        label: "关闭窗格（保留标签页）",
        icon: <IconMergeH size={13} />,
        disabled: !isSplit || !pane,
        accel: formatBinding(bindings.closePane),
        hint: isSplit ? "标签页移到相邻窗格" : undefined,
        onSelect: () => {
          if (ws && pane) useUi.getState().closePane(pane.id, ws.id);
        },
      },
    ];
  };

  const openTerminalMenu = (e: ReactMouseEvent<HTMLDivElement>) => {
    e.preventDefault();
    const selected = (handleRef.current?.getSelection() ?? "").trim();
    const link = handleRef.current?.hoveredLink?.() ?? null;
    setMenu({
      x: e.clientX,
      y: e.clientY,
      title: link ? displayTerminalLink(link) : selected ? clipText(selected.split("\n")[0], 80) : title,
      items: link ? [...buildLinkMenu(link), { kind: "separator" }, ...buildTerminalMenu()] : buildTerminalMenu(),
    });
  };

  return (
    <div ref={paneRef} className="nx-pane bg-term @container/term">
      <div className="nx-toolbar nx-term-toolbar">
        <span className="nx-toolbar-title" title={title}>{title}</span>
        <span
          className={`nx-badge ${
            sessionStatus === "connected"
              ? sessionLiveness === "suspect" ? "nx-badge-amber" : "nx-badge-green"
              : sessionStatus === "failed"
                ? "nx-badge-red"
                : ""
          }`}
          title={
            showConnectProgress && connectProgress
              ? `${connectProgress.host}:${connectProgress.port}`
              : statusText
          }
          aria-label={`连接状态：${statusText}`}
        >
          <span className="nx-dot" />
          <span className="nx-term-status-text">
            {showConnectProgress && connectProgress
              ? `${statusText} · ${connectProgressText(connectProgress)}`
              : statusText}
          </span>
        </span>
        <div className="nx-toolbar-extra">
        {staleConfig && (
          <>
            <span className="nx-badge nx-badge-amber" role="status">
              资产配置已修改，重新连接后生效
            </span>
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm"
              disabled={!canReconnect || reconnecting}
              title="使用新的资产配置重新连接，保留当前终端"
              onClick={() => void reconnectSession()}
            >
              重新连接
            </button>
          </>
        )}
        {effectiveWinrm && (
          <span
            className="nx-badge nx-badge-amber"
            title="WinRM 非交互模式：每条命令在新的 Shell 中执行，不支持 vim/top 等交互界面"
          >
            非交互模式
          </span>
        )}
        {containerId && (
          <span
            className={`nx-badge ${execInterrupted ? "nx-badge-amber" : "nx-badge-purple"}`}
            title="在容器内执行命令的终端（docker exec）：关闭标签页将结束该进程，不能转入后台"
          >
            {execInterrupted ? "容器命令已结束" : "容器内命令"}
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
        {throttleNow?.resyncing && (
          <span className="nx-badge" role="status" title="输出速度较快，画面将自动同步，无需操作">
            正在同步输出
          </span>
        )}
        {throttleNow?.recovered && (
          <span className="nx-badge nx-badge-green" title="终端输出积压已消退，画面恢复实时">
            输出已恢复
          </span>
        )}
        {streamBacklog && (
          <span
            className="nx-badge nx-badge-amber"
            title={`终端输出解析积压（${streamBacklog.pendingBytes} 字节待解析），画面可能有延迟`}
          >
            解析积压
          </span>
        )}
        {recording?.state === "running" && (
          <span className="nx-badge nx-badge-red" title={recording.path || "正在录制"}>
            <span className="nx-dot nx-dot-pulse" />
            录制中
          </span>
        )}
        <Cwd cwd={cwd} />
        {previewShareEnabled && kernelTabId && <PreviewShareMenu tabId={kernelTabId} sessionName={title} />}
        </div>
        <div className="nx-spacer" />

        <button
          className={`nx-btn nx-btn-ghost nx-btn-sm @max-[560px]:hidden ${searchOpen ? "bg-neutral-800 text-neutral-100" : ""}`}
          title={`搜索终端内容 (${searchBindingLabel})`}
          aria-pressed={searchOpen}
          onClick={() => setSearchOpen((v) => !v)}
        >
          <IconSearch size={13} />
          搜索
        </button>

        {encoding !== "utf-8" && (
          <span
            className="nx-badge nx-badge-amber font-mono @max-[560px]:order-1"
            title="当前终端字符编码（对之后的输出生效），在「更多终端操作」菜单中切换"
          >
            {encoding}
          </span>
        )}

        {blocksSupported && (
          <>
            <span className="nx-divider-v @max-[560px]:hidden" />
            <button
              className="nx-icon-btn nx-icon-btn-sm @max-[560px]:hidden"
              disabled={!blocks.length}
              aria-label="上一条命令"
              title="上一条命令"
              onClick={() => handleRef.current?.navigateBlock("prev")}
            >
              <IconArrowUp size={13} />
            </button>
            <button
              className="nx-icon-btn nx-icon-btn-sm @max-[560px]:hidden"
              disabled={!blocks.length}
              aria-label="下一条命令"
              title="下一条命令"
              onClick={() => handleRef.current?.navigateBlock("next")}
            >
              <IconArrowDown size={13} />
            </button>
            <button
              className={`nx-btn nx-btn-sm @max-[560px]:hidden ${blocksOpen ? "nx-btn-ghost bg-neutral-800 text-neutral-100" : "nx-btn-ghost"}`}
              title="命令列表：按命令查看、复制或定位输出"
              onClick={toggleBlocksPanel}
            >
              <IconList size={13} />
              命令列表
              {blocks.length > 0 && <span className="nx-count">{blocks.length}</span>}
            </button>
          </>
        )}
        {broadcastCount > 0 && (
          <button
            type="button"
            className="nx-badge nx-badge-red shrink-0 cursor-pointer @max-[560px]:order-1"
            title={`命令广播已开启：目标共 ${broadcastCount} 个，不可写目标会跳过；单击可关闭广播`}
            aria-label="关闭命令广播"
            onClick={closeBroadcast}
          >
            <span className="nx-dot nx-dot-pulse" />
            广播 {broadcastCount}
          </button>
        )}
        <button
          className="nx-icon-btn nx-icon-btn-sm nx-term-more"
          title="更多终端操作（搜索 / 编码 / 命令列表）"
          aria-label="更多终端操作"
          onClick={openToolbarOverflow}
        >
          ⋯
        </button>
      </div>

      {containerId && execInterrupted && (
        <div role="alert" className="flex shrink-0 flex-wrap items-center gap-2 border-b border-amber-800/50 bg-amber-950/30 px-2.5 py-1.5 text-xs text-amber-300">
          <span>容器内的命令已中断，原进程和输入无法恢复。重新打开将启动新的命令。</span>
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm"
            disabled={sessionStatus !== "connected" || !execTargetMatches}
            onClick={restartExec}
          >
            {sessionStatus === "connected" ? "重新打开" : "等待主机重新连接"}
          </button>
          {!execTargetMatches && <span>原主机已改变，请从容器列表重新打开。</span>}
        </div>
      )}

      {recording?.state === "failed" && (
        <div role="alert" className="flex shrink-0 flex-wrap items-center gap-2 border-b border-red-900/50 bg-red-950/30 px-2.5 py-1.5 text-xs text-red-200">
          <span>录制失败：{recording.safeError || "原因未知"}</span>
          {recording.path && <span className="font-mono break-all">路径：{recording.path}</span>}
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-sm"
            title="复制录制错误"
            onClick={() => void copyText(recording.safeError || "未知错误").catch(() => undefined)}
          >
            复制错误
          </button>
          {recording.path && (
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm"
              title="复制录制路径"
              onClick={() => void copyText(recording.path || "").catch(() => undefined)}
            >
              复制路径
            </button>
          )}
        </div>
      )}

      {productionClassification?.production && (
        <div className="shrink-0 px-2 pt-1.5" data-testid="terminal-production-warning">
          <ProductionWriteRiskNotice
            environment={productionClassification.environment}
            production
            operation="终端"
          />
        </div>
      )}

      {workspaceId && (
        <BroadcastStrip
          workspaceId={workspaceId}
          myStoreTabId={storeTabId}
          onManage={() => setBroadcastPickerOpen(true)}
        />
      )}

      {searchOpen && (
        <div className="flex shrink-0 items-center gap-1.5 border-b border-neutral-800/60 bg-neutral-900/70 px-2.5 py-1.5">
          <div className="nx-field max-w-[260px] @max-[560px]:max-w-none @max-[560px]:flex-1">
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
            className="nx-icon-btn"
            aria-label="上一个匹配"
            title="上一个"
            onClick={() => searchApi.current?.findPrevious(query)}
          >
            <IconArrowUp size={13} />
          </button>
          <button
            className="nx-icon-btn"
            aria-label="下一个匹配"
            title="下一个"
            onClick={() => searchApi.current?.findNext(query)}
          >
            <IconArrowDown size={13} />
          </button>
          <div className="nx-spacer @max-[560px]:hidden" />
          <button className="nx-icon-btn" aria-label="关闭搜索" title="关闭搜索" onClick={() => setSearchOpen(false)}>
            <IconClose size={12} />
          </button>
        </div>
      )}

      {hintShown && (
        <PersistenceHintBar
          hint={hint}
          local={sessionKind === "local"}
          onOpenDurable={durableHelper ? openDurableTerminal : undefined}
          onManageHelper={manageHelper}
          onDismiss={() => dismissPersistenceHint(storeTabId, hintKey)}
        />
      )}

      <div className="nx-terminal-body relative flex min-h-0 flex-1">
        <div className="relative min-h-0 min-w-0 flex-1" onContextMenu={openTerminalMenu}>
          <div className="h-full w-full p-1.5">
            <XtermView
              key={epoch}
              sessionId={sessionId}
              tabId={kernelTabId ?? "pending"}
              viewTabId={storeTabId}
              winrm={effectiveWinrm}
              containerId={containerId}
              resumeTabId={resumeRef.current}
              forceRegular={forceRegular}
              requestRegular={regularReason !== undefined}
              sessionConnected={sessionStatus === "connected"}
              canResize={canResize}
              remoteGrid={remoteGrid ?? undefined}
              followerZoom={followerZoom}
              onFollowerScale={setFollowerScale}
              onFollowerPinch={(direction) => setFollowerZoom(direction === "out" ? "height" : "fit")}
              visible={visible}
              onData={(d) => void sendData(d)}
              onAttachFailed={handleAttachDead}
              onAttachInfo={(info) => {
                setThrottle(null);
                setStreamBacklog(null);
                setControl({
                  sizeOwner: info.sizeOwner,
                  subscribers: info.subscribers,
                  viewers: info.viewers,
                  exited: info.exited,
                });
                useUi.getState().updateTab(storeTabId, {
                  shell: info.shell ?? "unknown",
                  retainable: info.retainable === true,
                  ...(info.durable === undefined ? {} : { durable: info.durable }),
                  exited: info.exited,
                  dead: false,
                  closeError: undefined,
                });
                if (info.durable !== undefined) setDurable(info.durable);
                if (
                  info.durable === false && (sessionKind === "ssh" || sessionKind === "local") && !forceRegular && !containerId && !effectiveWinrm &&
                  !findStoreTab(useUi.getState().workspaces, storeTabId)?.regularReason
                ) {
                  // Name why this SSH shell is regular; the backend reports its own fallback only once per connection.
                  void fallbackTerminalReason(sessionId, {
                    fallbackNotice: Boolean(info.fallbackNotice),
                    durableRequested: info.durableRequested,
                    kind: sessionKind,
                  }).then((reason) => {
                    const tab = findStoreTab(useUi.getState().workspaces, storeTabId);
                    if (reason && tab && tab.durable !== true && !tab.regularReason) {
                      useUi.getState().updateTab(storeTabId, { regularReason: reason });
                    }
                  });
                }
                if (info.encoding) setEncoding(info.encoding);
                const titleSync = reconcileBackendTabTitle(
                  storeTabId,
                  info,
                  containerId ? null : (id, title, at) => terminalApi.renameTab(id, title, at),
                );
                // The local custom title stays on the tab strip either way; a failed write-back only
                // leaves other devices and AI tools on the backend's program title until the next attach.
                void titleSync?.catch((error: unknown) => console.warn("terminal title sync failed", error));
              }}
              onAttach={(id) => {
                if (containerId) execAttachedSessionRef.current = sessionId;
                const operation = useUi.getState().helperOperations.operations[storeTabId];
                if (operation?.kind === "reattach") {
                  useUi.getState().dismissHelperOperation(storeTabId, operation.operationId);
                }
                setForceRegular(false);
                setOrdinaryStarting(false);
                setKernelTabId(id);
                setAttachDead(false);
                setThrottle(null);
                setStreamBacklog(null);
                resumeAttemptedRef.current = false;
                useUi.getState().updateTab(storeTabId, { tabId: id, dead: false, closeError: undefined });
                void useUi.getState().resyncSessions({ silent: true });
                const pending = takePendingDirectory(storeTabId);
                if (pending) {
                  const tab = useUi
                    .getState()
                    .workspaces.flatMap((workspace) => workspace.panes)
                    .flatMap((pane) => pane.tabs)
                    .find((candidate) => candidate.id === storeTabId);
                  const { command, failure } = directoryCommandFor(pending, tab?.shell ?? "unknown");
                  if (command) {
                    void terminalApi
                      .write(id, new TextEncoder().encode(`${command}\n`))
                      .catch((e) => pushToast("error", `切换目录失败：${describeError(e)}`));
                  } else {
                    const reason =
                      failure === "control"
                        ? "目录包含控制字符"
                        : failure === "unsupported"
                          ? "当前终端 Shell 暂不支持自动切换目录"
                          : "无法确认当前终端 Shell";
                    void copyText(pending)
                      .then(() => {
                        pushToast(
                          failure === "control" ? "error" : "info",
                          `${reason}，未发送命令；路径已复制，请手动切换`,
                        );
                      })
                      .catch((e) => {
                        pushToast(
                          "error",
                          `${reason}，未发送命令；复制路径失败：${describeError(e)}。路径：${pending}`,
                        );
                      });
                  }
                }
                void refreshControl(id);
              }}
              onBlocks={blocksSupported ? setBlocks : undefined}
              onTitle={handleOscTitle}
              onClipboard={osc52Handler}
              onStreamBacklog={setStreamBacklog}
              onChannel={(id) => {
                outputChannelRef.current = id;
              }}
              onPasteFiles={(files, skipped) => void handlePasteFiles(files, skipped)}
              onHandle={(h) => {
                handleRef.current = h;
              }}
              onSelectionCopy={handleSelectionCopy}
              onRequestPaste={() => void pasteIntoTerminal()}
              onTouchTap={() => takeTapForComposer(composerRef.current)}
              onLinkMenu={openLinkMenu}
              registerSearch={(api) => {
                searchApi.current = api;
              }}
            />
          </div>

          {/* Overlays at the top of the screen: taking layout height would resize the PTY. */}
          {controlSupported && !control?.exited && (
            <div className="pointer-events-none absolute inset-x-0 top-0 z-10 flex flex-col items-end gap-1 px-3 pt-1.5">
              {!canResize && (
                <FollowerSizeBadge
                  scale={followerScale}
                  ownedElsewhere={control?.sizeOwner != null}
                  zoom={followerZoom}
                  onZoom={setFollowerZoom}
                />
              )}
            </div>
          )}

          {durable && (
            <EarlierHistory
              viewKey={storeTabId}
              tabId={kernelTabId}
              info={historyInfo}
              dimensions={() => handleRef.current?.dimensions()}
              onClosed={() => handleRef.current?.focus()}
            />
          )}

          {switching && !attachDead && !control?.exited && (
            <div
              role="status"
              className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center bg-neutral-950/45 px-6 text-center backdrop-blur-[1px]"
            >
              <span className="rounded-md border border-blue-400/30 bg-neutral-950/75 px-3 py-2 text-xs text-blue-100 shadow-lg">
                正在切换终端…
              </span>
            </div>
          )}

          {controlSupported && control?.exited && (
            <div
              role="alert"
              className="pointer-events-none absolute inset-0 z-10 flex flex-col items-center justify-center gap-2.5 bg-neutral-950/70 px-6 text-center backdrop-blur-[1px]"
            >
              <span className="flex items-center gap-1.5 text-[13px] font-semibold text-neutral-100">
                <IconEye size={15} className="nx-term-overlay-warn text-amber-300" />
                终端进程已结束
              </span>
              <span className="max-w-[440px] text-[11.5px] leading-relaxed text-neutral-200">
                只能查看最后的终端输出。请新建终端继续操作
              </span>
              <button
                ref={exitedCloseBtnRef}
                className="nx-btn nx-btn-ghost nx-btn-sm pointer-events-auto"
                onClick={() => void requestCloseTab(storeTabId)}
              >
                <IconClose size={13} />
                关闭标签页
              </button>
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
              {imagePaste.phase === "uploading" && (
                <IconRefresh size={13} className="shrink-0 animate-spin" />
              )}
              <span className="min-w-0 break-all">{imagePaste.message}</span>
              {imagePaste.phase === "uploading" && imagePaste.cancellable && (
                <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm shrink-0" onClick={cancelPasteUpload}>
                  取消
                </button>
              )}
              <button
                type="button"
                className="nx-icon-btn nx-icon-btn-sm shrink-0"
                aria-label="关闭文件粘贴提示"
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
                <IconRefresh size={15} className="nx-term-overlay-warn text-amber-300" />
                该终端已失效
              </span>
              <span className="max-w-[440px] text-[11.5px] leading-relaxed text-neutral-200">
                {ordinaryStarting
                  ? "正在新建临时终端。原持久终端未恢复，其中的进程状态未知；完成后该标签页将换成新终端"
                  : "该终端所属的连接已不存在。重新连接将先尝试恢复原终端；无法恢复时，可选择新建临时终端。此前的输出可在「终端历史」中查看"}
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
                  {reconnecting ? "正在重新连接…" : "重新连接"}
                </button>
                <button
                  className="nx-btn nx-btn-ghost nx-btn-sm"
                  onClick={() => void requestCloseTab(storeTabId)}
                >
                  <IconClose size={13} />
                  关闭标签页
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

      {helperPanelOpen && visible !== false && (
          <HelperOperationDialog
            onClose={closeHelperPanel}
            stage={helperOperation?.stage ?? (helperPanelError ? "failed" : "ready")}
            reason={[helperOperation?.reason?.message, helperOperation?.cleanupFailure?.message, helperPanelError]
              .filter((value): value is string => Boolean(value))
              .join("；") || null}
            operationId={helperOperation?.operationId}
            attempt={helperOperation?.attempt ?? null}
            maxAttempts={helperOperation?.maxAttempts ?? null}
            deadline={helperOperation ? new Date(helperOperation.deadline).toISOString() : null}
            path={helperOperation?.path}
            transport={helperOperation?.transport}
            localVersion={helperDetails?.local}
            runningVersion={helperDetails?.running}
            compatibility={helperDetails?.compatibility}
            offlineTrust={helperDetails?.offline ?? null}
            update={helperDetails?.update ?? null}
            legacy={helperDetails?.legacy ?? null}
            replaceFailure={replaceFailure}
            onReplaceLegacy={helperDetails?.legacy?.consentToken
              ? () => void replaceLegacyHelper(helperDetails.legacy?.consentToken ?? "")
              : undefined}
            restoreFailure={helperOperation?.kind === "reattach" && helperOperation.originalSession === "not_restored"}
            busyLabel={helperActionBusy ? helperActionLabel : helperStatusLoading ? "正在检查持久终端组件…" : helperRetryPending ? "正在重试…" : ordinaryStarting ? "正在打开临时终端…" : undefined}
            controlsDisabled={helperActionBusy || helperRetryPending || ordinaryStarting}
            onRetryPersistent={helperOperation?.status === "awaiting_choice" ? () => void retryPersistent() : undefined}
            onContinueOrdinary={helperOperation?.status === "awaiting_choice" ? () => void continueOrdinary() : undefined}
            onCancel={helperOperation?.status === "running" || helperOperation?.status === "awaiting_choice"
              ? () => void cancelHelper()
              : undefined}
            onConfirmOfflineTrust={offlinePath && helperDetails?.offline && !helperDetails.offline.trusted
              ? () => void confirmOfflineHelper()
              : undefined}
            onUpdate={helperDetails?.update?.status === "available" ? () => void updateHelper(false) : undefined}
            onDeferUpdate={helperDetails?.update && ["available", "deferred"].includes(helperDetails.update.status) ? deferHelperUpdate : undefined}
            onInterruptAndUpdate={helperDetails?.update && ["available", "deferred"].includes(helperDetails.update.status)
              ? () => void updateHelper(true)
              : undefined}
          />
      )}

      <TerminalKeysBar
        onSend={(data) => void sendData(data)}
        onFocus={() => handleRef.current?.focus()}
        onSendText={(text, submit) => {
          const handle = handleRef.current;
          if (text && handle) handle.paste(text, submit);
          else if (text || submit) void sendData(`${text}${submit ? "\r" : ""}`);
        }}
        composerRef={composerRef}
        broadcastCount={memberBroadcastCount}
      />
      <ContextMenu state={menu} onClose={() => setMenu(null)} />
      {broadcastPickerOpen && workspaceId && myPaneId && (
        <BroadcastPickerModal
          workspaceId={workspaceId}
          myPaneId={myPaneId}
          myStoreTabId={storeTabId}
          onClose={() => setBroadcastPickerOpen(false)}
        />
      )}
      {broadcastConfirmation && (
        <ProductionConfirmationDialog
          key={`${broadcastConfirmation.sequence}:${broadcastConfirmation.challenge.targetSetHash}:${broadcastConfirmation.challenge.inputHash}`}
          challenge={broadcastConfirmation.challenge}
          contentHash={broadcastConfirmation.challenge.inputHash}
          command={broadcastConfirmation.data}
          variant="broadcast"
          onCancel={() => clearBroadcastConfirmations(true)}
          onConfirm={confirmBroadcastProduction}
        />
      )}
    </div>
  );
}

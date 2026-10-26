import { sessionApi, terminalApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { createHelperOperationState } from "../../features/terminal/helperOperationState";
import { advanceWorkspaceProblemResetGeneration } from "./problems";
import { nextWorkspaceGeneration } from "./runtimeSlice";
import { abnormalDisconnectNotices, sessionStatusEvidenceMap } from "./sessionStatus";
import type { UiGet, UiSet, UiState, WorkspaceRuntime } from "./types";

let recordingRequestSeq = 0;
let recordingsEpoch = 0;

const recordingRequests = new Map<string, number>();

// Auth guard for the session list: when login/logout switches accounts, in-flight old requests may arrive late and must not write back over the new session data.
let sessionsEpoch = 0;
let sessionsRequestSeq = 0;

export function createSessionSlice(set: UiSet, get: UiGet) {
  return {
    sessions: [],
    recordings: {},
    setRecordingStatus: (status) =>
      set((state) => {
        const current = state.recordings[status.tabId];
        if (current && current.generation > status.generation) return state;
        if (status.state === "never-started") {
          const { [status.tabId]: _, ...recordings } = state.recordings;
          return { recordings };
        }
        return { recordings: { ...state.recordings, [status.tabId]: status } };
      }),
    resyncRecording: async (tabId) => {
      const request = ++recordingRequestSeq;
      recordingRequests.set(tabId, request);
      const epoch = recordingsEpoch;
      try {
        const status = await terminalApi.recordingStatus(tabId);
        if (epoch !== recordingsEpoch || recordingRequests.get(tabId) !== request) return;
        get().setRecordingStatus(status);
      } catch {
      }
    },
    setSessions: (s) => set({ sessions: s }),
    sessionResyncFailed: false,
    sessionResyncCompleted: false,
    layoutSyncCompleted: false,
    workspaceRestoreDismissed: false,
    workspaceRestoreResults: {},
    dismissWorkspaceRestoreBanner: () => set({ workspaceRestoreDismissed: true }),
    resyncSessions: async (options) => {
      const epoch = sessionsEpoch;
      const seq = ++sessionsRequestSeq;
      try {
        const list = await sessionApi.list();
        // Latest-only: a superseded response (older request landing late) must not overwrite newer data.
        if (epoch !== sessionsEpoch || seq !== sessionsRequestSeq) return;
        set({ sessions: list, sessionResyncFailed: false });
      } catch (e) {
        if (epoch !== sessionsEpoch || seq !== sessionsRequestSeq || options?.silent) return;
        console.error("[NexTerm] Failed to refresh session list", e);
        if (get().sessionResyncFailed) return;
        set({ sessionResyncFailed: true });
        get().pushToast("error", `刷新会话列表失败：${describeError(e)}`);
      } finally {
        if (epoch === sessionsEpoch && seq === sessionsRequestSeq) set({ sessionResyncCompleted: true });
      }
    },
    resetSessions: (options) => {
      const clearWorkspaceRuntime = options?.clearWorkspaceRuntime === true;
      sessionsEpoch += 1;
      advanceWorkspaceProblemResetGeneration();
      recordingsEpoch += 1;
      abnormalDisconnectNotices.clear();
      sessionStatusEvidenceMap.clear();
      get().cancelAccountDialogs();
      set((state) => ({
        sessions: [],
        recordings: {},
        helperProgress: {},
        helperOperations: createHelperOperationState(),
        helperOperationTabs: {},
        helperOperationIntents: {},
        helperRetries: {},
        sessionResyncFailed: false,
        sessionResyncCompleted: false,
        workspaceRuntimeEpoch: state.workspaceRuntimeEpoch + (clearWorkspaceRuntime ? 1 : 0),
        workspaceRuntime: Object.fromEntries(
          Object.entries(state.workspaceRuntime).map(([workspaceId, runtime]) => [
            workspaceId,
            clearWorkspaceRuntime
              ? { generation: nextWorkspaceGeneration() }
              : { ...runtime, problems: undefined },
          ]),
        ) as Record<string, WorkspaceRuntime>,
        connectionProblems: {},
      }));
    },
    resetAccountWorkspaces: () => {
      sessionsEpoch += 1;
      advanceWorkspaceProblemResetGeneration();
      recordingsEpoch += 1;
      abnormalDisconnectNotices.clear();
      sessionStatusEvidenceMap.clear();
      get().cancelAccountDialogs();
      set((state) => ({
        sessions: [],
        recordings: {},
        sessionResyncFailed: false,
        sessionResyncCompleted: false,
        workspaceRestoreDismissed: false,
        workspaceRestoreResults: {},
        workspaces: [],
        activeWorkspaceId: null,
        projectInventory: null,
        connectingAssetIds: [],
        connectProgress: {},
        helperProgress: {},
        helperOperations: createHelperOperationState(),
        helperOperationTabs: {},
        helperOperationIntents: {},
        helperRetries: {},
        workspaceRuntimeEpoch: state.workspaceRuntimeEpoch + 1,
        workspaceRuntime: {},
        connectionProblems: {},
        knownAssetIds: [],
        knownAssetsEpoch: -1,
        broadcast: null,
      }));
    },
    connectingAssetIds: [],

    connectProgress: {},
    setConnectProgress: (assetId, progress) =>
      set((st) => {
        if (!assetId) return st;
        const next = { ...st.connectProgress };
        if (progress) next[assetId] = progress;
        else delete next[assetId];
        return { connectProgress: next };
      }),
  } satisfies Partial<UiState>;
}

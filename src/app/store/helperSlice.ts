import { assetApi, terminalApi } from "../../ipc/commands";
import {
  createHelperOperationState,
  HELPER_DEFAULT_BUDGET_MS,
  helperOperationReducer,
} from "../../features/terminal/helperOperationState";
import {
  helperOperationLive,
  helperOperationMessage,
  helperProgressContext,
  helperProgressKey,
  helperTabContext,
  nextLocalHelperOperationID,
} from "./helperOperations";
import type { UiGet, UiSet, UiState } from "./types";

let helperOperationIntentSeq = 0;

export function createHelperSlice(set: UiSet, get: UiGet) {
  return {
    helperProgress: {},
    helperOperations: createHelperOperationState(),
    helperOperationTabs: {},
    helperOperationIntents: {},
    helperRetries: {},
    registerHelperOperationIntent: (tabId, kind, originalTabId) => {
      helperOperationIntentSeq += 1;
      set((st) => ({
        helperOperationIntents: {
          ...st.helperOperationIntents,
          [tabId]: { kind, originalTabId, sequence: helperOperationIntentSeq },
        },
      }));
    },
    setHelperProgress: (progress) => {
      if (!progress) return;
      set((st) => {
        if (!progress.sessionId || !progress.assetId) return st;
        const key = helperProgressKey(progress);
        const raw = { ...st.helperProgress };
        const operationID = progress.operationId?.trim() ?? "";
        const attempt = progress.attempt ?? 0;
        const deadline = progress.deadline ?? 0;
        if (!operationID || !Number.isInteger(attempt) || attempt <= 0 || !Number.isFinite(deadline) || deadline <= 0) {
          if (progress.done && !progress.error) delete raw[key];
          else raw[key] = progress;
          return { helperProgress: raw };
        }
        const context = helperProgressContext(st, progress);
        if (!context) return st;
        const now = Date.now();
        const tabId = context.tabId;
        let operations = st.helperOperations;
        let retries = st.helperRetries;
        let operation = operations.operations[tabId];
        const pending = retries[progress.sessionId];
        if (pending && pending.tabId === tabId && pending.operationId !== operationID) {
          operations = helperOperationReducer(operations, {
            type: "retry",
            tabId,
            operationId: pending.operationId,
            newOperationId: operationID,
            now,
            budgetMs: HELPER_DEFAULT_BUDGET_MS,
            deadline,
          });
          retries = { ...retries };
          delete retries[progress.sessionId];
          operation = operations.operations[tabId];
        } else if (!operation || operation.operationId !== operationID) {
          if (operations.usedOperationIds.includes(operationID)) return st;
          if (operation) {
            operations = {
              ...operations,
              operations: { ...operations.operations },
            };
            delete operations.operations[tabId];
          }
          operations = helperOperationReducer(operations, {
            type: "start",
            tabId,
            operationId: operationID,
            kind: context.kind,
            now,
            budgetMs: HELPER_DEFAULT_BUDGET_MS,
            deadline,
            assetId: progress.assetId,
            quickConnect: context.quickConnect,
            originalTabId: context.originalTabId,
          });
          operation = operations.operations[tabId];
        }
        if (!operation || operation.operationId !== operationID || operation.attempt > attempt) return st;
        if (operation.attempt < attempt) {
          operations = helperOperationReducer(operations, {
            type: "attemptStarted",
            tabId,
            operationId: operationID,
            attempt,
            now,
            stage: progress.stage,
          });
          operation = operations.operations[tabId];
          if (!operation || operation.attempt !== attempt) return st;
        }
        const message = helperOperationMessage(progress);
        const reason = message ? { code: `helper_${progress.stage}`, message } : undefined;
        if (progress.stage === "backoff") {
          operations = helperOperationReducer(operations, {
            type: "attemptFailed",
            tabId,
            operationId: operationID,
            attempt,
            now,
            reason: { kind: "transient", code: "helper_backoff", message: message ?? "transient helper failure" },
            path: progress.path ?? null,
            transport: progress.transport ?? null,
          });
        } else if (progress.stage === "failed") {
          operations = helperOperationReducer(operations, {
            type: "attemptFailed",
            tabId,
            operationId: operationID,
            attempt,
            now,
            reason: {
              kind: operation.kind === "reattach" ? "restore_failed" : "permanent",
              code: "helper_failed",
              message: message ?? "helper operation failed",
            },
            path: progress.path ?? null,
            transport: progress.transport ?? null,
          });
        } else if (progress.stage === "ready" && progress.done && operation.kind === "deploy") {
          operations = helperOperationReducer(operations, {
            type: "succeeded",
            tabId,
            operationId: operationID,
            attempt,
            now,
          });
        } else {
          operations = helperOperationReducer(operations, {
            type: "progress",
            tabId,
            operationId: operationID,
            attempt,
            now,
            stage: progress.stage,
            reason: reason ? { kind: "info", ...reason } : null,
            path: progress.path ?? null,
            transport: progress.transport ?? null,
          });
        }
        operation = operations.operations[tabId];
        if (!operation || operation.operationId !== operationID) return st;
        if (progress.done && !progress.error && operation.kind === "deploy") delete raw[key];
        else raw[key] = progress;
        return {
          helperProgress: raw,
          helperOperations: operations,
          helperOperationTabs: { ...st.helperOperationTabs, [progress.sessionId]: tabId },
          helperRetries: retries,
        };
      });
    },
    cancelHelperOperation: async (tabId) => {
      const st = get();
      const operation = st.helperOperations.operations[tabId];
      if (!operation || !helperOperationLive(operation)) return;
      const context = helperTabContext(st, tabId);
      if (!context) throw new Error("终端所属会话已不存在");
      if (operation.status === "running" && !operation.operationId.startsWith("local-restore-")) {
        await terminalApi.helperOperation(context.sessionId, operation.operationId, "cancel");
      }
      set((current) => ({
        helperOperations: helperOperationReducer(current.helperOperations, {
          type: "cancel",
          tabId,
          operationId: operation.operationId,
        }),
      }));
    },
    retryHelperOperation: async (tabId) => {
      const st = get();
      const operation = st.helperOperations.operations[tabId];
      if (!operation || operation.status !== "awaiting_choice") throw new Error("当前持久终端操作不能重试");
      const context = helperTabContext(st, tabId);
      if (!context) throw new Error("终端所属会话已不存在");
      if (operation.operationId.startsWith("local-restore-")) {
        const now = Date.now();
        set((current) => ({
          helperOperations: helperOperationReducer(current.helperOperations, {
            type: "retry",
            tabId,
            operationId: operation.operationId,
            newOperationId: nextLocalHelperOperationID(),
            now,
            budgetMs: HELPER_DEFAULT_BUDGET_MS,
            deadline: now + HELPER_DEFAULT_BUDGET_MS,
          }),
        }));
        return;
      }
      set((current) => ({
        helperRetries: {
          ...current.helperRetries,
          [context.sessionId]: { tabId, operationId: operation.operationId },
        },
      }));
      try {
        await terminalApi.helperOperation(context.sessionId, operation.operationId, "retry");
      } catch (error) {
        set((current) => {
          const retries = { ...current.helperRetries };
          if (retries[context.sessionId]?.operationId === operation.operationId) delete retries[context.sessionId];
          return { helperRetries: retries };
        });
        throw error;
      }
    },
    continueHelperOrdinary: async (tabId, remember = false) => {
      const st = get();
      const operation = st.helperOperations.operations[tabId];
      if (!operation || operation.status !== "awaiting_choice") return;
      if (remember && !operation.quickConnect && operation.assetId) {
        const asset = await assetApi.get(operation.assetId);
        await assetApi.update({
          id: operation.assetId,
          options: { ...(asset.options ?? {}), terminalMode: "regular" },
        });
      }
      set((current) => ({
        helperOperations: helperOperationReducer(current.helperOperations, {
          type: "continueOrdinary",
          tabId,
          operationId: operation.operationId,
          remember,
        }),
      }));
    },
    failHelperRestore: (tabId, message) => {
      set((st) => {
        const context = helperTabContext(st, tabId);
        if (!context?.originalTabId) return st;
        const existing = st.helperOperations.operations[tabId];
        if (existing?.status === "awaiting_choice") return st;
        if (existing?.status === "running" && existing.kind === "reattach") {
          return {
            helperOperations: helperOperationReducer(st.helperOperations, {
              type: "attemptFailed",
              tabId,
              operationId: existing.operationId,
              attempt: existing.attempt,
              now: Date.now(),
              reason: { kind: "restore_failed", code: "restore_failed", message: message ?? "原持久终端恢复失败" },
            }),
          };
        }
        const now = Date.now();
        const operationId = nextLocalHelperOperationID();
        let operations = helperOperationReducer(st.helperOperations, {
          type: "start",
          tabId,
          operationId,
          kind: "reattach",
          now,
          budgetMs: HELPER_DEFAULT_BUDGET_MS,
          deadline: now + HELPER_DEFAULT_BUDGET_MS,
          assetId: st.sessions.find((session) => session.id === context.sessionId)?.assetId ?? undefined,
          quickConnect: context.quickConnect,
          originalTabId: context.originalTabId,
        });
        operations = helperOperationReducer(operations, {
          type: "attemptFailed",
          tabId,
          operationId,
          attempt: 1,
          now,
          reason: { kind: "restore_failed", code: "restore_failed", message: message ?? "原持久终端恢复失败" },
        });
        return { helperOperations: operations };
      });
    },
    dismissHelperOperation: (tabId, operationId) => {
      set((st) => {
        const operation = st.helperOperations.operations[tabId];
        if (!operation || (operationId !== undefined && operation.operationId !== operationId)) return st;
        const operations = { ...st.helperOperations.operations };
        delete operations[tabId];
        const tabs = { ...st.helperOperationTabs };
        for (const [sessionId, mapped] of Object.entries(tabs)) {
          if (mapped === tabId) delete tabs[sessionId];
        }
        const retries = { ...st.helperRetries };
        for (const [sessionId, retry] of Object.entries(retries)) {
          if (retry.tabId === tabId) delete retries[sessionId];
        }
        const intents = { ...st.helperOperationIntents };
        delete intents[tabId];
        const raw = { ...st.helperProgress };
        const context = helperTabContext(st, tabId);
        if (context) {
          const session = st.sessions.find((candidate) => candidate.id === context.sessionId);
          if (session?.assetId) delete raw[helperProgressKey({ sessionId: context.sessionId, assetId: session.assetId })];
        }
        return {
          helperOperations: { ...st.helperOperations, operations },
          helperOperationTabs: tabs,
          helperOperationIntents: intents,
          helperRetries: retries,
          helperProgress: raw,
        };
      });
    },
  } satisfies Partial<UiState>;
}

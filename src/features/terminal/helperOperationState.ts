export const HELPER_MAX_ATTEMPTS = 3;
export const HELPER_DEFAULT_BUDGET_MS = 10 * 60 * 1_000;

export type HelperOperationKind = "deploy" | "reattach";
export type HelperOperationStatus = "running" | "awaiting_choice" | "succeeded" | "failed" | "canceled";
export type HelperOperationStage = string;

export type HelperReasonKind =
  | "info"
  | "transient"
  | "permanent"
  | "timeout"
  | "restore_failed"
  | "canceled"
  | "cleanup_failed";

export interface HelperReasonDetail {
  code: string;
  message?: string;
}

export interface HelperOperationReason extends HelperReasonDetail {
  kind: HelperReasonKind;
}

export interface HelperFailureReason extends HelperReasonDetail {
  kind: "transient" | "permanent" | "restore_failed";
}

export type HelperVersionState =
  | { known: false }
  | {
      known: true;
      release?: string | null;
      commit?: string | null;
      protocol?: string | number | null;
      sha256?: string | null;
    };

export type HelperActivityState = { known: false } | { known: true; active: boolean };
export type HelperOriginalSession = "not_applicable" | "unknown" | "restored" | "not_restored";

export interface HelperOperation {
  operationId: string;
  tabId: string;
  kind: HelperOperationKind;
  status: HelperOperationStatus;
  stage: HelperOperationStage;
  reason: HelperOperationReason | null;
  attempt: number;
  maxAttempts: number;
  budgetMs: number;
  deadline: number;
  retryAt: number | null;
  originalTabId?: string;
  quickConnect: boolean;
  assetId: string | null;
  runningVersion: HelperVersionState;
  activity: HelperActivityState;
  ssh: "preserved";
  existingDurable: "preserved";
  remoteProcess: "unknown";
  originalSession: HelperOriginalSession;
  choice: "ordinary" | "canceled" | null;
  ordinary: "existing" | "new" | null;
  cleanupFailure: HelperOperationReason | null;
  path: string | null;
  transport: string | null;
}

export interface HelperRegularPreference {
  appliesTo: "new_terminals";
  existingDurable: "preserved";
}

export interface HelperOperationState {
  operations: Record<string, HelperOperation>;
  regularByAsset: Record<string, HelperRegularPreference>;
  usedOperationIds: string[];
}

interface HelperOperationScope {
  tabId: string;
  operationId: string;
  attempt: number;
}

export type HelperOperationAction =
  | {
      type: "start";
      tabId: string;
      operationId: string;
      kind: HelperOperationKind;
      now: number;
      budgetMs?: number;
      deadline?: number;
      assetId?: string;
      quickConnect?: boolean;
      originalTabId?: string;
    }
  | (HelperOperationScope & {
      type: "progress";
      now: number;
      stage: HelperOperationStage;
      reason?: HelperOperationReason | null;
      path?: string | null;
      transport?: string | null;
    })
  | (HelperOperationScope & {
      type: "attemptStarted";
      now: number;
      stage?: HelperOperationStage;
    })
  | (HelperOperationScope & {
      type: "attemptFailed";
      now: number;
      retryDelayMs?: number;
      reason: HelperFailureReason;
      path?: string | null;
      transport?: string | null;
    })
  | (HelperOperationScope & { type: "succeeded"; now: number })
  | (HelperOperationScope & {
      type: "observe";
      now: number;
      runningVersion?: HelperVersionState;
      activity?: HelperActivityState;
    })
  | (HelperOperationScope & { type: "tick"; now: number })
  | {
      type: "retry";
      tabId: string;
      operationId: string;
      newOperationId: string;
      now: number;
      budgetMs?: number;
      deadline?: number;
    }
  | {
      type: "continueOrdinary";
      tabId: string;
      operationId: string;
      remember?: boolean;
    }
  | { type: "cancel"; tabId: string; operationId: string }
  | (HelperOperationScope & {
      type: "cleanupFailed";
      reason: HelperReasonDetail;
    });

interface OperationSeed {
  tabId: string;
  operationId: string;
  kind: HelperOperationKind;
  now: number;
  budgetMs: number;
  deadline?: number;
  assetId: string | null;
  quickConnect: boolean;
  originalTabId?: string;
}

function initialStage(kind: HelperOperationKind): HelperOperationStage {
  return kind === "deploy" ? "discover" : "probe";
}

function makeOperation(seed: OperationSeed): HelperOperation {
  return {
    operationId: seed.operationId,
    tabId: seed.tabId,
    kind: seed.kind,
    status: "running",
    stage: initialStage(seed.kind),
    reason: null,
    attempt: 1,
    maxAttempts: HELPER_MAX_ATTEMPTS,
    budgetMs: seed.budgetMs,
    deadline: seed.deadline ?? seed.now + seed.budgetMs,
    retryAt: null,
    originalTabId: seed.originalTabId,
    quickConnect: seed.quickConnect,
    assetId: seed.assetId,
    runningVersion: { known: false },
    activity: { known: false },
    ssh: "preserved",
    existingDurable: "preserved",
    remoteProcess: "unknown",
    originalSession: seed.kind === "reattach" ? "unknown" : "not_applicable",
    choice: null,
    ordinary: null,
    cleanupFailure: null,
    path: null,
    transport: null,
  };
}

function validBudget(now: number, budgetMs: number): boolean {
  return Number.isFinite(now) && Number.isFinite(budgetMs) && budgetMs > 0;
}

function operationIdWasUsed(state: HelperOperationState, operationId: string): boolean {
  return state.usedOperationIds.includes(operationId);
}

function operationForTab(
  state: HelperOperationState,
  tabId: string,
  operationId: string,
): HelperOperation | undefined {
  const operation = state.operations[tabId];
  return operation?.operationId === operationId ? operation : undefined;
}

function scopedOperation(
  state: HelperOperationState,
  action: HelperOperationScope,
): HelperOperation | undefined {
  const operation = operationForTab(state, action.tabId, action.operationId);
  return operation?.attempt === action.attempt ? operation : undefined;
}

function updateOperation(state: HelperOperationState, operation: HelperOperation): HelperOperationState {
  return {
    ...state,
    operations: { ...state.operations, [operation.tabId]: operation },
  };
}

function expireOperation(state: HelperOperationState, operation: HelperOperation): HelperOperationState {
  return updateOperation(state, {
    ...operation,
    status: "awaiting_choice",
    stage: "failed",
    reason: { kind: "timeout", code: "deadline_exceeded" },
    retryAt: null,
  });
}

function isAwaitingOrRunning(operation: HelperOperation): boolean {
  return operation.status === "running" || operation.status === "awaiting_choice";
}

export function createHelperOperationState(): HelperOperationState {
  return { operations: {}, regularByAsset: {}, usedOperationIds: [] };
}

export function helperOperationReducer(
  state: HelperOperationState,
  action: HelperOperationAction,
): HelperOperationState {
  switch (action.type) {
    case "start": {
      const existing = state.operations[action.tabId];
      if ((existing && isAwaitingOrRunning(existing)) || operationIdWasUsed(state, action.operationId)) {
        return state;
      }
      const budgetMs = action.budgetMs ?? HELPER_DEFAULT_BUDGET_MS;
      if (!validBudget(action.now, budgetMs)) return state;
      const operation = makeOperation({
        tabId: action.tabId,
        operationId: action.operationId,
        kind: action.kind,
        now: action.now,
        budgetMs,
        deadline: action.deadline,
        assetId: action.assetId ?? null,
        quickConnect: action.quickConnect ?? false,
        originalTabId: action.originalTabId,
      });
      return {
        ...updateOperation(state, operation),
        usedOperationIds: [...state.usedOperationIds, operation.operationId],
      };
    }
    case "progress": {
      const operation = scopedOperation(state, action);
      if (!operation || operation.status !== "running" || !Number.isFinite(action.now)) return state;
      if (action.now >= operation.deadline) return expireOperation(state, operation);
      return updateOperation(state, {
        ...operation,
        stage: action.stage,
        reason: action.reason ?? null,
        path: action.path === undefined ? operation.path : action.path,
        transport: action.transport === undefined ? operation.transport : action.transport,
      });
    }
    case "attemptStarted": {
      const operation = operationForTab(state, action.tabId, action.operationId);
      if (
        !operation ||
        operation.status !== "running" ||
        operation.stage !== "backoff" ||
        operation.retryAt === null ||
        operation.attempt >= HELPER_MAX_ATTEMPTS ||
        action.attempt !== operation.attempt + 1 ||
        !Number.isFinite(action.now) ||
        action.now < operation.retryAt
      ) {
        return state;
      }
      if (action.now >= operation.deadline) return expireOperation(state, operation);
      return updateOperation(state, {
        ...operation,
        status: "running",
        stage: action.stage ?? initialStage(operation.kind),
        reason: null,
        attempt: operation.attempt + 1,
        retryAt: null,
      });
    }
    case "attemptFailed": {
      const operation = scopedOperation(state, action);
      if (!operation || operation.status !== "running" || !Number.isFinite(action.now)) return state;
      if (action.now >= operation.deadline) return expireOperation(state, operation);
      if (action.reason.kind !== "transient" || operation.attempt >= HELPER_MAX_ATTEMPTS) {
        return updateOperation(state, {
          ...operation,
          status: "awaiting_choice",
          stage: "failed",
          reason: action.reason,
          retryAt: null,
          path: action.path === undefined ? operation.path : action.path,
          transport: action.transport === undefined ? operation.transport : action.transport,
          originalSession:
            operation.kind === "reattach" && action.reason.kind === "restore_failed"
              ? "not_restored"
              : operation.originalSession,
        });
      }
      const requestedDelay = action.retryDelayMs ?? 0;
      const retryDelay = Number.isFinite(requestedDelay) ? Math.max(0, requestedDelay) : 0;
      return updateOperation(state, {
        ...operation,
        status: "running",
        stage: "backoff",
        reason: action.reason,
        retryAt: Math.min(action.now + retryDelay, operation.deadline),
        path: action.path === undefined ? operation.path : action.path,
        transport: action.transport === undefined ? operation.transport : action.transport,
      });
    }
    case "succeeded": {
      const operation = scopedOperation(state, action);
      if (!operation || operation.status !== "running" || !Number.isFinite(action.now)) return state;
      if (action.now >= operation.deadline) return expireOperation(state, operation);
      return updateOperation(state, {
        ...operation,
        status: "succeeded",
        stage: "ready",
        reason: null,
        retryAt: null,
        originalSession: operation.kind === "reattach" ? "restored" : "not_applicable",
      });
    }
    case "observe": {
      const operation = scopedOperation(state, action);
      if (!operation || !isAwaitingOrRunning(operation) || !Number.isFinite(action.now)) return state;
      if (operation.status === "running" && action.now >= operation.deadline) {
        return expireOperation(state, operation);
      }
      if (action.runningVersion === undefined && action.activity === undefined) return state;
      return updateOperation(state, {
        ...operation,
        runningVersion: action.runningVersion ?? operation.runningVersion,
        activity: action.activity ?? operation.activity,
      });
    }
    case "tick": {
      const operation = scopedOperation(state, action);
      if (
        !operation ||
        operation.status !== "running" ||
        !Number.isFinite(action.now) ||
        action.now < operation.deadline
      ) {
        return state;
      }
      return expireOperation(state, operation);
    }
    case "retry": {
      const operation = operationForTab(state, action.tabId, action.operationId);
      if (
        !operation ||
        operation.status !== "awaiting_choice" ||
        operationIdWasUsed(state, action.newOperationId)
      ) {
        return state;
      }
      const budgetMs = action.budgetMs ?? operation.budgetMs;
      if (!validBudget(action.now, budgetMs)) return state;
      const retry = makeOperation({
        tabId: operation.tabId,
        operationId: action.newOperationId,
        kind: operation.kind,
        now: action.now,
        budgetMs,
        deadline: action.deadline,
        assetId: operation.assetId,
        quickConnect: operation.quickConnect,
        originalTabId: operation.originalTabId,
      });
      return {
        ...updateOperation(state, retry),
        usedOperationIds: [...state.usedOperationIds, retry.operationId],
      };
    }
    case "continueOrdinary": {
      const operation = operationForTab(state, action.tabId, action.operationId);
      if (!operation || operation.status !== "awaiting_choice") return state;
      const remember = action.remember === true && !operation.quickConnect && operation.assetId !== null;
      const regularByAsset = remember && operation.assetId !== null
        ? {
            ...state.regularByAsset,
            [operation.assetId]: {
              appliesTo: "new_terminals" as const,
              existingDurable: "preserved" as const,
            },
          }
        : state.regularByAsset;
      return {
        ...updateOperation(state, {
          ...operation,
          status: "failed",
          stage: "failed",
          choice: "ordinary",
          ordinary: operation.kind === "reattach" ? "new" : "existing",
          retryAt: null,
        }),
        regularByAsset,
      };
    }
    case "cancel": {
      const operation = operationForTab(state, action.tabId, action.operationId);
      if (!operation || !isAwaitingOrRunning(operation)) return state;
      return updateOperation(state, {
        ...operation,
        status: "canceled",
        reason: operation.reason ?? { kind: "canceled", code: "canceled" },
        choice: "canceled",
        retryAt: null,
      });
    }
    case "cleanupFailed": {
      const operation = scopedOperation(state, action);
      if (!operation) return state;
      return updateOperation(state, {
        ...operation,
        cleanupFailure: { kind: "cleanup_failed", ...action.reason },
      });
    }
  }
}

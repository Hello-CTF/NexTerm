import { describe, expect, it } from "vitest";
import {
  createHelperOperationState,
  HELPER_MAX_ATTEMPTS,
  helperOperationReducer,
  type HelperFailureReason,
  type HelperOperation,
  type HelperOperationAction,
  type HelperOperationState,
} from "../../features/terminal/helperOperationState";

const NOW = 1_000;
const BUDGET = 10_000;

type StartAction = Extract<HelperOperationAction, { type: "start" }>;

function startAction(overrides: Partial<StartAction> = {}): StartAction {
  return {
    type: "start",
    tabId: "tab",
    operationId: "op",
    kind: "deploy",
    now: NOW,
    budgetMs: BUDGET,
    ...overrides,
  };
}

function current(state: HelperOperationState, tabId = "tab"): HelperOperation {
  const operation = state.operations[tabId];
  if (!operation) throw new Error(`Missing operation for ${tabId}`);
  return operation;
}

function apply(
  state: HelperOperationState,
  ...actions: HelperOperationAction[]
): HelperOperationState {
  return actions.reduce(helperOperationReducer, state);
}

function failure(kind: HelperFailureReason["kind"] = "permanent"): HelperFailureReason {
  return { kind, code: `test_${kind}`, message: `test ${kind}` };
}

function started(overrides: Partial<StartAction> = {}): HelperOperationState {
  return helperOperationReducer(createHelperOperationState(), startAction(overrides));
}

function awaitingChoice(overrides: Partial<StartAction> = {}): HelperOperationState {
  return apply(started(overrides), {
    type: "attemptFailed",
    tabId: overrides.tabId ?? "tab",
    operationId: overrides.operationId ?? "op",
    attempt: 1,
    now: NOW + 100,
    reason: failure(),
  });
}

describe("helper operation lifecycle", () => {
  it.each([
    { kind: "deploy" as const, stage: "discover", originalSession: "not_applicable" },
    { kind: "reattach" as const, stage: "probe", originalSession: "unknown" },
  ])("starts $kind with a bounded first attempt and unknown evidence", ({ kind, stage, originalSession }) => {
    const state = started({
      kind,
      assetId: "asset-a",
      originalTabId: kind === "reattach" ? "original-tab" : undefined,
    });
    const operation = current(state);

    expect(operation).toMatchObject({
      operationId: "op",
      tabId: "tab",
      kind,
      status: "running",
      stage,
      reason: null,
      attempt: 1,
      maxAttempts: 3,
      budgetMs: BUDGET,
      deadline: NOW + BUDGET,
      retryAt: null,
      originalTabId: kind === "reattach" ? "original-tab" : undefined,
      quickConnect: false,
      assetId: "asset-a",
      runningVersion: { known: false },
      activity: { known: false },
      ssh: "preserved",
      existingDurable: "preserved",
      remoteProcess: "unknown",
      originalSession,
      choice: null,
      ordinary: null,
      cleanupFailure: null,
    });
    expect(HELPER_MAX_ATTEMPTS).toBe(3);
  });

  it("moves through real progress and succeeds only on an explicit success event", () => {
    let state = started();
    state = apply(state, {
      type: "progress",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + 50,
      stage: "upload",
      reason: { kind: "info", code: "sftp_fallback", message: "using exec" },
    });

    expect(current(state)).toMatchObject({
      status: "running",
      stage: "upload",
      reason: { kind: "info", code: "sftp_fallback", message: "using exec" },
    });

    state = apply(state, {
      type: "succeeded",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + 100,
    });
    expect(current(state)).toMatchObject({
      status: "succeeded",
      stage: "ready",
      reason: null,
      runningVersion: { known: false },
      activity: { known: false },
      originalSession: "not_applicable",
    });
  });

  it("marks a reattach success as restored without inferring version or activity", () => {
    const state = apply(started({ kind: "reattach", originalTabId: "original-tab" }), {
      type: "succeeded",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + 100,
    });

    expect(current(state)).toMatchObject({
      status: "succeeded",
      stage: "ready",
      originalTabId: "original-tab",
      originalSession: "restored",
      runningVersion: { known: false },
      activity: { known: false },
      remoteProcess: "unknown",
    });
  });

  it("does not replace a live operation on a duplicate start", () => {
    const state = started();
    expect(helperOperationReducer(state, startAction({ operationId: "replacement" }))).toBe(state);
  });
});

describe("attempt budget and event fencing", () => {
  it("backs off a transient failure and accepts only the matching next attempt", () => {
    let state = apply(started(), {
      type: "attemptFailed",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + 100,
      retryDelayMs: 200,
      reason: failure("transient"),
    });

    expect(current(state)).toMatchObject({
      status: "running",
      stage: "backoff",
      attempt: 1,
      retryAt: NOW + 300,
      reason: { kind: "transient", code: "test_transient" },
    });

    const backoff = state;
    state = apply(state, {
      type: "attemptStarted",
      tabId: "tab",
      operationId: "op",
      attempt: 2,
      now: NOW + 200,
    });
    expect(state).toBe(backoff);

    state = apply(state, {
      type: "attemptStarted",
      tabId: "tab",
      operationId: "op",
      attempt: 2,
      now: NOW + 300,
      stage: "verify",
    });
    expect(current(state)).toMatchObject({
      status: "running",
      stage: "verify",
      attempt: 2,
      retryAt: null,
      reason: null,
    });

    const secondAttempt = state;
    state = apply(state, {
      type: "attemptFailed",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + 301,
      reason: failure("permanent"),
    });
    expect(state).toBe(secondAttempt);
  });

  it("offers an immediate choice on permanent error without consuming another attempt", () => {
    const state = awaitingChoice();
    expect(current(state)).toMatchObject({
      status: "awaiting_choice",
      stage: "failed",
      attempt: 1,
      maxAttempts: 3,
      retryAt: null,
      reason: { kind: "permanent", code: "test_permanent" },
    });

    expect(apply(state, {
      type: "attemptStarted",
      tabId: "tab",
      operationId: "op",
      attempt: 2,
      now: NOW + 200,
    })).toBe(state);
  });

  it("stops after the third failed attempt and never starts a fourth", () => {
    let state = started();
    for (let attempt = 1; attempt <= 3; attempt += 1) {
      state = apply(state, {
        type: "attemptFailed",
        tabId: "tab",
        operationId: "op",
        attempt,
        now: NOW + attempt * 100,
        retryDelayMs: 10,
        reason: failure("transient"),
      });
      if (attempt < 3) {
        state = apply(state, {
          type: "attemptStarted",
          tabId: "tab",
          operationId: "op",
          attempt: attempt + 1,
          now: NOW + attempt * 100 + 10,
        });
      }
    }

    expect(current(state)).toMatchObject({
      status: "awaiting_choice",
      stage: "failed",
      attempt: 3,
      maxAttempts: 3,
      retryAt: null,
    });
    expect(apply(state, {
      type: "attemptStarted",
      tabId: "tab",
      operationId: "op",
      attempt: 4,
      now: NOW + 500,
    })).toBe(state);
  });

  it("times out during backoff at the shared deadline", () => {
    let state = apply(started(), {
      type: "attemptFailed",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + 100,
      retryDelayMs: BUDGET,
      reason: failure("transient"),
    });
    expect(current(state)).toMatchObject({
      status: "running",
      stage: "backoff",
      retryAt: NOW + BUDGET,
    });

    expect(apply(state, {
      type: "tick",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + BUDGET - 1,
    })).toBe(state);

    state = apply(state, {
      type: "tick",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + BUDGET,
    });
    expect(current(state)).toMatchObject({
      status: "awaiting_choice",
      stage: "failed",
      attempt: 1,
      retryAt: null,
      reason: { kind: "timeout", code: "deadline_exceeded" },
    });

    expect(apply(state, {
      type: "attemptStarted",
      tabId: "tab",
      operationId: "op",
      attempt: 2,
      now: NOW + BUDGET + 1,
    })).toBe(state);
  });

  it("rejects a late success at the deadline instead of manufacturing a result", () => {
    const state = apply(started(), {
      type: "succeeded",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + BUDGET,
    });
    expect(current(state)).toMatchObject({
      status: "awaiting_choice",
      stage: "failed",
      reason: { kind: "timeout", code: "deadline_exceeded" },
    });
  });

  it("retries as a fresh operation and fences every old operation event", () => {
    let state = apply(started({ kind: "reattach", originalTabId: "original-tab" }), {
      type: "observe",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + 50,
      runningVersion: { known: true, release: "old-release", protocol: 7 },
      activity: { known: true, active: false },
    });
    state = apply(state, {
      type: "attemptFailed",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + 100,
      reason: failure("permanent"),
    });
    state = apply(state, {
      type: "retry",
      tabId: "tab",
      operationId: "op",
      newOperationId: "new-op",
      now: NOW + 5_000,
      budgetMs: 2_000,
    });

    expect(current(state)).toMatchObject({
      operationId: "new-op",
      kind: "reattach",
      originalTabId: "original-tab",
      status: "running",
      stage: "probe",
      attempt: 1,
      maxAttempts: 3,
      budgetMs: 2_000,
      deadline: NOW + 7_000,
      retryAt: null,
      reason: null,
      runningVersion: { known: false },
      activity: { known: false },
      originalSession: "unknown",
      choice: null,
      cleanupFailure: null,
    });

    const oldEvents: HelperOperationAction[] = [
      { type: "progress", tabId: "tab", operationId: "op", attempt: 1, now: NOW + 5_100, stage: "ready" },
      { type: "succeeded", tabId: "tab", operationId: "op", attempt: 1, now: NOW + 5_100 },
      { type: "attemptFailed", tabId: "tab", operationId: "op", attempt: 1, now: NOW + 5_100, reason: failure() },
      { type: "observe", tabId: "tab", operationId: "op", attempt: 1, now: NOW + 5_100, activity: { known: true, active: true } },
      { type: "cleanupFailed", tabId: "tab", operationId: "op", attempt: 1, reason: { code: "old_cleanup" } },
    ];
    for (const action of oldEvents) {
      expect(helperOperationReducer(state, action)).toBe(state);
    }

    expect(apply(state, {
      type: "attemptStarted",
      tabId: "tab",
      operationId: "new-op",
      attempt: 2,
      now: NOW + 5_100,
    })).toBe(state);
  });
});

describe("choices and truthful outcomes", () => {
  it("continues ordinary with SSH and durable preserved and remembers only new terminals for the asset", () => {
    const state = apply(awaitingChoice({ assetId: "asset-a" }), {
      type: "continueOrdinary",
      tabId: "tab",
      operationId: "op",
      remember: true,
    });

    expect(current(state)).toMatchObject({
      status: "failed",
      stage: "failed",
      choice: "ordinary",
      ordinary: "existing",
      ssh: "preserved",
      existingDurable: "preserved",
      remoteProcess: "unknown",
      reason: { kind: "permanent", code: "test_permanent" },
    });
    expect(state.regularByAsset).toEqual({
      "asset-a": { appliesTo: "new_terminals", existingDurable: "preserved" },
    });
  });

  it("cancels without giving up SSH and ignores stale success", () => {
    let state = apply(started(), { type: "cancel", tabId: "tab", operationId: "op" });
    expect(current(state)).toMatchObject({
      status: "canceled",
      choice: "canceled",
      ssh: "preserved",
      existingDurable: "preserved",
      remoteProcess: "unknown",
      reason: { kind: "canceled", code: "canceled" },
    });

    expect(apply(state, {
      type: "succeeded",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + 100,
    })).toBe(state);
    state = apply(state, {
      type: "cleanupFailed",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      reason: { code: "cleanup_denied", message: "cleanup failed" },
    });
    expect(current(state)).toMatchObject({
      status: "canceled",
      choice: "canceled",
      cleanupFailure: { kind: "cleanup_failed", code: "cleanup_denied", message: "cleanup failed" },
      remoteProcess: "unknown",
    });
  });

  it("never stores regular-mode memory for quick connect", () => {
    const state = apply(awaitingChoice({ quickConnect: true, assetId: "quick-target" }), {
      type: "continueOrdinary",
      tabId: "tab",
      operationId: "op",
      remember: true,
    });

    expect(current(state)).toMatchObject({ quickConnect: true, choice: "ordinary" });
    expect(state.regularByAsset).toEqual({});
  });

  it("keeps restore failure explicit and makes ordinary a new shell intent, never a remote kill", () => {
    let state = apply(started({ kind: "reattach", originalTabId: "original-tab" }), {
      type: "attemptFailed",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + 100,
      reason: failure("restore_failed"),
    });
    expect(current(state)).toMatchObject({
      status: "awaiting_choice",
      originalTabId: "original-tab",
      originalSession: "not_restored",
      remoteProcess: "unknown",
      reason: { kind: "restore_failed", code: "test_restore_failed" },
    });

    state = apply(state, { type: "continueOrdinary", tabId: "tab", operationId: "op" });
    expect(current(state)).toMatchObject({
      status: "failed",
      choice: "ordinary",
      ordinary: "new",
      originalSession: "not_restored",
      remoteProcess: "unknown",
      ssh: "preserved",
      existingDurable: "preserved",
    });
  });

  it("keeps cleanup failure visible beside the primary failure after an ordinary choice", () => {
    const state = apply(awaitingChoice(), {
      type: "continueOrdinary",
      tabId: "tab",
      operationId: "op",
    }, {
      type: "cleanupFailed",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      reason: { code: "read_only", message: "cannot remove temporary helper" },
    });

    expect(current(state)).toMatchObject({
      status: "failed",
      reason: { kind: "permanent", code: "test_permanent" },
      cleanupFailure: { kind: "cleanup_failed", code: "read_only", message: "cannot remove temporary helper" },
      ssh: "preserved",
    });
  });
});

describe("metadata evidence and tab isolation", () => {
  it("uses explicit observations and clears known values when evidence becomes unknown", () => {
    let state = apply(started(), {
      type: "observe",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + 50,
      runningVersion: { known: true, release: "v1", protocol: 7 },
      activity: { known: true, active: true },
    });
    expect(current(state)).toMatchObject({
      runningVersion: { known: true, release: "v1", protocol: 7 },
      activity: { known: true, active: true },
    });

    state = apply(state, {
      type: "observe",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + 100,
      runningVersion: { known: false },
      activity: { known: false },
    });
    expect(current(state).runningVersion).toEqual({ known: false });
    expect(current(state).activity).toEqual({ known: false });

    state = apply(state, {
      type: "succeeded",
      tabId: "tab",
      operationId: "op",
      attempt: 1,
      now: NOW + 200,
    });
    expect(current(state).runningVersion).toEqual({ known: false });
    expect(current(state).activity).toEqual({ known: false });
  });

  it("isolates concurrent tabs and their attempts", () => {
    let state = apply(createHelperOperationState(), startAction({
      tabId: "tab-a",
      operationId: "op-a",
      assetId: "asset-a",
    }), startAction({
      tabId: "tab-b",
      operationId: "op-b",
      kind: "reattach",
      originalTabId: "original-b",
    }));
    const initialB = current(state, "tab-b");

    state = apply(state, {
      type: "attemptFailed",
      tabId: "tab-a",
      operationId: "op-a",
      attempt: 1,
      now: NOW + 100,
      reason: failure("permanent"),
    });
    expect(current(state, "tab-b")).toBe(initialB);
    const failedA = current(state, "tab-a");

    state = apply(state, {
      type: "progress",
      tabId: "tab-b",
      operationId: "op-b",
      attempt: 1,
      now: NOW + 100,
      stage: "restore",
    });
    expect(current(state, "tab-a")).toBe(failedA);
    expect(current(state, "tab-b")).toMatchObject({
      operationId: "op-b",
      status: "running",
      stage: "restore",
      originalTabId: "original-b",
    });

    expect(apply(state, {
      type: "succeeded",
      tabId: "tab-b",
      operationId: "op-a",
      attempt: 1,
      now: NOW + 200,
    })).toBe(state);
  });
});

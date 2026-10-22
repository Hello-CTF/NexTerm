import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  helperOperation: vi.fn(),
  assetGet: vi.fn(),
  assetUpdate: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  terminalApi: { helperOperation: mocks.helperOperation },
  assetApi: { get: mocks.assetGet, update: mocks.assetUpdate },
  sessionApi: {},
  dbApi: {},
  vaultApi: {},
}));

import { createHelperOperationState, HELPER_DEFAULT_BUDGET_MS } from "../../features/terminal/helperOperationState";
import { useUi, type Workspace } from "../../app/store";
import type { HelperProgressEvent } from "../../ipc/events";

const NOW = Date.now();
const DEADLINE = NOW + HELPER_DEFAULT_BUDGET_MS;

function workspace(quickConnect = false): Workspace {
  return {
    id: "ws-1",
    kind: "session",
    title: "host",
    sessionId: "session-1",
    assetId: quickConnect ? undefined : "asset-1",
    assetKind: "ssh",
    quickTarget: quickConnect ? {
      id: "quick-1",
      host: "127.0.0.1",
      port: 22,
      username: "root",
      authKind: "password",
    } : undefined,
    panes: [{
      id: "pane-1",
      tabs: [{ id: "tab-view-1", kind: "terminal", title: "terminal", sessionId: "session-1", closable: true }],
      activeTabId: "tab-view-1",
    }],
    activePaneId: "pane-1",
    splitRatio: 0.5,
    closable: true,
  };
}

function seed(quickConnect = false): void {
  useUi.setState({
    workspaces: [workspace(quickConnect)],
    activeWorkspaceId: "ws-1",
    sessions: [{
      id: "session-1",
      assetId: quickConnect ? null : "asset-1",
      name: "host",
      kind: "ssh",
      status: "connected",
      tabs: [],
      createdAt: 0,
    }],
    helperProgress: {},
    helperOperations: createHelperOperationState(),
    helperOperationTabs: {},
    helperRetries: {},
    toasts: [],
  });
}

function progress(overrides: Partial<HelperProgressEvent> = {}): HelperProgressEvent {
  return {
    sessionId: "session-1",
    assetId: "asset-1",
    operationId: "operation-1",
    attempt: 1,
    attempts: 3,
    deadline: DEADLINE,
    step: 1,
    steps: 6,
    stage: "discover",
    transferred: 0,
    total: 0,
    percent: -1,
    done: false,
    ...overrides,
  };
}

function current() {
  const operation = useUi.getState().helperOperations.operations["tab-view-1"];
  if (!operation) throw new Error("missing helper operation");
  return operation;
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.helperOperation.mockResolvedValue({ accepted: true });
  mocks.assetGet.mockResolvedValue({ id: "asset-1", options: { encoding: "gbk" } });
  mocks.assetUpdate.mockImplementation(async (args) => args);
  seed();
});

describe("real helper progress integration", () => {
  it("reduces operation evidence, supersedes with a fresh operation and fences retired IDs", () => {
    const st = useUi.getState();
    st.setHelperProgress(progress({ path: "/remote/helper", transport: "exec", cause: "using exec" }));
    expect(current()).toMatchObject({
      operationId: "operation-1",
      tabId: "tab-view-1",
      status: "running",
      stage: "discover",
      attempt: 1,
      maxAttempts: 3,
      deadline: DEADLINE,
      path: "/remote/helper",
      transport: "exec",
    });

    st.setHelperProgress(progress({ stage: "backoff", cause: "temporary", step: 6 }));
    st.setHelperProgress(progress({ stage: "upload", attempt: 2, cause: "second attempt" }));
    expect(current()).toMatchObject({ operationId: "operation-1", attempt: 2, stage: "upload" });

    st.setHelperProgress(progress({ operationId: "operation-2", attempt: 1, stage: "ready", done: true }));
    expect(current()).toMatchObject({ operationId: "operation-2", attempt: 1, status: "succeeded" });

    st.setHelperProgress(progress({ operationId: "operation-1", attempt: 1, stage: "failed", done: true, cause: "late old attempt" }));
    expect(current()).toMatchObject({ operationId: "operation-2", attempt: 1, status: "succeeded" });
  });

  it("cancels the real backend operation and then ignores its late success", async () => {
    const st = useUi.getState();
    st.setHelperProgress(progress());
    await st.cancelHelperOperation("tab-view-1");

    expect(mocks.helperOperation).toHaveBeenCalledExactlyOnceWith("session-1", "operation-1", "cancel");
    expect(current()).toMatchObject({ status: "canceled", choice: "canceled", ssh: "preserved", existingDurable: "preserved" });
    expect(mocks.assetUpdate).not.toHaveBeenCalled();

    st.setHelperProgress(progress({ stage: "ready", done: true }));
    expect(current().status).toBe("canceled");
  });

  it("retries through the backend and adopts only the fresh operation with its fresh deadline", async () => {
    const st = useUi.getState();
    st.setHelperProgress(progress());
    st.setHelperProgress(progress({ stage: "failed", done: true, cause: "permanent failure" }));
    expect(current().status).toBe("awaiting_choice");

    await st.retryHelperOperation("tab-view-1");
    expect(mocks.helperOperation).toHaveBeenCalledExactlyOnceWith("session-1", "operation-1", "retry");
    expect(current()).toMatchObject({ operationId: "operation-1", status: "awaiting_choice" });

    const retryDeadline = Date.now() + HELPER_DEFAULT_BUDGET_MS;
    st.setHelperProgress(progress({ operationId: "operation-2", deadline: retryDeadline, stage: "discover" }));
    expect(current()).toMatchObject({ operationId: "operation-2", status: "running", attempt: 1, deadline: retryDeadline });

    st.setHelperProgress(progress({ operationId: "operation-1", stage: "ready", done: true }));
    expect(current()).toMatchObject({ operationId: "operation-2", status: "running" });
  });

  it("persists remember-regular only after an explicit asset write and preserves unrelated options", async () => {
    const st = useUi.getState();
    st.setHelperProgress(progress());
    st.setHelperProgress(progress({ stage: "failed", done: true, cause: "permanent failure" }));

    await st.continueHelperOrdinary("tab-view-1", true);

    expect(mocks.assetUpdate).toHaveBeenCalledExactlyOnceWith({
      id: "asset-1",
      options: { encoding: "gbk", terminalMode: "regular" },
    });
    expect(current()).toMatchObject({ status: "failed", choice: "ordinary", ordinary: "existing", existingDurable: "preserved" });
    expect(useUi.getState().helperOperations.regularByAsset["asset-1"]).toEqual({
      appliesTo: "new_terminals",
      existingDurable: "preserved",
    });
  });

  it("does not write memory for quick connect or for cancellation", async () => {
    seed(true);
    const st = useUi.getState();
    st.setHelperProgress(progress());
    st.setHelperProgress(progress({ stage: "failed", done: true, cause: "permanent failure" }));

    await st.continueHelperOrdinary("tab-view-1", true);
    await st.cancelHelperOperation("tab-view-1");

    expect(mocks.assetGet).not.toHaveBeenCalled();
    expect(mocks.assetUpdate).not.toHaveBeenCalled();
    expect(mocks.helperOperation).not.toHaveBeenCalled();
    expect(useUi.getState().helperOperations.regularByAsset).toEqual({});
  });
});

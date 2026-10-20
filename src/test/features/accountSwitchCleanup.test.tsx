/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AccountUser } from "../../ipc/authApi";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return { sessionList: vi.fn() };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    sessionApi: { ...actual.sessionApi, list: mocks.sessionList },
  };
});

import { QueryClient } from "@tanstack/react-query";
import { useAuth } from "../../features/auth/store";
import { useUi } from "../../app/store";
import { subscribeAccountSwitchCleanup } from "../../app/accountSwitch";
import { useAssetReachability } from "../../features/explorer/assetReachability";
import { isTabUnread, setTabUnread } from "../../app/unreadStore";

const USER_A: AccountUser = {
  id: "u-a",
  username: "alice",
  display_name: "Alice",
  role: "user",
  state: "active",
  must_change_password: false,
  mfa_enabled: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};
const USER_B: AccountUser = { ...USER_A, id: "u-b", username: "bob", display_name: "Bob" };

let queryClient: QueryClient;
let unwire: (() => void) | undefined;

function authReady(user: AccountUser | null): void {
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user,
    dek: null,
    gate: user ? "ready" : "login",
    error: null,
    cleanupError: null,
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.sessionList.mockResolvedValue([]);
  queryClient = new QueryClient();
  // Establish the signed-in baseline before subscribing so the seed describes account A's session.
  authReady(USER_A);
  unwire = subscribeAccountSwitchCleanup(useAuth, queryClient);
  useUi.setState({
    workspaces: [{
      id: "ws-a",
      kind: "session",
      title: "A workspace",
      sessionId: "s-a",
      assetId: "a-1",
      assetKind: "ssh",
      panes: [{ id: "p1", tabs: [], activeTabId: null }],
      activePaneId: "p1",
      splitRatio: 0.5,
      closable: true,
    }],
    activeWorkspaceId: "ws-a",
    sessions: [{ id: "s-a", assetId: "a-1", name: "A", kind: "ssh", status: "connected", tabs: [], createdAt: 1 }],
    connectionProblems: {},
    toasts: [],
  });
  queryClient.setQueryData(["inventory-ssh-watch"], { watching: true, path: "/home/a/.ssh/config" });
  queryClient.setQueryData(["assets"], [{ id: "a-1" }]);
  useAssetReachability.setState({
    entries: { "a-1": { state: "reachable", at: 1 } },
    batchError: null,
  });
  setTabUnread("t-a", true);
});

afterEach(() => {
  unwire?.();
  unwire = undefined;
  authReady(null);
  useUi.setState({ workspaces: [], sessions: [], activeWorkspaceId: null, toasts: [] });
  useAssetReachability.getState().reset();
  queryClient.clear();
});

describe("account switch cleanup", () => {
  it("clears account-scoped caches on the first frame of the switch", () => {
    const epochBefore = useUi.getState().accountEpoch;

    authReady(USER_B);

    expect(useUi.getState().accountEpoch).toBe(epochBefore + 1);
    expect(useUi.getState().workspaces).toEqual([]);
    expect(useUi.getState().activeWorkspaceId).toBeNull();
    expect(useUi.getState().sessions).toEqual([]);
    expect(useUi.getState().connectionProblems).toEqual({});
    expect(useAssetReachability.getState().entries).toEqual({});
    expect(isTabUnread("t-a")).toBe(false);
    expect(queryClient.getQueryData(["inventory-ssh-watch"])).toBeUndefined();
    expect(queryClient.getQueryData(["assets"])).toBeUndefined();
  });

  it("settles pending global dialogs with cancel results on account switch", () => {
    const textResolve = vi.fn();
    const dialogResolve = vi.fn();
    const choiceResolve = vi.fn();
    useUi.setState({
      textPrompt: { message: "m", value: "", resolve: textResolve },
      appDialog: { kind: "confirm", message: "m", level: "warning", resolve: dialogResolve },
      appChoice: { title: "t", message: "m", options: [], level: "warning", resolve: choiceResolve },
    });

    authReady(USER_B);

    expect(textResolve).toHaveBeenCalledWith(null);
    expect(dialogResolve).toHaveBeenCalledWith(false);
    expect(choiceResolve).toHaveBeenCalledWith(null, false);
    expect(useUi.getState().textPrompt).toBeNull();
    expect(useUi.getState().appDialog).toBeNull();
    expect(useUi.getState().appChoice).toBeNull();
  });

  it("does not clear caches for same-account gate transitions", () => {
    useAuth.setState({ gate: "login" });
    useAssetReachability.setState({ entries: { "a-1": { state: "reachable", at: 1 } }, batchError: null });
    setTabUnread("t-a", true);
    const epochBefore = useUi.getState().accountEpoch;

    authReady(USER_A);

    expect(useUi.getState().accountEpoch).toBe(epochBefore);
    expect(useAssetReachability.getState().entries).toEqual({ "a-1": { state: "reachable", at: 1 } });
    expect(isTabUnread("t-a")).toBe(true);
  });

  it("discards an in-flight session list from account A after the switch", async () => {
    let resolveList!: (value: unknown) => void;
    mocks.sessionList.mockImplementation(
      () => new Promise((resolve) => {
        resolveList = resolve;
      }),
    );
    const resync = useUi.getState().resyncSessions();
    authReady(USER_B);
    resolveList([{ id: "s-a", assetId: "a-1", name: "A", kind: "ssh", status: "connected", tabs: [], createdAt: 1 }]);
    await resync;

    expect(useUi.getState().sessions).toEqual([]);
  });
});

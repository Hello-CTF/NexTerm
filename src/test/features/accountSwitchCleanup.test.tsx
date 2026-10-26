/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AccountUser } from "../../ipc/authApi";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return { sessionList: vi.fn(), snippetList: vi.fn(), snippetDelete: vi.fn() };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    sessionApi: { ...actual.sessionApi, list: mocks.sessionList },
    assetApi: {
      ...actual.assetApi,
      snippetList: mocks.snippetList,
      snippetDelete: mocks.snippetDelete,
    },
  };
});

import { QueryClient } from "@tanstack/react-query";
import { createElement } from "react";
import { useAuth } from "../../features/auth/store";
import { useUi } from "../../app/store";
import { subscribeAccountSwitchCleanup } from "../../app/accountSwitch";
import { useAssetReachability } from "../../features/explorer/assetReachability";
import { isTabUnread, setTabUnread } from "../../app/unreadStore";
import {
  ask,
  askChoice,
  promptText,
  registerDialogEpochSource,
  registerDialogHandlers,
  registerPromptHandler,
} from "../../ui/dialogs";
import { SnippetsPanel } from "../../features/explorer/SnippetsPanel";
import { click, flush, mountWithClient, waitFor, type MountedView } from "./reactTestUtils";

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
  mocks.snippetList.mockResolvedValue([]);
  mocks.snippetDelete.mockResolvedValue(undefined);
  queryClient = new QueryClient();
  // Wire the global dialogs to the store exactly like the App shell does.
  registerPromptHandler((message, value, options) =>
    new Promise<string | null>((resolve) => {
      useUi.getState().openTextPrompt({ message, value, ...options, resolve });
    }),
  );
  registerDialogEpochSource(() => useUi.getState().accountEpoch);
  registerDialogHandlers({
    ask: (message, options) =>
      new Promise<boolean>((resolve) => {
        useUi.getState().openAppDialog({
          kind: "ask",
          message,
          title: options?.title,
          level: options?.kind === "warning" ? "warning" : "info",
          resolve,
        });
      }),
    confirm: (message) =>
      new Promise<boolean>((resolve) => {
        useUi.getState().openAppDialog({ kind: "confirm", message, level: "warning", resolve });
      }),
    message: (message) =>
      new Promise<void>((resolve) => {
        useUi.getState().openAppDialog({ kind: "message", message, level: "info", resolve: () => resolve() });
      }),
    choose: (message, options) =>
      new Promise<{ key: string; remember: boolean } | null>((resolve) => {
        useUi.getState().openAppChoice({
          title: options?.title ?? "请选择",
          message,
          options: options?.choices ?? [],
          level: options?.level ?? "info",
          rememberLabel: options?.rememberLabel,
          resolve: (key, remember) => resolve(key === null ? null : { key, remember }),
        });
      }),
  });
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
  useUi.setState({
    workspaces: [],
    sessions: [],
    activeWorkspaceId: null,
    toasts: [],
    accountEpoch: 0,
    textPrompt: null,
    appDialog: null,
    appChoice: null,
  });
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

describe("queued dialog account isolation", () => {
  it("settles two queued asks plus a prompt and a choice with cancel on switch without reopening old dialogs", async () => {
    const firstAsk = ask("first ask", { kind: "warning" });
    const secondAsk = ask("second ask", { kind: "warning" });
    const prompt = promptText("prompt message");
    const choice = askChoice("choose", { choices: [{ key: "k", label: "option" }] });
    await flush();

    // The first ask is active; everything else is queued behind it.
    expect(useUi.getState().appDialog?.message).toBe("first ask");

    authReady(USER_B);

    // The active dialog settles with cancel; every stale queued task is dropped with the same
    // cancellation result instead of starting: no old dialog reopens in B.
    await expect(firstAsk).resolves.toBe(false);
    await expect(secondAsk).resolves.toBe(false);
    await expect(prompt).resolves.toBeNull();
    await expect(choice).resolves.toBeNull();
    await flush();
    expect(useUi.getState().appDialog).toBeNull();
    expect(useUi.getState().textPrompt).toBeNull();
    expect(useUi.getState().appChoice).toBeNull();
  });

  it("does not delete B's snippet when A's two queued delete confirmations settle after a switch", async () => {
    mocks.snippetList.mockResolvedValue([{ id: "shared-snippet", name: "A snippet", body: "echo hi", groupId: null, createdAt: 1, updatedAt: 1 }]);
    let mounted: MountedView | undefined;
    try {
      mounted = mountWithClient(createElement(SnippetsPanel, { onClose: () => undefined }));
      await waitFor(() => expect(mocks.snippetList).toHaveBeenCalled());
      await waitFor(() => expect(mounted?.container.textContent).toContain("A snippet"));

      // Click delete twice: the second confirmation queues behind the first.
      const deleteButton = mounted.container.querySelector<HTMLButtonElement>('button[aria-label="删除片段「A snippet」"]');
      if (!deleteButton) throw new Error("delete button not found");
      click(deleteButton);
      click(deleteButton);
      await waitFor(() => expect(useUi.getState().appDialog?.message).toContain("删除片段"));

      // Account switch: the queued old confirmations must never fire a delete in B.
      authReady(USER_B);
      await flush();

      expect(mocks.snippetDelete).not.toHaveBeenCalled();
      expect(useUi.getState().appDialog).toBeNull();
    } finally {
      mounted?.unmount();
    }
  });
});

/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, deferred, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  env: vi.fn(),
  list: vi.fn(),
  create: vi.fn(),
  createSocks: vi.fn(),
  remove: vi.fn(),
  ask: vi.fn(),
  realAsk: null as null | ((message: string, options?: { title?: string; kind?: "info" | "warning" | "error" }) => Promise<boolean>),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    forwardApi: {
      env: mocks.env,
      list: mocks.list,
      create: mocks.create,
      createSocks: mocks.createSocks,
      remove: mocks.remove,
    },
    dbApi: {},
    sessionApi: {},
    vaultApi: {},
    terminalApi: {},
  };
});
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  mocks.realAsk = actual.ask;
  return { ...actual, ask: mocks.ask };
});

import { ForwardPanel } from "../../features/forward/ForwardPanel";
import { useUi } from "../../app/store";
import { dialogLevelForKind } from "../../app/App";
import { registerDialogHandlers } from "../../ui/dialogs";
import { DialogHost } from "../../ui/DialogHost";

async function mountForward(): Promise<MountedView> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const mounted = mount(
    createElement(QueryClientProvider, { client }, createElement(ForwardPanel, { sessionId: "s" })),
  );
  clickButton(mounted.container, "SOCKS5 动态转发");
  return mounted;
}

describe("SOCKS exposure confirmation", () => {
  let mounted: MountedView | undefined;
  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    mocks.list.mockResolvedValue([]);
    mocks.createSocks.mockResolvedValue({
      id: "f1",
      sessionId: "s",
      listenPort: 1080,
      targetHost: null,
      targetPort: null,
      kind: "socks",
      createdAt: 1,
    });
    useUi.setState({ pushToast: mocks.toast, sessions: [] });
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("requires explicit acknowledgement before creating a non-loopback open proxy", async () => {
    mocks.env.mockResolvedValue({ available: true, platform: "other", listenHost: "0.0.0.0" });
    mocks.createSocks
      .mockRejectedValueOnce({
        code: "needs_confirm",
        message: "需要确认",
        detail: { risk: "unauthenticated_exposed_socks", listenHost: "0.0.0.0", authentication: "none" },
      })
      .mockResolvedValueOnce({ id: "f1", kind: "socks" });
    mocks.ask.mockResolvedValue(true);
    mounted = await mountForward();
    clickButton(mounted.container, "创建");

    await waitFor(() => expect(mocks.createSocks).toHaveBeenCalledTimes(2));
    expect(mocks.createSocks).toHaveBeenNthCalledWith(1, "s", 1080);
    expect(mocks.createSocks).toHaveBeenNthCalledWith(2, "s", 1080, true);
    expect(mocks.ask).toHaveBeenCalledWith(expect.stringMatching(/无认证.*开放 SOCKS5 代理/), {
      title: "确认开放代理风险",
      kind: "warning",
    });
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringMatching(/SOCKS5 代理已就绪/));
  });

  it("keeps desktop loopback creation free of the additional confirmation", async () => {
    mocks.env.mockResolvedValue({ available: true, platform: "other", listenHost: "127.0.0.1" });
    mounted = await mountForward();
    clickButton(mounted.container, "创建");

    await waitFor(() => expect(mocks.createSocks).toHaveBeenCalledOnce());
    expect(mocks.createSocks).toHaveBeenCalledWith("s", 1080);
    expect(mocks.ask).not.toHaveBeenCalled();
  });

  it("does not retry or claim success when the user rejects the exposure", async () => {
    mocks.env.mockResolvedValue({ available: true, platform: "other", listenHost: "0.0.0.0" });
    mocks.createSocks.mockRejectedValueOnce({ code: "needs_confirm", message: "需要确认" });
    mocks.ask.mockResolvedValue(false);
    mounted = await mountForward();
    clickButton(mounted.container, "创建");

    await waitFor(() => expect(mocks.ask).toHaveBeenCalledOnce());
    expect(mocks.createSocks).toHaveBeenCalledOnce();
    expect(mocks.toast).not.toHaveBeenCalledWith("success", expect.any(String));
  });

  it("uses the authoritative public address when env resolves after creation starts", async () => {
    const env = deferred<{ available: boolean; platform: string; listenHost: string }>();
    mocks.env.mockReturnValue(env.promise);
    mocks.createSocks
      .mockRejectedValueOnce({
        code: "needs_confirm",
        message: "需要确认",
        detail: { listenHost: "0.0.0.0", authentication: "none" },
      })
      .mockResolvedValueOnce({ id: "f1", kind: "socks" });
    mocks.ask.mockResolvedValue(true);
    mounted = await mountForward();
    clickButton(mounted.container, "创建");

    await waitFor(() => expect(mocks.createSocks).toHaveBeenCalledTimes(2));
    const warning = mocks.ask.mock.calls[0][0] as string;
    expect(warning).toContain("0.0.0.0:1080");
    expect(warning).not.toContain("127.0.0.1");
    expect(mocks.toast).toHaveBeenCalledWith(
      "success",
      expect.stringMatching(/0\.0\.0\.0:1080/),
    );
    env.resolve({ available: true, platform: "other", listenHost: "0.0.0.0" });
  });
});

describe("停止转发确认（R42, real DialogHost）", () => {
  let mounted: MountedView | undefined;
  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    mocks.env.mockResolvedValue({ available: true, platform: "other", listenHost: "127.0.0.1" });
    mocks.list.mockResolvedValue([]);
    mocks.remove.mockResolvedValue(undefined);
    useUi.setState({ pushToast: mocks.toast, sessions: [], appDialog: null });
    mocks.ask.mockImplementation((message: string, options?: { title?: string; kind?: "info" | "warning" | "error" }) =>
      mocks.realAsk!(message, options),
    );
    registerDialogHandlers({
      ask: (message, options) =>
        new Promise<boolean>((resolve) => {
          useUi.getState().openAppDialog({
            kind: "ask",
            message,
            title: options?.title,
            level: dialogLevelForKind(options?.kind),
            resolve,
          });
        }),
      confirm: (message) =>
        new Promise<boolean>((resolve) => {
          useUi.getState().openAppDialog({ kind: "confirm", message, level: "warning", resolve });
        }),
      message: (message) =>
        new Promise<void>((resolve) => {
          useUi
            .getState()
            .openAppDialog({ kind: "message", message, level: "info", resolve: () => resolve() });
        }),
      choose: vi.fn(),
    });
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("renders an alertdialog before dropping established connections, cancel aborts", async () => {
    mocks.list.mockResolvedValue([
      {
        id: "f9",
        sessionId: "s",
        listenPort: 13306,
        targetHost: "10.0.0.8",
        targetPort: 3306,
        kind: "local",
        createdAt: 1,
      },
    ]);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const m = mount(
      createElement(
        QueryClientProvider,
        { client },
        createElement("div", null, createElement(ForwardPanel, { sessionId: "s" }), createElement(DialogHost)),
      ),
    );
    mounted = m;
    await waitFor(() => expect(m.container.textContent).toContain("10.0.0.8"));

    async function openModal(): Promise<HTMLElement> {
      await waitFor(() =>
        expect(m.container.querySelector(".nx-modal")).not.toBeNull(),
      );
      return m.container.querySelector<HTMLElement>(".nx-modal")!;
    }
    async function closeModal(modal: HTMLElement, button: "取消" | "确定"): Promise<void> {
      clickButton(modal, button);
      await waitFor(() => expect(m.container.querySelector(".nx-modal")).toBeNull());
    }

    clickButton(m.container, "停止");
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("已经建立的连接会被断开");
    await closeModal(modal, "取消");
    expect(mocks.remove).not.toHaveBeenCalled();

    clickButton(m.container, "停止");
    const modal2 = await openModal();
    expect(modal2.getAttribute("role")).toBe("alertdialog");
    await closeModal(modal2, "确定");
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith("f9"));
  });
});

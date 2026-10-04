/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  remove: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    mountApi: {
      list: mocks.list,
      create: mocks.create,
      remove: mocks.remove,
    },
    sessionApi: {},
  };
});

import { registerDialogHandlers } from "../../ui/dialogs";
import { DialogHost } from "../../ui/DialogHost";
import { MountPanel } from "../../features/files/MountPanel";
import { useUi } from "../../app/store";
import { dialogLevelForKind } from "../../app/App";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.list.mockResolvedValue([
    { id: "m1", localPoint: "Z:", remote: "\\\\nas\\share", sessionId: "s1", createdAt: 1 },
  ]);
  mocks.remove.mockResolvedValue(undefined);
  useUi.setState({ pushToast: mocks.toast, sessions: [], appDialog: null });
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

function mountPanelWithDialogHost(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(
        "div",
        null,
        createElement(MountPanel, { sessionId: "s1" }),
        createElement(DialogHost),
      ),
    ),
  );
}

describe("断开挂载确认（R42 审计结论：显式 info）", () => {
  it("renders a plain info dialog (role=dialog, not alertdialog), cancel aborts", async () => {
    mounted = mountPanelWithDialogHost();
    await waitFor(() => expect(mounted!.container.textContent).toContain("nas"));

    clickButton(mounted!.container, "断开");
    await waitFor(() => expect(mounted!.container.querySelector(".nx-modal")).not.toBeNull());
    const modal = mounted!.container.querySelector(".nx-modal")!;
    expect(modal.getAttribute("role")).toBe("dialog");
    expect(modal.querySelector(".nx-modal-body")?.textContent).toContain("断开 Z:？");

    clickButton(modal, "取消");
    await waitFor(() => expect(mounted!.container.querySelector(".nx-modal")).toBeNull());
    expect(mocks.remove).not.toHaveBeenCalled();
  });

  it("confirm proceeds through the same info dialog", async () => {
    mounted = mountPanelWithDialogHost();
    await waitFor(() => expect(mounted!.container.textContent).toContain("nas"));

    clickButton(mounted!.container, "断开");
    await waitFor(() => expect(mounted!.container.querySelector(".nx-modal")).not.toBeNull());
    const modal = mounted!.container.querySelector(".nx-modal")!;
    expect(modal.getAttribute("role")).toBe("dialog");

    clickButton(modal, "确定");
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith("Z:", "s1"));
  });

  it("a failed disconnect surfaces the real error with the standard prefix", async () => {
    mocks.remove.mockRejectedValueOnce(new Error("device busy"));
    mounted = mountPanelWithDialogHost();
    await waitFor(() => expect(mounted!.container.textContent).toContain("nas"));

    clickButton(mounted!.container, "断开");
    await waitFor(() => expect(mounted!.container.querySelector(".nx-modal")).not.toBeNull());
    clickButton(mounted!.container.querySelector(".nx-modal")!, "确定");
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith("Z:", "s1"));
    expect(mocks.toast).toHaveBeenCalledWith("error", "断开失败：device busy");
  });
});

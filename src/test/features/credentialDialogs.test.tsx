/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  status: vi.fn(),
  listCredentials: vi.fn(),
  revealCredential: vi.fn(),
  updateCredential: vi.fn(),
  deleteCredential: vi.fn(),
  ask: vi.fn(),
  realAsk: null as null | ((message: string, options?: { title?: string; kind?: "info" | "warning" | "error" }) => Promise<boolean>),
  promptText: vi.fn(),
  pickKeyFile: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    vaultApi: {
      status: mocks.status,
      listCredentials: mocks.listCredentials,
      revealCredential: mocks.revealCredential,
      updateCredential: mocks.updateCredential,
      deleteCredential: mocks.deleteCredential,
    },
    assetApi: {},
    sessionApi: {},
    terminalApi: {},
    dbApi: {},
  };
});
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  mocks.realAsk = actual.ask;
  return {
    ...actual,
    ask: mocks.ask,
    promptText: mocks.promptText,
    pickKeyFile: mocks.pickKeyFile,
  };
});

import { CredentialsPanel } from "../../features/credentials/CredentialsPanel";
import type { Credential } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { dialogLevelForKind } from "../../app/App";
import { registerDialogHandlers } from "../../ui/dialogs";
import { DialogHost } from "../../ui/DialogHost";

const CRED: Credential = {
  id: "c1",
  name: "部署密钥",
  kind: "private_key",
  createdAt: 1,
  updatedAt: 1,
  usedBy: [{ id: "a1", name: "web-1", kind: "ssh" }],
  source: "inline",
  refPath: null,
  hasPassphrase: true,
};

function mountPanel(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(
        "div",
        null,
        createElement(CredentialsPanel, { credId: "c1" }),
        createElement(DialogHost),
      ),
    ),
  );
}

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.status.mockResolvedValue({ initialized: true, unlocked: true, mode: "master" });
  mocks.listCredentials.mockResolvedValue([{ ...CRED }]);
  mocks.updateCredential.mockResolvedValue(undefined);
  mocks.deleteCredential.mockResolvedValue(undefined);
  useUi.setState({ pushToast: mocks.toast, appDialog: null });
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

describe("凭据 destructive 确认（R42, real DialogHost）", () => {
  async function openModal(): Promise<HTMLElement> {
    await waitFor(() =>
      expect(mounted!.container.querySelector(".nx-modal")).not.toBeNull(),
    );
    return mounted!.container.querySelector<HTMLElement>(".nx-modal")!;
  }

  async function closeModal(modal: HTMLElement, button: "取消" | "确定"): Promise<void> {
    clickButton(modal, button);
    await waitFor(() =>
      expect(mounted!.container.querySelector(".nx-modal")).toBeNull(),
    );
  }

  it("清除口令：警示浮层（alertdialog），取消即中止", async () => {
    mounted = mountPanel();
    await waitFor(() => expect(mounted!.container.textContent).toContain("使用它的资产"));

    clickButton(mounted!.container, "清除口令");
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("清除这条私钥的口令");
    await closeModal(modal, "取消");
    expect(mocks.updateCredential).not.toHaveBeenCalled();

    clickButton(mounted!.container, "清除口令");
    const modal2 = await openModal();
    await closeModal(modal2, "确定");
    await waitFor(() =>
      expect(mocks.updateCredential).toHaveBeenCalledWith("c1", { passphrase: "" }),
    );
  });

  it("删除凭据：警示浮层并讲清引用影响，取消即中止", async () => {
    mounted = mountPanel();
    await waitFor(() => expect(mounted!.container.textContent).toContain("使用它的资产"));

    clickButton(mounted!.container, "删除");
    const modal = await openModal();
    expect(modal.getAttribute("role")).toBe("alertdialog");
    expect(modal.textContent).toContain("1 个资产正在使用");
    await closeModal(modal, "取消");
    expect(mocks.deleteCredential).not.toHaveBeenCalled();

    clickButton(mounted!.container, "删除");
    const modal2 = await openModal();
    await closeModal(modal2, "确定");
    await waitFor(() => expect(mocks.deleteCredential).toHaveBeenCalledWith("c1"));
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("引用已置空"));
  });
});

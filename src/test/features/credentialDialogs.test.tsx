/** @vitest-environment jsdom */
//
// R42：凭据详情页的 destructive 确认（清除私钥口令 / 删除凭据）走共享 warning 语义。
// 两处的危害都是不可逆的：口令清掉后需要口令的私钥即无法连接；凭据删除后密钥材料
// 即丢失（引用置空但资产保留）。取消必须原样中止，不发任何写 RPC。

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
  promptText: vi.fn(),
  pickKeyFile: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", () => ({
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
}));
vi.mock("../../ui/dialogs", () => ({
  ask: mocks.ask,
  promptText: mocks.promptText,
  pickKeyFile: mocks.pickKeyFile,
}));

import { CredentialsPanel } from "../../features/credentials/CredentialsPanel";
import type { Credential } from "../../ipc/commands";
import { useUi } from "../../app/store";

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
      createElement(CredentialsPanel, { credId: "c1" }),
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
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("凭据 destructive 确认（R42）", () => {
  it("清除口令：warning 确认，取消即中止", async () => {
    mounted = mountPanel();
    await waitFor(() => expect(mounted!.container.textContent).toContain("使用它的资产"));

    mocks.ask.mockResolvedValueOnce(false);
    clickButton(mounted!.container, "清除口令");
    await waitFor(() => expect(mocks.ask).toHaveBeenCalledOnce());
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("清除这条私钥的口令"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.updateCredential).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    clickButton(mounted!.container, "清除口令");
    await waitFor(() =>
      expect(mocks.updateCredential).toHaveBeenCalledWith("c1", { passphrase: "" }),
    );
  });

  it("删除凭据：warning 确认并讲清引用影响，取消即中止", async () => {
    mounted = mountPanel();
    await waitFor(() => expect(mounted!.container.textContent).toContain("使用它的资产"));

    mocks.ask.mockResolvedValueOnce(false);
    clickButton(mounted!.container, "删除");
    await waitFor(() => expect(mocks.ask).toHaveBeenCalledOnce());
    expect(mocks.ask).toHaveBeenCalledWith(
      expect.stringContaining("1 个资产正在使用"),
      expect.objectContaining({ kind: "warning" }),
    );
    expect(mocks.deleteCredential).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    clickButton(mounted!.container, "删除");
    await waitFor(() => expect(mocks.deleteCredential).toHaveBeenCalledWith("c1"));
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("引用已置空"));
  });
});

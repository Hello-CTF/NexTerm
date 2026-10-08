/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  clickButton,
  deferred,
  flush,
  mount,
  setInputValue,
  setSelectValue,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  available: vi.fn(),
  pickBrowserFile: vi.fn(),
  pickKeyFile: vi.fn(),
  readKeyFile: vi.fn(),
  setCredential: vi.fn(),
  create: vi.fn(),
  groupList: vi.fn(),
  listCredentials: vi.fn(),
  vaultStatus: vi.fn(),
  unlock: vi.fn(),
  promptText: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/webFiles", () => ({
  browserFilesAvailable: mocks.available,
  pickBrowserFile: mocks.pickBrowserFile,
}));
vi.mock("../../ui/dialogs", () => ({
  pickKeyFile: mocks.pickKeyFile,
  ask: vi.fn(),
  promptText: mocks.promptText,
}));
vi.mock("../../ipc/commands", () => ({
  assetApi: {
    readKeyFile: mocks.readKeyFile,
    create: mocks.create,
    groupList: mocks.groupList,
  },
  vaultApi: {
    setCredential: mocks.setCredential,
    listCredentials: mocks.listCredentials,
    status: mocks.vaultStatus,
    unlock: mocks.unlock,
  },
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
}));

import { NewCredentialModal } from "../../features/credentials/NewCredentialModal";
import { AssetEditor } from "../../features/explorer/AssetTree";
import { useUi } from "../../app/store";

type FormKind = "credential" | "asset";

async function mountInlineForm(kind: FormKind): Promise<MountedView> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const node =
    kind === "credential"
      ? createElement(NewCredentialModal, { onClose: vi.fn(), onSaved: vi.fn() })
      : createElement(AssetEditor, {
          kind: "asset",
          onClose: vi.fn(),
          onSaved: vi.fn(),
          presetGroupId: null,
        });
  const mounted = mount(createElement(QueryClientProvider, { client }, node));
  if (kind === "credential") {
    clickButton(mounted.container, "私钥");
  } else {
    const auth = [...mounted.container.querySelectorAll("select")].find(
      (select) => select.value === "password",
    );
    if (!auth) throw new Error("Auth select not found");
    setSelectValue(auth, "key");
  }
  clickButton(mounted.container, "存入凭据库");
  return mounted;
}

function inlineInput(container: HTMLElement): HTMLInputElement {
  const input = container.querySelector<HTMLInputElement>('input[placeholder="选择私钥文件"]');
  if (!input) throw new Error("Inline path input not found");
  return input;
}

describe.each<FormKind>(["credential", "asset"])("%s inline key picker ownership", (kind) => {
  let mounted: MountedView | undefined;
  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    mocks.available.mockReturnValue(true);
    mocks.groupList.mockResolvedValue([]);
    mocks.listCredentials.mockResolvedValue([]);
    mocks.readKeyFile.mockResolvedValue("NATIVE KEY");
    mocks.vaultStatus.mockResolvedValue({ initialized: true, mode: "master", unlocked: true });
    mocks.unlock.mockResolvedValue(undefined);
    mocks.promptText.mockResolvedValue(null);
    useUi.setState({ pushToast: mocks.toast });
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("ignores a deferred File.text result after switching to reference mode", async () => {
    const text = deferred<string>();
    mocks.pickBrowserFile.mockResolvedValue({ name: "browser-id", size: 100, text: () => text.promise });
    mounted = await mountInlineForm(kind);
    clickButton(mounted.container, "浏览…");
    clickButton(mounted.container, "引用本地文件");
    text.resolve("BROWSER KEY");
    await flush();
    expect(mounted.container.textContent).not.toContain("browser-id");
    expect(mocks.setCredential).not.toHaveBeenCalled();
    expect(mocks.create).not.toHaveBeenCalled();
    const reference = mounted.container.querySelector<HTMLInputElement>(
      kind === "credential" ? 'input[placeholder="C:\\Users\\you\\.ssh\\id_rsa"]' : 'input[placeholder="选择或输入私钥路径"]',
    );
    expect(reference?.value ?? "").toBe("");
  });

  it("does not overwrite a manually edited path when deferred File.text resolves", async () => {
    const text = deferred<string>();
    mocks.pickBrowserFile.mockResolvedValue({ name: "browser-id", size: 100, text: () => text.promise });
    mounted = await mountInlineForm(kind);
    const input = inlineInput(mounted.container);
    clickButton(mounted.container, "浏览…");
    setInputValue(input, "/manual/private-key");
    text.resolve("BROWSER KEY");
    await flush();
    expect(input.value).toBe("/manual/private-key");
    expect(input.value).not.toBe("browser-id");
  });

  it("surfaces oversized-file rejection through the form toast", async () => {
    const text = vi.fn(async () => "NOT A KEY");
    mocks.pickBrowserFile.mockResolvedValue({ name: "large.bin", size: 64 * 1024 + 1, text });
    mounted = await mountInlineForm(kind);
    clickButton(mounted.container, "浏览…");
    await waitFor(() => {
      expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringMatching(/64KB/));
    });
    expect(text).not.toHaveBeenCalled();
  });

  it("surfaces File.text rejection through the form toast", async () => {
    mocks.pickBrowserFile.mockResolvedValue({
      name: "id_ed25519",
      size: 100,
      text: async () => Promise.reject(new Error("browser read failed")),
    });
    mounted = await mountInlineForm(kind);
    clickButton(mounted.container, "浏览…");
    await waitFor(() => {
      expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringMatching(/browser read failed/));
    });
  });
});

/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, mount, setSelectValue, type MountedView } from "./reactTestUtils";

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

function mountKeyForm(kind: FormKind): MountedView {
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
  return mounted;
}

function refPathInput(container: HTMLElement, kind: FormKind): HTMLInputElement {
  const placeholder =
    kind === "credential" ? "C:\\Users\\you\\.ssh\\id_rsa" : "选择或输入私钥路径";
  const input = [...container.querySelectorAll<HTMLInputElement>("input")].find(
    (candidate) => candidate.placeholder === placeholder,
  );
  if (!input) throw new Error("ref path input not found");
  return input;
}

function inlineFileInput(container: HTMLElement): HTMLInputElement | null {
  return container.querySelector<HTMLInputElement>('input[placeholder="选择私钥文件"]');
}

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
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

describe.each<FormKind>(["credential", "asset"])("Web 模式 %s 私钥来源", (kind) => {
  it("默认「存入凭据库」，不再把暂存路径当本地路径引用", () => {
    mocks.available.mockReturnValue(true);
    mounted = mountKeyForm(kind);
    expect(inlineFileInput(mounted.container)).not.toBeNull();
    expect(mocks.pickKeyFile).not.toHaveBeenCalled();
  });

  it("引用本地文件面板没有浏览按钮，只保留服务器路径输入", () => {
    mocks.available.mockReturnValue(true);
    mounted = mountKeyForm(kind);
    clickButton(mounted.container, "引用本地文件");
    const input = refPathInput(mounted.container, kind);
    expect(input.closest("div")?.querySelector("button")).toBeNull();
    expect(mounted.container.textContent).toContain("服务器上的私钥路径");
    expect(mocks.pickKeyFile).not.toHaveBeenCalled();
  });
});

describe.each<FormKind>(["credential", "asset"])("桌面模式 %s 私钥来源", (kind) => {
  it("保留「浏览…」经原生对话框写入路径引用", async () => {
    mocks.available.mockReturnValue(false);
    mocks.pickKeyFile.mockResolvedValue("/keys/id_rsa");
    mounted = mountKeyForm(kind);
    const input = refPathInput(mounted.container, kind);
    const browse = input.closest("div")?.querySelector<HTMLButtonElement>("button");
    expect(browse?.textContent).toBe("浏览…");
    if (!browse) throw new Error("browse button not found");
    click(browse);
    await flush();
    expect(mocks.pickKeyFile).toHaveBeenCalledOnce();
    expect(input.value).toBe("/keys/id_rsa");
  });
});

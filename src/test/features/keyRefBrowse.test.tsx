/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  clickButton,
  deferred,
  flush,
  flushUntil,
  mount,
  chooseSelectOption,
  type MountedView,
} from "./reactTestUtils";
import type { Asset, Credential } from "../../ipc/commands";

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

function mountKeyForm(kind: FormKind, initial?: Asset): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const node =
    kind === "credential"
      ? createElement(NewCredentialModal, { onClose: vi.fn(), onSaved: vi.fn() })
      : createElement(AssetEditor, {
          kind: "asset",
          initial,
          onClose: vi.fn(),
          onSaved: vi.fn(),
          presetGroupId: null,
        });
  const mounted = mount(createElement(QueryClientProvider, { client }, node));
  if (kind === "credential") {
    clickButton(mounted.container, "私钥");
  } else {
    const auth = mounted.container.querySelector<HTMLButtonElement>('button[aria-label="认证方式"][aria-haspopup="listbox"]');
    if (!auth) throw new Error("Auth select not found");
    if (!auth.textContent?.includes("私钥文件")) chooseSelectOption(mounted.container, "认证方式", "私钥文件");
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

const KEY_SOURCE_PLATFORMS = [
  ["desktop", false],
  ["Web", true],
] as const;

const EXISTING_KEY_ASSET: Asset = {
  id: "a1",
  groupId: null,
  kind: "ssh",
  name: "web-01",
  host: "10.0.0.8",
  port: 22,
  username: "root",
  authKind: "key",
  keyPath: "/keys/id_ed25519",
  credId: null,
  options: {},
  tags: "",
  note: "",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
  deletedAt: null,
  builtin: false,
};

const FILE_KEY_CREDENTIAL: Credential = {
  id: "k1",
  name: "部署密钥",
  kind: "private_key",
  createdAt: 1,
  updatedAt: 1,
  usedBy: [],
  source: "file",
  refPath: "/keys/id_ed25519",
  hasPassphrase: false,
};

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

describe("AssetEditor private key source defaults", () => {
  it("puts storing in the credential vault before referencing a file path", () => {
    mocks.available.mockReturnValue(false);
    mounted = mountKeyForm("asset");
    const row = [...mounted.container.querySelectorAll<HTMLElement>(".nx-form-row")].find(
      (candidate) => candidate.querySelector(".nx-label")?.textContent?.trim() === "私钥来源",
    );
    const segment = row?.querySelector(".nx-segment");
    if (!segment) throw new Error("key source segment not found");
    expect([...segment.querySelectorAll("button")].map((button) => button.textContent?.trim())).toEqual([
      "存入凭据库",
      "引用文件路径",
    ]);
  });

  it.each(KEY_SOURCE_PLATFORMS)("defaults a new %s asset to the credential vault", (_platform, available) => {
    mocks.available.mockReturnValue(available);
    mounted = mountKeyForm("asset");
    expect(inlineFileInput(mounted.container)).not.toBeNull();
    expect(mounted.container.querySelector('input[placeholder="选择或输入私钥路径"]')).toBeNull();
  });

  it.each(KEY_SOURCE_PLATFORMS)("keeps an existing %s asset with a key path in reference mode", (_platform, available) => {
    mocks.available.mockReturnValue(available);
    mounted = mountKeyForm("asset", EXISTING_KEY_ASSET);
    expect(refPathInput(mounted.container, "asset").value).toBe("/keys/id_ed25519");
  });

  it.each(KEY_SOURCE_PLATFORMS)(
    "keeps an existing %s asset bound to a file-source credential in reference mode",
    async (_platform, available) => {
      mocks.available.mockReturnValue(available);
      const listed = deferred<Credential[]>();
      mocks.listCredentials.mockReturnValue(listed.promise);
      mounted = mountKeyForm("asset", {
        ...EXISTING_KEY_ASSET,
        keyPath: null,
        credId: FILE_KEY_CREDENTIAL.id,
      });
      listed.resolve([FILE_KEY_CREDENTIAL]);
      await flushUntil(
        () =>
          mounted!.container.querySelector<HTMLInputElement>(
            'input[placeholder="选择或输入私钥路径"]',
          )?.value === "/keys/id_ed25519",
      );
      expect(refPathInput(mounted.container, "asset").value).toBe("/keys/id_ed25519");
    },
  );

  it.each(KEY_SOURCE_PLATFORMS)(
    "preserves a manual key source choice when an existing %s asset's file-source credential resolves",
    async (_platform, available) => {
      mocks.available.mockReturnValue(available);
      const listed = deferred<Credential[]>();
      mocks.listCredentials.mockReturnValue(listed.promise);
      mounted = mountKeyForm("asset", {
        ...EXISTING_KEY_ASSET,
        keyPath: null,
        credId: FILE_KEY_CREDENTIAL.id,
      });
      clickButton(mounted.container, "引用文件路径");
      clickButton(mounted.container, "存入凭据库");
      listed.resolve([FILE_KEY_CREDENTIAL]);
      await flush();
      expect(inlineFileInput(mounted.container)).not.toBeNull();
      clickButton(mounted.container, "引用文件路径");
      expect(refPathInput(mounted.container, "asset").value).toBe("");
    },
  );
});

describe.each<FormKind>(["credential", "asset"])("Web private key source for %s", (kind) => {
  it("defaults to storing keys in the credential vault instead of treating staged paths as local file references", () => {
    mocks.available.mockReturnValue(true);
    mounted = mountKeyForm(kind);
    expect(inlineFileInput(mounted.container)).not.toBeNull();
    expect(mocks.pickKeyFile).not.toHaveBeenCalled();
  });

  it("shows only the server path input without a browse button in reference mode", () => {
    mocks.available.mockReturnValue(true);
    mounted = mountKeyForm(kind);
    clickButton(mounted.container, kind === "asset" ? "引用文件路径" : "引用本地文件");
    const input = refPathInput(mounted.container, kind);
    expect(input.closest("div")?.querySelector("button")).toBeNull();
    expect(mounted.container.textContent).toContain("服务器上的私钥路径");
    expect(mocks.pickKeyFile).not.toHaveBeenCalled();
  });
});

describe.each<FormKind>(["credential", "asset"])("Desktop private key source for %s", (kind) => {
  it("keeps the native browse dialog for writing a file path reference", async () => {
    mocks.available.mockReturnValue(false);
    mocks.pickKeyFile.mockResolvedValue("/keys/id_rsa");
    mounted = mountKeyForm(kind);
    clickButton(mounted.container, kind === "asset" ? "引用文件路径" : "引用本地文件");
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

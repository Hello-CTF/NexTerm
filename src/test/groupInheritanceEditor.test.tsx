/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { chooseSelectOption, clickButton, flush, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  update: vi.fn(),
  groupList: vi.fn(),
  groupCreate: vi.fn(),
  groupUpdate: vi.fn(),
  listAssets: vi.fn(),
  listCredentials: vi.fn(),
  inheritancePreview: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      create: mocks.create,
      update: mocks.update,
      groupList: mocks.groupList,
      groupCreate: mocks.groupCreate,
      groupUpdate: mocks.groupUpdate,
      groupDelete: vi.fn(),
      list: mocks.listAssets,
      readKeyFile: vi.fn(),
      inheritancePreview: mocks.inheritancePreview,
    },
    vaultApi: { listCredentials: mocks.listCredentials, setCredential: vi.fn() },
    sessionApi: { probe: vi.fn() },
    terminalApi: {},
  };
});

import { AssetEditor } from "../features/explorer/AssetTree";
import { useUi } from "../app/store";
import type { Asset, AssetGroup } from "../ipc/commands";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.groupList.mockResolvedValue([{ id: "g1", parentId: null, name: "生产环境", sort: 0, createdAt: 1, updatedAt: 1 }]);
  mocks.listAssets.mockResolvedValue([
    { id: "jump-1", kind: "ssh", name: "bastion", host: "10.0.0.2", port: 22, options: {} },
  ]);
  mocks.listCredentials.mockResolvedValue([
    { id: "pw-1", name: "prod-password", kind: "password", usedBy: [] },
    { id: "key-1", name: "prod-key", kind: "private_key", source: "vault", usedBy: [] },
  ]);
  mocks.inheritancePreview.mockResolvedValue({ fields: [], error: "" });
  mocks.groupCreate.mockResolvedValue({ id: "g2" });
  mocks.groupUpdate.mockResolvedValue({ id: "g1" });
  mocks.create.mockResolvedValue({ id: "a1" });
  mocks.update.mockResolvedValue({ id: "a1" });
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function mountEditor(props: Record<string, unknown>) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(AssetEditor, { onClose: vi.fn(), onSaved: vi.fn(), ...props } as never),
    ),
  );
}

describe("group editor connection defaults", () => {
  it("saves username, authentication and jump host with a new group", async () => {
    mountEditor({ kind: "group" });
    await flush();
    const name = mounted!.container.querySelector<HTMLInputElement>('input[placeholder="生产环境"]')!;
    setInputValue(name, "预发");
    clickButton(mounted!.container, "连接默认值");
    const username = mounted!.container.querySelector<HTMLInputElement>('input[placeholder="留空则不设置"]')!;
    setInputValue(username, "deploy");
    chooseSelectOption(mounted!.container, "默认认证", "密码");
    await flush();
    chooseSelectOption(mounted!.container, "默认凭据", "prod-password");
    chooseSelectOption(mounted!.container, "默认跳板机", "bastion");
    clickButton(mounted!.container, "保存");
    await flush();
    expect(mocks.groupCreate).toHaveBeenCalledWith("预发", undefined, {
      username: "deploy", authKind: "password", keyPath: "", credId: "pw-1", jumpAssetId: "jump-1",
    });
  });

  it("edits the defaults of an existing group and can clear them", async () => {
    const group: AssetGroup = {
      id: "g1", parentId: null, name: "生产环境", sort: 0, createdAt: 1, updatedAt: 1,
      defaultUsername: "deploy", defaultAuthKind: "key", defaultKeyPath: "~/.ssh/id", defaultCredId: null, defaultJumpAssetId: "jump-1",
    };
    mountEditor({ kind: "group", initialGroup: group });
    await flush();
    expect(mounted!.container.textContent).toContain("编辑分组");
    const username = mounted!.container.querySelector<HTMLInputElement>('input[placeholder="留空则不设置"]')!;
    expect(username.value).toBe("deploy");
    expect(mounted!.container.querySelector<HTMLInputElement>('input[aria-label="默认私钥路径"]')!.value).toBe("~/.ssh/id");
    setInputValue(username, "");
    chooseSelectOption(mounted!.container, "默认跳板机", "不设置");
    clickButton(mounted!.container, "保存");
    await flush();
    expect(mocks.groupUpdate).toHaveBeenCalledWith("g1", "生产环境", null, {
      username: "", authKind: "key", keyPath: "~/.ssh/id", credId: "", jumpAssetId: "",
    });
  });
});

describe("asset editor inherited values", () => {
  const member = {
    id: "a1", kind: "ssh", name: "web", groupId: "g1", host: "10.0.0.1", port: 22, username: null,
    authKind: null, keyPath: null, credId: null, options: {}, tags: "", note: "",
  } as unknown as Asset;

  it("shows the inherited value with its source group for every empty field", async () => {
    mocks.inheritancePreview.mockResolvedValue({
      error: "",
      fields: [
        { field: "username", applied: true, value: "deploy", groupId: "g1", groupName: "生产环境", reason: "" },
        { field: "auth", applied: true, value: "密码 · prod-password", groupId: "g1", groupName: "生产环境", reason: "" },
        { field: "jump", applied: true, value: "bastion", groupId: "g1", groupName: "生产环境", reason: "" },
      ],
    });
    mountEditor({ kind: "asset", initial: member });
    await flush();
    await flush();
    expect(mocks.inheritancePreview).toHaveBeenCalledWith(expect.objectContaining({
      assetId: "a1", kind: "ssh", groupId: "g1", username: "", authKind: "", credId: "", jumpAssetId: "",
    }));
    const username = mounted!.container.querySelector<HTMLInputElement>('input[placeholder^="deploy"]');
    expect(username?.placeholder).toBe("deploy（继承自 生产环境）");
    expect(mounted!.container.querySelector('[data-testid="inherit-username"]')?.textContent).toContain("继承自 生产环境：deploy");
    expect(mounted!.container.querySelector('[data-testid="inherit-auth"]')?.textContent).toContain("继承自 生产环境：密码 · prod-password");
    expect(mounted!.container.querySelector('button[aria-label="认证方式"]')?.textContent).toContain("继承自 生产环境");
  });

  it("explains a skipped default instead of showing it as inherited", async () => {
    const rdp = { ...member, kind: "rdp", port: 3389 } as Asset;
    mocks.inheritancePreview.mockResolvedValue({
      error: "",
      fields: [{ field: "auth", applied: false, value: "", groupId: "g1", groupName: "生产环境", reason: "RDP 仅支持密码凭据，已跳过分组默认的私钥凭据" }],
    });
    mountEditor({ kind: "asset", initial: rdp });
    await flush();
    await flush();
    const skipped = mounted!.container.querySelector('[data-testid="inherit-skipped-auth"]');
    expect(skipped?.textContent).toContain("已跳过 生产环境 的默认值");
    expect(skipped?.textContent).toContain("私钥凭据");
    expect(mounted!.container.querySelector('[data-testid="inherit-auth"]')).toBeNull();
  });

  it("does not claim inheritance for a field the asset sets itself", async () => {
    mocks.inheritancePreview.mockResolvedValue({
      error: "",
      fields: [{ field: "username", applied: true, value: "deploy", groupId: "g1", groupName: "生产环境", reason: "" }],
    });
    mountEditor({ kind: "asset", initial: { ...member, username: "alice" } });
    await flush();
    await flush();
    expect(mounted!.container.querySelector('[data-testid="inherit-username"]')).toBeNull();
  });

  it("keeps inheriting on save: unset username and authentication stay unset (NULL), not rewritten to empty", async () => {
    mountEditor({ kind: "asset", initial: member });
    await flush();
    clickButton(mounted!.container, "保存");
    await flush();
    expect(mocks.update).toHaveBeenCalledWith(expect.objectContaining({
      id: "a1", username: null, authKind: null, keyPath: null, credId: null,
    }));
  });

  it("shows a group default that cannot be used as an error naming the group", async () => {
    mocks.inheritancePreview.mockResolvedValue({ fields: [], error: "分组「生产环境」的默认凭据不存在，请在分组连接默认值中重新选择" });
    mountEditor({ kind: "asset", initial: member });
    await flush();
    await flush();
    expect(mounted!.container.querySelector('[role="alert"]')?.textContent).toContain("生产环境");
  });
});

describe("asset editor never invents connection values", () => {
  const rdp = (extra: Record<string, unknown> = {}) => ({
    id: "r1", kind: "rdp", name: "win", groupId: "g1", host: "10.0.0.5", port: 3389, username: null,
    authKind: null, keyPath: null, credId: null, options: {}, tags: "", note: "", ...extra,
  }) as unknown as Asset;

  it("a new RDP asset saved before the preview answers stores no username", async () => {
    mocks.inheritancePreview.mockReturnValue(new Promise(() => undefined));
    mountEditor({ kind: "asset", presetGroupId: "g1" });
    await flush();
    chooseSelectOption(mounted!.container, "分组", "生产环境");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="web-01"]')!, "win");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="1.2.3.4"]')!, "10.0.0.5");
    chooseSelectOption(mounted!.container, "类型", "RDP");
    clickButton(mounted!.container, "保存");
    await flush();
    expect(mocks.create).toHaveBeenCalledWith(expect.objectContaining({ kind: "rdp", username: "" }));
  });

  it("a new SSH asset also keeps the username empty", async () => {
    mocks.inheritancePreview.mockResolvedValue({ fields: [], error: "" });
    mountEditor({ kind: "asset", presetGroupId: "g1" });
    await flush();
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="web-01"]')!, "web");
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="1.2.3.4"]')!, "10.0.0.5");
    clickButton(mounted!.container, "保存");
    await flush();
    expect(mocks.create).toHaveBeenCalledWith(expect.objectContaining({ kind: "ssh", username: "" }));
  });

  it("choosing 不使用密码 stores the explicit none authentication", async () => {
    mountEditor({ kind: "asset", initial: rdp() });
    await flush();
    chooseSelectOption(mounted!.container, "密码", "不使用密码");
    clickButton(mounted!.container, "保存");
    await flush();
    expect(mocks.update).toHaveBeenCalledWith(expect.objectContaining({ id: "r1", authKind: "none", credId: null }));
  });

  it("an RDP asset stored as none opens with 不使用密码 selected and previews as its own authentication", async () => {
    mountEditor({ kind: "asset", initial: rdp({ authKind: "none" }) });
    await flush();
    await flush();
    expect(mounted!.container.querySelector('button[aria-label="密码"]')?.textContent).toContain("不使用密码");
    expect(mocks.inheritancePreview).toHaveBeenCalledWith(expect.objectContaining({ kind: "rdp", authKind: "none" }));
  });

  it("a prompting RDP asset tells the preview it asks for the password itself", async () => {
    mountEditor({ kind: "asset", initial: rdp({ options: { promptPassword: true } }) });
    await flush();
    await flush();
    expect(mocks.inheritancePreview).toHaveBeenCalledWith(expect.objectContaining({ promptPassword: true }));
  });

  it("an unrelated save keeps NULL username and authKind NULL and the credential of an SSH asset without authKind", async () => {
    const ssh = {
      id: "s1", kind: "ssh", name: "web", groupId: "g1", host: "10.0.0.1", port: 22, username: null,
      authKind: null, keyPath: null, credId: "pw-1", options: {}, tags: "", note: "",
    } as unknown as Asset;
    mountEditor({ kind: "asset", initial: ssh });
    await flush();
    setInputValue(mounted!.container.querySelector<HTMLInputElement>('input[placeholder="web-01"]')!, "web-2");
    clickButton(mounted!.container, "保存");
    await flush();
    expect(mocks.update).toHaveBeenCalledWith(expect.objectContaining({
      id: "s1", username: null, authKind: null, credId: "pw-1",
    }));
  });
});

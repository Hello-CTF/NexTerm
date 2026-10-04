/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, deferred, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  update: vi.fn(),
  groupList: vi.fn(),
  listCredentials: vi.fn(),
  probe: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      create: mocks.create,
      update: mocks.update,
      groupList: mocks.groupList,
      groupCreate: vi.fn(),
      groupUpdate: vi.fn(),
      groupDelete: vi.fn(),
      list: vi.fn(),
      readKeyFile: vi.fn(),
      saveKeyFile: vi.fn(),
      auditQuery: vi.fn(),
    },
    vaultApi: {
      listCredentials: mocks.listCredentials,
      setCredential: vi.fn(),
    },
    sessionApi: {
      probe: mocks.probe,
    },
    terminalApi: {},
  };
});

import { AssetEditor } from "../../features/explorer/AssetTree";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.groupList.mockResolvedValue([]);
  mocks.listCredentials.mockResolvedValue([]);
  mocks.create.mockResolvedValue({ id: "a1" });
  mocks.update.mockResolvedValue({ id: "a1" });
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function withClient(node: React.ReactElement): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, node));
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

function mountEditor() {
  const onClose = vi.fn();
  const onSaved = vi.fn();
  mounted = withClient(
    createElement(AssetEditor, { kind: "asset", onClose, onSaved }),
  );
  return { onClose, onSaved };
}

function fillName(name: string) {
  const input = mounted!.container.querySelector<HTMLInputElement>("input.nx-input");
  if (!input) throw new Error("name input not found");
  setInputValue(input, name);
}

describe("AssetEditor 保存防重", () => {
  it("保存进行中禁用按钮并忽略重复点击", async () => {
    const { onSaved } = mountEditor();
    await flush();
    const gate = deferred<{ id: string }>();
    mocks.create.mockImplementation(() => gate.promise);
    fillName("web-01");
    const saveBtn = [...mounted!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "保存",
    ) as HTMLButtonElement;
    expect(saveBtn.disabled).toBe(false);
    click(saveBtn);
    await flushUntil(() => text().includes("保存中…"));
    expect(saveBtn.disabled).toBe(true);
    const cancelBtn = [...mounted!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "取消",
    ) as HTMLButtonElement;
    expect(cancelBtn.disabled).toBe(true);
    click(saveBtn);
    click(saveBtn);
    expect(mocks.create).toHaveBeenCalledTimes(1);
    gate.resolve({ id: "a1" });
    await flushUntil(() => onSaved.mock.calls.length > 0);
    expect(onSaved).toHaveBeenCalledOnce();
  });

  it("保存失败显示内联错误并恢复可点", async () => {
    mountEditor();
    await flush();
    mocks.create.mockRejectedValueOnce(new Error("名称已存在"));
    fillName("web-01");
    clickButton(mounted!.container, "保存");
    await flushUntil(() => text().includes("名称已存在"));
    const saveBtn = [...mounted!.container.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "保存",
    ) as HTMLButtonElement;
    expect(saveBtn.disabled).toBe(false);
    mocks.create.mockResolvedValueOnce({ id: "a1" });
    click(saveBtn);
    await flushUntil(() => !text().includes("名称已存在"));
    expect(mocks.create).toHaveBeenCalledTimes(2);
  });
});

/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, flushUntil as flushUntilRaw, mount, setInputValue, type MountedView } from "./reactTestUtils";

async function flushUntil(predicate: () => boolean): Promise<void> {
  if (predicate()) {
    await flush();
    return;
  }
  await flushUntilRaw(predicate);
}

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  save: vi.fn(),
  start: vi.fn(),
  stop: vi.fn(),
  delete: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/forwardRuleApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/forwardRuleApi")>();
  return {
    ...actual,
    forwardRuleApi: {
      list: mocks.list,
      save: mocks.save,
      start: mocks.start,
      stop: mocks.stop,
      delete: mocks.delete,
    },
  };
});
vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});

import { ForwardRules } from "../../features/forward/ForwardRules";
import { useUi } from "../../app/store";
import type { ForwardRule } from "../../ipc/forwardRuleApi";

const rule: ForwardRule = {
  id: "rule-1",
  assetId: "asset-1",
  name: "database",
  kind: "local",
  listenHost: "127.0.0.1",
  listenPort: 15432,
  targetHost: "db.internal",
  targetPort: 5432,
  status: "stopped",
  createdAt: 1,
  updatedAt: 1,
};

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.list.mockResolvedValue([]);
  mocks.save.mockResolvedValue(rule);
  mocks.start.mockResolvedValue({ ...rule, status: "active", sessionId: "session-1" });
  mocks.stop.mockResolvedValue(rule);
  mocks.delete.mockResolvedValue(undefined);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({
    pushToast: mocks.toast,
    sessions: [
      { id: "session-1", assetId: "asset-1", name: "host", kind: "ssh", status: "connected", tabs: [], createdAt: 1 },
    ],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

function mountRules(canSave = true): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(ForwardRules, {
        sessionId: "session-1",
        draft: { listenPort: "15432", targetHost: "db.internal", targetPort: "5432" },
        canSave,
        visible: true,
      }),
    ),
  );
}

async function expand(): Promise<void> {
  clickButton(mounted!.container, "已保存规则");
  await flushUntil(() => mocks.list.mock.calls.length > 0);
}

describe("saved forward rules", () => {
  it("loads only when expanded and saves the current local draft without starting it", async () => {
    mountRules();
    expect(mocks.list).not.toHaveBeenCalled();
    await expand();
    clickButton(mounted!.container, "保存当前为规则");
    await flushUntil(() => mocks.save.mock.calls.length === 1);
    expect(mocks.save).toHaveBeenCalledWith({
      assetId: "asset-1",
      name: "",
      listenPort: 15432,
      targetHost: "db.internal",
      targetPort: 5432,
    });
    expect(mocks.start).not.toHaveBeenCalled();
  });

  it("starts explicitly through the connected session for the rule asset", async () => {
    mocks.list.mockResolvedValue([rule]);
    mountRules();
    await expand();
    await flushUntil(() => mounted!.container.textContent?.includes("database") ?? false);
    expect(mocks.start).not.toHaveBeenCalled();
    clickButton(mounted!.container, "启动");
    await flushUntil(() => mocks.start.mock.calls.length === 1);
    expect(mocks.start).toHaveBeenCalledWith("rule-1", "session-1");
  });

  it("edits a stopped rule without changing its stable ID or asset", async () => {
    mocks.list.mockResolvedValue([rule]);
    mountRules();
    await expand();
    await flushUntil(() => mounted!.container.textContent?.includes("database") ?? false);
    clickButton(mounted!.container, "编辑");
    const name = mounted!.container.querySelector<HTMLInputElement>('input[aria-label="规则名称"]')!;
    setInputValue(name, "renamed");
    clickButton(mounted!.container, "保存编辑");
    await flushUntil(() => mocks.save.mock.calls.length === 1);
    expect(mocks.save).toHaveBeenCalledWith({
      id: "rule-1",
      assetId: "asset-1",
      name: "renamed",
      listenPort: 15432,
      targetHost: "db.internal",
      targetPort: 5432,
    });
    expect(mocks.start).not.toHaveBeenCalled();
  });

  it("confirms stop and deletion while keeping the operations distinct", async () => {
    mocks.list.mockResolvedValue([{ ...rule, status: "active", sessionId: "session-1" }]);
    mountRules();
    await expand();
    await flushUntil(() => mounted!.container.textContent?.includes("database") ?? false);
    clickButton(mounted!.container, "停止");
    await flushUntil(() => mocks.stop.mock.calls.length === 1);
    expect(mocks.stop).toHaveBeenCalledWith("rule-1");
    expect(mocks.delete).not.toHaveBeenCalled();
    clickButton(mounted!.container, "删除");
    await flushUntil(() => mocks.delete.mock.calls.length === 1);
    expect(mocks.delete).toHaveBeenCalledWith("rule-1");
    expect(mocks.ask).toHaveBeenCalledTimes(2);
  });
});

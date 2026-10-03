/** @vitest-environment jsdom */
//
// R42 审计钉板：断开挂载**不是** destructive —— 远端数据原样保留，重新挂载即恢复，
// 所以确认框刻意保持默认 info 级别。这条回归把该结论钉住，防止后续「无差别补 warning」
// 把它误标（Grid 报告把它列进了候选清单，逐项审计后决定不动）。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  remove: vi.fn(),
  ask: vi.fn(),
  toast: vi.fn(),
}));
vi.mock("../../ipc/commands", () => ({
  mountApi: {
    list: mocks.list,
    create: mocks.create,
    remove: mocks.remove,
  },
  sessionApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { MountPanel } from "../../features/files/MountPanel";
import { useUi } from "../../app/store";

let mounted: MountedView | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.list.mockResolvedValue([
    { id: "m1", localPoint: "Z:", remote: "\\\\nas\\share", sessionId: "s1", createdAt: 1 },
  ]);
  mocks.remove.mockResolvedValue(undefined);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: mocks.toast, sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("断开挂载确认（R42 审计结论：保持 info）", () => {
  it("unmount is reversible — the confirm stays at the default info level", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(
      createElement(
        QueryClientProvider,
        { client },
        createElement(MountPanel, { sessionId: "s1" }),
      ),
    );
    await waitFor(() => expect(mounted!.container.textContent).toContain("nas"));

    mocks.ask.mockResolvedValueOnce(false);
    clickButton(mounted!.container, "断开");
    await waitFor(() => expect(mocks.ask).toHaveBeenCalledOnce());
    // 钉住审计结论：单参数调用（没有 { kind: "warning" }）—— 断开可逆，不是 destructive
    expect(mocks.ask).toHaveBeenCalledWith("断开 Z:？");
    expect(mocks.remove).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    clickButton(mounted!.container, "断开");
    await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith("Z:", "s1"));
  });
});

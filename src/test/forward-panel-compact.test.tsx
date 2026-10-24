/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  forwardEnv: vi.fn(),
  forwardList: vi.fn(),
  forwardRemove: vi.fn(),
  ask: vi.fn(),
}));
vi.mock("../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/commands")>();
  return {
    ...actual,
    forwardApi: { env: mocks.forwardEnv, list: mocks.forwardList, remove: mocks.forwardRemove },
  };
});
vi.mock("../ui/dialogs", () => ({ ask: mocks.ask }));

import { ForwardPanel } from "../features/forward/ForwardPanel";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;
let width = 390;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(
    () => ({ width, height: 600, top: 0, left: 0, right: width, bottom: 600, x: 0, y: 0, toJSON: () => ({}) }),
  );
  mocks.forwardEnv.mockResolvedValue({ available: true, platform: "other", listenHost: "127.0.0.1" });
  mocks.forwardList.mockResolvedValue([
    { id: "f1", sessionId: "s", kind: "local", listenHost: "127.0.0.1", listenPort: 13306, targetHost: "db.internal", targetPort: 3306, status: "active" },
  ]);
  mocks.forwardRemove.mockResolvedValue(null);
  mocks.ask.mockResolvedValue(true);
  useUi.setState({ pushToast: vi.fn(), sessions: [] });
});
afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.restoreAllMocks();
});

function mountForward(): void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(ForwardPanel, { sessionId: "s" })));
}

describe("ForwardPanel on a phone-width pane", () => {
  it("lists forwards as rows without a table and keeps the stop action working", async () => {
    mountForward();
    await flush();
    expect(mounted!.container.querySelector("table")).toBeNull();
    expect(mounted!.container.textContent).toContain("db.internal:3306");
    clickButton(mounted!.container, "停止");
    await flush();
    expect(mocks.forwardRemove).toHaveBeenCalledWith("f1");
  });

  it("collapses the create form behind a toggle", async () => {
    mountForward();
    await flush();
    const toggle = clickButton(mounted!.container, "+ 新建转发");
    await flush();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(toggle.textContent).toBe("收起");
  });

  it("keeps the table on a wide pane", async () => {
    width = 1000;
    mountForward();
    await flush();
    width = 390;
    expect(mounted!.container.querySelector("table")).not.toBeNull();
    expect(Array.from(mounted!.container.querySelectorAll("button")).some((b) => b.textContent === "+ 新建转发")).toBe(false);
  });
});

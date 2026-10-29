/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

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
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("db.internal:3306"));
    expect(mounted!.container.querySelector("table")).toBeNull();
    clickButton(mounted!.container, "停止");
    await flushUntil(() => mocks.forwardRemove.mock.calls.length > 0);
    expect(mocks.forwardRemove).toHaveBeenCalledWith("f1");
  });

  it("collapses the create form behind a toggle", async () => {
    mountForward();
    await flushUntil(() => Array.from(mounted!.container.querySelectorAll("button")).some((b) => b.textContent === "+ 新建转发"));
    const toggle = clickButton(mounted!.container, "+ 新建转发");
    await flushUntil(() => toggle.getAttribute("aria-expanded") === "true");
    expect(toggle.textContent).toBe("收起");
  });

  it("keeps the table on a wide pane", async () => {
    width = 1000;
    mountForward();
    await flushUntil(() => mounted!.container.querySelector("table") !== null);
    width = 390;
    expect(mounted!.container.querySelector("table")).not.toBeNull();
    expect(Array.from(mounted!.container.querySelectorAll("button")).some((b) => b.textContent === "+ 新建转发")).toBe(false);
  });
});

describe("ForwardPanel phone form and rows", () => {
  it("lays the create form out as a two-column grid with a full-width submit and visible labels", async () => {
    mountForward();
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("db.internal:3306"));
    clickButton(mounted!.container, "+ 新建转发");
    const grid = mounted!.container.querySelector("input[aria-label='目标主机']")!.closest(".grid");
    expect(grid).not.toBeNull();
    expect(grid!.className).toContain("grid-cols-2");
    const submit = Array.from(mounted!.container.querySelectorAll("button")).find((b) => b.textContent?.trim() === "创建");
    expect(submit!.className).toContain("col-span-2");
    expect(mounted!.container.textContent).toContain("目标主机");
    expect(mounted!.container.textContent).toContain("目标端口");
  });

  it("keeps the form clear of the soft keyboard and the home indicator", async () => {
    mountForward();
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("db.internal:3306"));
    clickButton(mounted!.container, "+ 新建转发");
    const form = mounted!.container.querySelector("input[aria-label='目标主机']")!.closest(".overflow-auto")!;
    expect(form.className).toContain("var(--nx-kb-inset");
    expect(form.className).toContain("var(--nx-safe-bottom");
  });

  it("expands a truncated row on tap so the full target is readable", async () => {
    mountForward();
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("db.internal:3306"));
    const info = mounted!.container.querySelector("[aria-label$='详情']") as HTMLElement;
    expect(info.getAttribute("aria-expanded")).toBe("false");
    expect(info.querySelector(".truncate")).not.toBeNull();
    click(info);
    await flushUntil(() => info.getAttribute("aria-expanded") === "true");
    expect(info.querySelector(".truncate")).toBeNull();
    expect(info.querySelector(".break-all")).not.toBeNull();
  });
});

/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, setInputValue, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ps: vi.fn(),
  images: vi.fn(),
  logsAttach: vi.fn(),
  copyText: vi.fn(),
  bytesCb: null as null | ((bytes: Uint8Array) => void),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dockerApi: {
      ps: mocks.ps,
      images: mocks.images,
      inspect: vi.fn(),
      stats: vi.fn(),
      containerListDir: vi.fn(),
      action: vi.fn(),
      imageRemove: vi.fn(),
      imagePull: vi.fn(),
      logsAttach: mocks.logsAttach,
      execAttach: vi.fn(),
    },
    terminalApi: { closeTab: vi.fn().mockResolvedValue(undefined) },
  };
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    createBinaryChannel: vi.fn((cb: (bytes: Uint8Array) => void) => {
      mocks.bytesCb = cb;
      return { id: "ch-1" };
    }),
    disposeChannel: vi.fn(),
    onChannelReopen: () => () => undefined,
  };
});
vi.mock("../../ui/clipboard", () => ({ copyText: mocks.copyText }));

import { DockerPanel } from "../../features/docker/DockerPanel";
import { useUi } from "../../app/store";

const container = {
  id: "aaaa11112222",
  name: "a-very-long-container-name-for-a-narrow-screen",
  image: "registry.example.com/team/service:1.2.3",
  state: "running",
  status: "Up 2 hours",
  ports: "0.0.0.0:8080->80/tcp, :::8080->80/tcp",
  composeProject: null,
};

let mounted: MountedView | undefined;

function mountPanel(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(DockerPanel, { sessionId: "s1", visible: true })));
}

function emitLog(text: string): void {
  act(() => {
    mocks.bytesCb?.(new TextEncoder().encode(text));
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: vi.fn(), resyncSessions: vi.fn().mockResolvedValue(undefined), sessions: [{ id: "s1", assetId: "host-a", name: "host A", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }] });
  mocks.bytesCb = null;
  mocks.ps.mockResolvedValue([container]);
  mocks.images.mockResolvedValue([]);
  mocks.logsAttach.mockResolvedValue("tab-1");
  mocks.copyText.mockResolvedValue(undefined);
  // A 390px phone: the pane is narrower than the compact threshold.
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ width: 390, height: 700, x: 0, y: 0, top: 0, left: 0, right: 390, bottom: 700, toJSON: () => ({}) });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.restoreAllMocks();
});

async function openLogsOnPhone(): Promise<void> {
  mounted = mountPanel();
  await waitFor(() => expect(mounted!.container.textContent).toContain(container.name));
  click(mounted.container.querySelector<HTMLButtonElement>(`button[aria-label="${container.name} 更多操作"]`)!);
  const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) => el.textContent?.includes("查看日志"));
  click(item!);
  await flush();
  await waitFor(() => expect(mounted!.container.querySelector('[role="log"]')).not.toBeNull());
}

describe("docker panel on a phone-width pane", () => {
  it("renders container cards whose long name, image and ports wrap instead of being cut off", async () => {
    mounted = mountPanel();
    await waitFor(() => expect(mounted!.container.querySelector(".nx-docker-card")).not.toBeNull());
    const root = mounted.container;
    expect(root.querySelector("table")).toBeNull();
    const name = [...root.querySelectorAll("span")].find((el) => el.textContent === container.name)!;
    expect(name.className).toContain("break-all");
    expect(name.className).not.toMatch(/(^|\s)truncate(\s|$)/);
    expect(name.getAttribute("title")).toBe(container.name);
    const image = [...root.querySelectorAll(".nx-mono")].find((el) => el.textContent?.includes(container.image))!;
    expect(image.className).toContain("break-all");
    expect(root.querySelector(".nx-docker-toolbar")!.className).toContain("flex-wrap");
    // The auto-refresh interval stays reachable on a narrow pane; only its caption is hidden.
    expect(root.querySelector('[aria-label="自动刷新间隔"]')).not.toBeNull();
  });
});

describe("docker log stream on a phone-width pane", () => {
  it("offers filter, pause and copy controls and a shrinkable toolbar title", async () => {
    await openLogsOnPhone();
    const root = mounted!.container;
    const title = root.querySelector(".nx-docker-log-toolbar .nx-toolbar-title")!;
    expect(title.className).toContain("truncate");
    emitLog("alpha one\nbeta two\nalpha three\n");
    const log = root.querySelector('[role="log"]')!;
    expect(log.textContent).toContain("beta two");

    setInputValue(root.querySelector<HTMLInputElement>('input[aria-label="过滤日志"]')!, "alpha");
    expect(log.textContent).toContain("alpha one");
    expect(log.textContent).not.toContain("beta two");

    click(root.querySelector<HTMLButtonElement>('button[aria-label="复制日志"]')!);
    await flush();
    expect(mocks.copyText).toHaveBeenCalledWith("alpha one\nalpha three");

    setInputValue(root.querySelector<HTMLInputElement>('input[aria-label="过滤日志"]')!, "");
    const pause = [...root.querySelectorAll("button")].find((b) => b.textContent?.trim() === "暂停")!;
    click(pause);
    emitLog("gamma four\n");
    expect(log.textContent).not.toContain("gamma four");
    click([...root.querySelectorAll("button")].find((b) => b.textContent?.trim() === "继续")!);
    expect(log.textContent).toContain("gamma four");
  });
});

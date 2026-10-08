/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  click,
  clickButton,
  flush,
  mount,
  waitFor,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ps: vi.fn(),
  images: vi.fn(),
  overview: vi.fn(),
  logsAttach: vi.fn(),
  bytesCb: null as null | ((bytes: Uint8Array) => void),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dockerApi: {
      overview: mocks.overview,
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
    terminalApi: { closeTab: vi.fn(() => Promise.resolve()) },
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

import { DockerPanel } from "../../features/docker/DockerPanel";
import type { ContainerSummary } from "../../ipc/commands";
import { useUi } from "../../app/store";

const containerA: ContainerSummary = {
  id: "aaaa11112222",
  name: "web",
  image: "nginx:1.25",
  state: "running",
  status: "Up 2 hours",
  ports: "80/tcp",
  composeProject: null,
};

function stubScroller(el: HTMLDivElement) {
  const metrics = { scrollTop: 0, scrollHeight: 0, clientHeight: 0 };
  for (const key of ["scrollTop", "scrollHeight", "clientHeight"] as const) {
    Object.defineProperty(el, key, {
      configurable: true,
      get: () => metrics[key],
      set: (value: number) => {
        metrics[key] = value;
      },
    });
  }
  return {
    metrics,
    setMetrics(patch: Partial<typeof metrics>) {
      Object.assign(metrics, patch);
    },
    dispatchScroll() {
      act(() => {
        el.dispatchEvent(new Event("scroll"));
      });
    },
  };
}

function emitLog(text: string): void {
  act(() => {
    mocks.bytesCb?.(new TextEncoder().encode(text));
  });
}

let mounted: MountedView | undefined;

function mountPanel(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(DockerPanel, { sessionId: "s1", visible: true }),
    ),
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: vi.fn() });
  mocks.bytesCb = null;
  mocks.ps.mockResolvedValue([containerA]);
  mocks.images.mockResolvedValue([]);
  mocks.overview.mockResolvedValue({ containers: [containerA], hostStats: {} });
  mocks.logsAttach.mockResolvedValue("tab-1");
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("docker log stream follow", () => {
  async function openLogView(m: MountedView) {
    await waitFor(() =>
      expect(m.container.querySelector('button[title="查看日志"]')).not.toBeNull(),
    );
    click(m.container.querySelector('button[title="查看日志"]')!);
    await flush();
    await waitFor(() => expect(m.container.querySelector('[role="log"]')).not.toBeNull());
    return m.container.querySelector<HTMLDivElement>('[role="log"]')!;
  }

  it("exposes the log container with role=log and a label", async () => {
    mounted = mountPanel();
    const log = await openLogView(mounted);
    expect(log.getAttribute("role")).toBe("log");
    expect(log.getAttribute("aria-label")).toBe("容器日志");
  });

  it("follows new output while at the bottom", async () => {
    mounted = mountPanel();
    const log = await openLogView(mounted);
    const sc = stubScroller(log);

    sc.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    sc.dispatchScroll();
    emitLog("第一行\n");
    expect(sc.metrics.scrollTop).toBe(1000);
    expect(mounted.container.textContent).not.toContain("↓ 新输出");
  });

  it("stops forcing scroll after the user scrolls up and offers a jump back", async () => {
    mounted = mountPanel();
    const log = await openLogView(mounted);
    const sc = stubScroller(log);

    sc.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    sc.dispatchScroll();
    emitLog("第一行\n");
    expect(sc.metrics.scrollTop).toBe(1000);

    sc.setMetrics({ scrollTop: 100 });
    sc.dispatchScroll();
    sc.setMetrics({ scrollHeight: 1600 });
    emitLog("第二行\n");
    expect(sc.metrics.scrollTop).toBe(100);
    expect(mounted.container.textContent).toContain("↓ 新输出");

    clickButton(mounted.container, "↓ 新输出");
    expect(sc.metrics.scrollTop).toBe(1600);
    expect(mounted.container.textContent).not.toContain("↓ 新输出");

    sc.setMetrics({ scrollHeight: 2000 });
    emitLog("第三行\n");
    expect(sc.metrics.scrollTop).toBe(2000);
  });

  it("keeps the follow state across log lines without spurious new-output badges", async () => {
    mounted = mountPanel();
    const log = await openLogView(mounted);
    const sc = stubScroller(log);

    sc.setMetrics({ scrollHeight: 1000, clientHeight: 200, scrollTop: 800 });
    sc.dispatchScroll();
    emitLog("第一行\n");
    sc.setMetrics({ scrollHeight: 1200 });
    emitLog("第二行\n");
    expect(sc.metrics.scrollTop).toBe(1200);
    expect(mounted.container.textContent).not.toContain("↓ 新输出");
  });
});
